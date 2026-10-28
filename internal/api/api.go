// Package api assembles the HTTP routes of the player API and the admin API.
package api

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"brunoplay/internal/admin"
	"brunoplay/internal/ads"
	"brunoplay/internal/bot"
	"brunoplay/internal/config"
	"brunoplay/internal/daily"
	"brunoplay/internal/db"
	"brunoplay/internal/fishing"
	"brunoplay/internal/games"
	"brunoplay/internal/httpx"
	"brunoplay/internal/notify"
	"brunoplay/internal/ops"
	"brunoplay/internal/players"
	"brunoplay/internal/ratelimit"
	"brunoplay/internal/referrals"
	"brunoplay/internal/risk"
	"brunoplay/internal/settings"
	"brunoplay/internal/shop"
	"brunoplay/internal/stats"
	"brunoplay/internal/tasks"
	"brunoplay/internal/telegram"
	"brunoplay/internal/token"
	"brunoplay/internal/ton"
	"brunoplay/internal/topup"
	"brunoplay/internal/tournaments"
	"brunoplay/internal/withdrawals"
)

// Version is set at build time (-ldflags "-X brunoplay/internal/api.Version=...").
var Version = "dev"

// Deps is everything the handlers need.
type Deps struct {
	Cfg          config.Config
	Pool         *pgxpool.Pool
	Settings     *settings.Service
	Players      *players.Store
	Admin        *admin.Service
	Ads          *ads.Service
	Fishing      *fishing.Service
	Shop         *shop.Service
	Daily        *daily.Service
	Games        *games.Service
	Notify       *notify.Service
	Stats        *stats.Service
	Risk         *risk.Service
	Ops          *ops.Service
	Bot          *bot.Bot
	Tournaments  *tournaments.Service
	Tasks        *tasks.Service
	Referrals    *referrals.Service
	TG           telegram.API
	Chain        ton.Chain
	Topup        *topup.Service
	Withdrawals  *withdrawals.Service
	PlayerTokens *token.Signer
	MW           *httpx.MW
	Now          func() time.Time
	// PlayerTokenTTL overrides the default player token lifetime (tests).
	PlayerTokenTTL time.Duration

	// stricter limiters for sensitive routes
	LoginLimit ratelimit.Limiter // admin login and player sign-in per IP
	MoneyLimit ratelimit.Limiter // money-moving POSTs per player
}

func (d *Deps) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

// NewDeps wires the shared services. Domain modules are added to Deps as the milestones land.
func NewDeps(cfg config.Config, pool *pgxpool.Pool) *Deps {
	st := settings.New(pool)
	ps := players.NewStore(pool, st)
	ptok := token.New(cfg.JWTSecret, token.Player)
	atok := token.New(cfg.AdminJWTSecr, token.Admin)
	d := &Deps{
		Cfg: cfg, Pool: pool, Settings: st, Players: ps,
		Admin:        admin.NewService(pool, atok),
		PlayerTokens: ptok,
		LoginLimit:   ratelimit.NewLocal(orF(cfg.LoginRate, 0.5), orI(cfg.LoginBurst, 20)),
		MoneyLimit:   ratelimit.NewLocal(3, 6),
	}
	d.Ads = ads.New(pool, st, cfg.AdsWebhookSec)
	d.Ads.Now = d.now
	d.Ads.SetPublicURL(cfg.PublicAPIURL)
	d.Fishing = fishing.NewService(pool, st, d.Ads, ps)
	d.Fishing.Now = d.now
	d.Shop = shop.NewService(pool, st, ps, d.Fishing)
	d.Shop.Now = d.now
	if cfg.BotToken != "" {
		d.TG = telegram.NewClient(cfg.BotToken)
	} else {
		d.TG = telegram.NewFake() // development without a bot: nothing is sent
	}
	d.Chain = ton.NewToncenter(cfg.TonAPIURL, cfg.TonAPIKey)
	d.Topup = topup.New(pool, st, ps, d.TG, d.Chain)
	d.Topup.Now = d.now
	d.Topup.Alert = d.Alert
	d.Games = games.New(pool, st, ps, d.Ads)
	d.Games.Now = d.now
	d.Daily = daily.New(pool, st, ps, d.Ads)
	d.Daily.Now = d.now
	d.Tasks = tasks.New(pool, st, ps, d.Ads, d.TG)
	d.Tasks.Now = d.now
	d.Referrals = referrals.New(pool, st, cfg.BotUsername, cfg.BotAppName)
	d.Referrals.Now = d.now
	// friends: a friend counts at level 2, the inviter earns on level-ups and on the friend's collects
	d.Shop.OnLevelUp = append(d.Shop.OnLevelUp, d.Referrals.OnLevelUp)
	d.Fishing.OnCollected = append(d.Fishing.OnCollected, d.Referrals.OnCollected)
	d.Shop.Evaluators["task_done"] = func(ctx context.Context, q db.Querier, p *players.Player, v int64) (bool, error) {
		return d.Tasks.Done(ctx, q, p.ID, v)
	}
	d.Withdrawals = withdrawals.New(pool, st, ps, d.Chain, nil, cfg.AutoPayout)
	d.Withdrawals.Now = d.now
	d.Withdrawals.Alert = d.Alert
	d.Notify = notify.New(pool, st, d.TG, cfg.WebAppURL)
	d.Notify.Now = d.now
	d.Tournaments = tournaments.New(pool, ps, d.Games)
	d.Tournaments.Now = d.now
	d.Bot = bot.New(pool, d.TG, st, cfg.WebAppURL)
	d.Bot.RememberInvite = d.Referrals.Remember
	d.Stats = stats.New(pool)
	d.Stats.Now = d.now
	d.Risk = risk.New(pool, st)
	d.Risk.Now = d.now
	d.Risk.Alert = d.Alert
	d.Ops = ops.New(pool)
	d.Ops.Now = d.now
	d.Ops.Alert = d.Alert
	d.wireBot()
	d.MW = &httpx.MW{
		Settings: st, Players: ps, PlayerTokens: ptok, Pool: pool,
		IPLimiter:   ratelimit.NewLocal(orF(cfg.IPRate, 30), orI(cfg.IPBurst, 90)),
		PlayerLimit: ratelimit.NewLocal(orF(cfg.PlayerRate, 15), orI(cfg.PlayerBurst, 45)),
		TrustProxy:  cfg.TrustedProxy, CORSOrigins: cfg.CORSOrigins,
	}
	return d
}

// Router builds the complete HTTP handler.
func (d *Deps) Router() http.Handler { return d.MW.Base(d.Routes()) }

// Routes builds the route tree (also used to generate the API reference).
func (d *Deps) Routes() chi.Router {
	r := chi.NewRouter()
	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) { httpx.JSON(w, 200, map[string]string{"status": "ok"}) })
	r.Get("/readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := d.Pool.Ping(ctx); err != nil {
			httpx.JSON(w, 503, map[string]string{"status": "database unavailable"})
			return
		}
		httpx.JSON(w, 200, map[string]string{"status": "ready", "version": Version})
	})

	r.Route("/api", func(r chi.Router) {
		r.Get("/webhooks/adsgram", httpx.Wrap(d.adsgramWebhook))
		r.Post("/webhooks/adsgram", httpx.Wrap(d.adsgramWebhook))
		r.Get("/webhooks/adsgram-task", httpx.Wrap(d.adsgramTaskWebhook))
		r.Post("/webhooks/adsgram-task", httpx.Wrap(d.adsgramTaskWebhook))
		r.Get("/webhooks/tads", httpx.Wrap(d.tadsWebhook))
		r.Post("/webhooks/tads", httpx.Wrap(d.tadsWebhook))
		r.Post("/webhooks/telegram",httpx.Wrap(d.telegramWebhook))
		r.Get("/gift-images/{key}", httpx.Wrap(d.giftImage))
		d.playerRoutes(r)
		d.adminRoutes(r)
	})
	return r
}

// Alert tells the owner something needs a human (a Telegram message to the admin chat; always logged).
func (d *Deps) Alert(ctx context.Context, text string) {
	slog.Warn("alert", "text", text)
	if d.Cfg.AlertChatID == 0 || d.TG == nil {
		return
	}
	go func() {
		c, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		if err := d.TG.SendMessage(c, d.Cfg.AlertChatID, "⚠️ "+text, telegram.SendOpts{}); err != nil {
			slog.Error("alert send failed", "err", err)
		}
	}()
}

func orF(v, def float64) float64 {
	if v > 0 {
		return v
	}
	return def
}

func orI(v, def int) int {
	if v > 0 {
		return v
	}
	return def
}
