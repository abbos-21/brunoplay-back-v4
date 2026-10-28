package api

import (
	"log/slog"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"brunoplay/internal/ads"
	"brunoplay/internal/apperr"
	"brunoplay/internal/gamecfg"
	"brunoplay/internal/httpx"
)

// coreRoutes: fishing, ads, shop, levels (milestone 2).
func (d *Deps) coreRoutes(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(d.MW.PlayerAuth(false))
		r.Get("/fishing", httpx.Wrap(d.fishingState))
		r.Get("/shop/cards", httpx.Wrap(d.shopCards))
		r.Get("/shop/boosters", httpx.Wrap(d.shopBoosters))
		r.Get("/levels", httpx.Wrap(d.levels))
		r.Post("/ads", httpx.Wrap(d.adsRequest))
		r.Post("/ads/failed", httpx.Wrap(d.adsFailed))

		// money-moving: per-player limit and Idempotency-Key support
		r.Group(func(r chi.Router) {
			r.Use(d.MW.Limit(d.MoneyLimit, "money"), d.MW.Idempotency)
			r.Post("/fishing/start", httpx.Wrap(d.fishingStart))
			r.Post("/fishing/collect", httpx.Wrap(d.fishingCollect))
			r.Post("/shop/cards/{card}/upgrade", httpx.Wrap(d.cardUpgrade))
			r.Post("/shop/boosters/{type}/buy", httpx.Wrap(d.boosterBuy))
		})
	})
}

func (d *Deps) fishingState(w http.ResponseWriter, r *http.Request) error {
	p := httpx.PlayerFrom(r.Context())
	// a full tank of a player with Autocollect is collected right now (the coins arrive and the tank starts over)
	if err := d.Fishing.SettleAuto(r.Context(), p.ID); err != nil {
		return err
	}
	st, err := d.Fishing.State(r.Context(), d.Pool, p)
	if err != nil {
		return err
	}
	httpx.JSON(w, 200, st)
	return nil
}

type adReq struct {
	AdViewID string `json:"adViewId" validate:"omitempty,max=64"`
}

func (d *Deps) fishingStart(w http.ResponseWriter, r *http.Request) error {
	var in adReq
	if err := httpx.DecodeOptional(r, &in); err != nil {
		return err
	}
	st, err := d.Fishing.Start(r.Context(), httpx.PlayerFrom(r.Context()).ID, in.AdViewID)
	if err != nil {
		return err
	}
	httpx.JSON(w, 200, st)
	return nil
}

func (d *Deps) fishingCollect(w http.ResponseWriter, r *http.Request) error {
	var in adReq
	if err := httpx.DecodeOptional(r, &in); err != nil {
		return err
	}
	res, err := d.Fishing.Collect(r.Context(), httpx.PlayerFrom(r.Context()).ID, in.AdViewID)
	if err != nil {
		return err
	}
	httpx.JSON(w, 200, res)
	return nil
}

func (d *Deps) shopCards(w http.ResponseWriter, r *http.Request) error {
	p := httpx.PlayerFrom(r.Context())
	cards, err := d.Shop.Cards(r.Context(), d.Pool, p)
	if err != nil {
		return err
	}
	httpx.JSON(w, 200, map[string]any{"cards": cards})
	return nil
}

func (d *Deps) cardUpgrade(w http.ResponseWriter, r *http.Request) error {
	res, err := d.Shop.Upgrade(r.Context(), httpx.PlayerFrom(r.Context()).ID, chi.URLParam(r, "card"))
	if err != nil {
		return err
	}
	httpx.JSON(w, 200, res)
	return nil
}

func (d *Deps) shopBoosters(w http.ResponseWriter, r *http.Request) error {
	p := httpx.PlayerFrom(r.Context())
	b, err := d.Shop.Boosters(r.Context(), d.Pool, p)
	if err != nil {
		return err
	}
	httpx.JSON(w, 200, map[string]any{"boosters": b})
	return nil
}

func (d *Deps) boosterBuy(w http.ResponseWriter, r *http.Request) error {
	b, err := d.Shop.BuyBooster(r.Context(), httpx.PlayerFrom(r.Context()).ID, chi.URLParam(r, "type"))
	if err != nil {
		return err
	}
	httpx.JSON(w, 200, b)
	return nil
}

func (d *Deps) levels(w http.ResponseWriter, r *http.Request) error {
	p := httpx.PlayerFrom(r.Context())
	l, err := d.Shop.Levels(r.Context(), p.Lang())
	if err != nil {
		return err
	}
	httpx.JSON(w, 200, map[string]any{"levels": l, "current": p.Level, "maxLevel": gamecfg.MaxLevel})
	return nil
}

type placementReq struct {
	Placement string `json:"placement" validate:"required,max=32"`
}

func (d *Deps) adsRequest(w http.ResponseWriter, r *http.Request) error {
	var in placementReq
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	p := httpx.PlayerFrom(r.Context())
	if err := d.checkAdPlacement(r, p.ID, in.Placement); err != nil {
		return err
	}
	v, err := d.Ads.Request(r.Context(), p.ID, in.Placement)
	if err != nil {
		return err
	}
	httpx.JSON(w, 200, v)
	return nil
}

// checkAdPlacement is where later modules refuse an ad that could not be used (daily limits are checked at request time too).
func (d *Deps) checkAdPlacement(r *http.Request, playerID int64, placement string) error {
	if !ads.ValidPlacement(placement) {
		return apperr.Validation.With("field", "placement")
	}
	if !d.Ads.Enabled(r.Context(), placement) {
		return apperr.AdDisabled
	}
	return d.placementGuards(r, playerID, placement)
}

func (d *Deps) adsFailed(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Placement string `json:"placement" validate:"required,max=32"`
		// Reason is what the providers said ("tads: No TADS ads; adsgram: ..."): only written to the log, so the owner can see why no ad appeared
		Reason string `json:"reason" validate:"max=300"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	slog.Info("ad failed", "player", httpx.PlayerFrom(r.Context()).ID, "placement", in.Placement, "reason", in.Reason)
	v, err := d.Ads.Failed(r.Context(), httpx.PlayerFrom(r.Context()).ID, in.Placement)
	if err != nil {
		return err
	}
	httpx.JSON(w, 200, map[string]any{"fallback": v != nil, "ad": v})
	return nil
}

// adsgramWebhook is Adsgram's server-to-server reward callback:
// GET/POST /api/webhooks/adsgram?userid=<telegram id>&blockid=<block>&secret=<shared secret>
func (d *Deps) adsgramWebhook(w http.ResponseWriter, r *http.Request) error {
	q := r.URL.Query()
	secret := q.Get("secret")
	if secret == "" {
		secret = r.Header.Get("X-Adsgram-Secret")
	}
	if !d.Ads.SecretOK(secret) {
		return apperr.Unauthorized
	}
	tg, err := strconv.ParseInt(q.Get("userid"), 10, 64)
	if err != nil {
		tg, err = strconv.ParseInt(q.Get("userId"), 10, 64)
		if err != nil {
			return apperr.BadRequest
		}
	}
	ok, err := d.Ads.Complete(r.Context(), tg, q.Get("blockid"))
	if err != nil {
		return err
	}
	httpx.JSON(w, 200, map[string]bool{"ok": ok})
	return nil
}

// adsgramTaskWebhook is the reward callback of the Adsgram TASK block (its own Reward URL, same secret):
// GET/POST /api/webhooks/adsgram-task?userid=<telegram id>&secret=<shared secret>
// It only records that the player completed a sponsored task; the game then claims the coins (POST /api/ads/task/claim).
func (d *Deps) adsgramTaskWebhook(w http.ResponseWriter, r *http.Request) error {
	q := r.URL.Query()
	secret := q.Get("secret")
	if secret == "" {
		secret = r.Header.Get("X-Adsgram-Secret")
	}
	if !d.Ads.SecretOK(secret) {
		return apperr.Unauthorized
	}
	tg, err := strconv.ParseInt(q.Get("userid"), 10, 64)
	if err != nil {
		tg, err = strconv.ParseInt(q.Get("userId"), 10, 64)
		if err != nil {
			return apperr.BadRequest
		}
	}
	ok, err := d.Ads.SponsoredDone(r.Context(), tg, q.Get("blockid"))
	if err != nil {
		return err
	}
	httpx.JSON(w, 200, map[string]bool{"ok": ok})
	return nil
}
