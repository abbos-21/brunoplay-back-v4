package api

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"brunoplay/internal/apperr"
	"brunoplay/internal/config"
	"brunoplay/internal/httpx"
	"brunoplay/internal/players"
	"brunoplay/internal/settings"
	"brunoplay/internal/tgauth"
)

// DefaultPlayerTokenTTL is how long a player token lives; the app signs in again with its initData when it expires.
const DefaultPlayerTokenTTL = 12 * time.Hour

// playerRoutes registers the player API. Milestones add their areas here.
func (d *Deps) playerRoutes(r chi.Router) {
	// public (no sign-in): the app asks this at start, also during maintenance
	r.Get("/app-info", httpx.Wrap(d.appInfo))

	r.Group(func(r chi.Router) {
		r.Use(d.MW.Gates)
		r.With(d.MW.Limit(d.LoginLimit, "login")).Post("/auth/telegram", httpx.Wrap(d.authTelegram))
		if d.Cfg.DevAuth && !d.Cfg.IsProd() {
			r.Post("/auth/dev", httpx.Wrap(d.authDev))
		}

		// blocked players may still read their profile (the app shows the blocked screen)
		r.Group(func(r chi.Router) {
			r.Use(d.MW.PlayerAuth(true))
			r.Get("/me", httpx.Wrap(d.me))
		})

		r.Group(func(r chi.Router) {
			r.Use(d.MW.PlayerAuth(false))
			r.Patch("/me/language", httpx.Wrap(d.setLanguage))
		})

		d.coreRoutes(r)
		d.moneyRoutes(r)
		d.socialRoutes(r)
		d.gameRoutes(r)
		d.botRoutes(r)
	})
}

// ---- views

type PlayerView struct {
	ID                int64     `json:"id"`
	TelegramID        string    `json:"telegramId"`
	Username          string    `json:"username"`
	FirstName         string    `json:"firstName"`
	LastName          string    `json:"lastName"`
	PhotoURL          string    `json:"photoUrl"`
	Language          string    `json:"language"`
	Coins             int64     `json:"coins"`
	StarsBalance      int64     `json:"starsBalance"`
	TotalCoinsEarned  int64     `json:"totalCoinsEarned"`
	Level             int       `json:"level"`
	CoinCapacityLevel int       `json:"coinCapacityLevel"`
	MiningRateLevel   int       `json:"miningRateLevel"`
	BestRideScore     int64     `json:"bestRideScore"`
	IsBlocked         bool      `json:"isBlocked"`
	BlockReason       string    `json:"blockReason,omitempty"`
	CreatedAt         time.Time `json:"createdAt"`
}

func viewOf(p *players.Player) PlayerView {
	return PlayerView{
		ID: p.ID, TelegramID: itoa(p.TelegramID), Username: p.Username, FirstName: p.FirstName, LastName: p.LastName,
		PhotoURL: p.PhotoURL, Language: p.Language, Coins: p.Coins, StarsBalance: p.Stars,
		TotalCoinsEarned: p.TotalCoinsEarned, Level: p.Level, CoinCapacityLevel: p.CapacityLevel,
		MiningRateLevel: p.MiningLevel, BestRideScore: p.BestRideScore, IsBlocked: p.IsBlocked,
		BlockReason: p.BlockReason, CreatedAt: p.CreatedAt,
	}
}

// ---- handlers

func (d *Deps) appInfo(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	st := d.Settings
	out := map[string]any{
		"supportUrl":  st.StrOr(ctx, settings.KeyLinkSupport, ""),
		"newsUrl":     st.StrOr(ctx, settings.KeyLinkNews, ""),
		"faqUrl":      st.StrOr(ctx, settings.KeyLinkFAQ, ""),
		"version":     Version,
		"botUsername": d.Cfg.BotUsername,
		"maintenance": map[string]any{
			"enabled": st.BoolOr(ctx, settings.KeyMaintenanceOn, false),
			"until":   st.StrOr(ctx, settings.KeyMaintenanceEnd, ""),
			"textEn":  st.StrOr(ctx, settings.KeyMaintenanceEN, ""),
			"textRu":  st.StrOr(ctx, settings.KeyMaintenanceRU, ""),
		},
		"minVersion": st.StrOr(ctx, settings.KeyMinVersion, ""),
	}
	d.extendAppInfo(r, out)
	httpx.JSON(w, 200, out)
	return nil
}

// extendAppInfo lets later milestones add fields (top-up bonus %, ...).
func (d *Deps) extendAppInfo(r *http.Request, out map[string]any) {
	if v, err := d.Settings.Float(r.Context(), settings.KeyTopupTonBonus); err == nil {
		out["topupTonBonusPercent"] = v
	}
}

type authReq struct {
	InitData string `json:"initData" validate:"required,max=4096"`
}

func (d *Deps) authTelegram(w http.ResponseWriter, r *http.Request) error {
	var in authReq
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	now := d.now()
	data, err := tgauth.Validate(in.InitData, d.Cfg.BotToken, d.Cfg.InitDataMaxAge, now)
	if err != nil {
		if err == tgauth.ErrExpired {
			return apperr.AuthExpired
		}
		return apperr.AuthFailed
	}
	return d.finishLogin(w, r, data.User, data.StartParam, now)
}

type devReq struct {
	TelegramID int64  `json:"telegramId" validate:"required,gt=0"`
	FirstName  string `json:"firstName" validate:"max=64"`
	Username   string `json:"username" validate:"max=64"`
	Language   string `json:"language" validate:"omitempty,oneof=en ru"`
	StartParam string `json:"startParam" validate:"max=64"`
}

// authDev signs in without Telegram. Registered only when DEV_AUTH is on and the environment is not production.
func (d *Deps) authDev(w http.ResponseWriter, r *http.Request) error {
	var in devReq
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	u := tgauth.User{ID: in.TelegramID, FirstName: in.FirstName, Username: in.Username, LanguageCode: in.Language}
	return d.finishLogin(w, r, u, in.StartParam, d.now())
}

func (d *Deps) finishLogin(w http.ResponseWriter, r *http.Request, u tgauth.User, startParam string, now time.Time) error {
	p, isNew, err := d.Players.Login(r.Context(), players.LoginInput{User: u, StartParam: startParam, IP: httpx.IP(r.Context()), Now: now})
	if err != nil {
		return err
	}
	tok, exp, err := d.PlayerTokens.Issue(itoa(p.ID), d.tokenTTL(), now)
	if err != nil {
		return err
	}
	d.onLogin(r, p, isNew)
	httpx.JSON(w, 200, map[string]any{"token": tok, "expiresAt": exp, "isNew": isNew, "user": viewOf(p)})
	return nil
}

// onLogin is the hook later milestones use (welcome message, first-visit data, ...).
func (d *Deps) onLogin(r *http.Request, p *players.Player, isNew bool) {}

func (d *Deps) me(w http.ResponseWriter, r *http.Request) error {
	p := httpx.PlayerFrom(r.Context())
	httpx.JSON(w, 200, d.userPayload(r, p))
	return nil
}

// userPayload is the profile returned by /me (extended by later milestones).
func (d *Deps) userPayload(r *http.Request, p *players.Player) map[string]any {
	v := viewOf(p)
	return map[string]any{"user": v}
}

type langReq struct {
	Language string `json:"language" validate:"required,oneof=EN RU en ru"`
}

func (d *Deps) setLanguage(w http.ResponseWriter, r *http.Request) error {
	var in langReq
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	p := httpx.PlayerFrom(r.Context())
	if err := d.Players.SetLanguage(r.Context(), p.ID, in.Language); err != nil {
		return err
	}
	httpx.JSON(w, 200, map[string]string{"language": normalizeLang(in.Language)})
	return nil
}

var _ = config.Config{}

func (d *Deps) tokenTTL() time.Duration {
	if d.PlayerTokenTTL > 0 {
		return d.PlayerTokenTTL
	}
	return DefaultPlayerTokenTTL
}
