package api

import (
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"brunoplay/internal/admin"
	"brunoplay/internal/apperr"
	"brunoplay/internal/ads"
	"brunoplay/internal/httpx"
	"brunoplay/internal/settings"
)

// ---- Monetag postback

var ymidRe = regexp.MustCompile(`^[0-9a-f]{1,64}$`)

// monetagWebhook is Monetag's postback (GET), sent when the ad starts to show and again for a click or a retry:
// /api/webhooks/monetag?secret=..&ymid=<our proof id>&event_type=impression&reward_event_type=valued&zone_id=..&telegram_id=..
// Only "valued" events count. The ymid names the exact proof, so a repeat finds it already completed and is answered 200 anyway
// (Monetag retries anything that is not 2xx for about 38 hours).
func (d *Deps) monetagWebhook(w http.ResponseWriter, r *http.Request) error {
	q := r.URL.Query()
	if !d.Ads.SecretOK(q.Get("secret")) {
		return apperr.Unauthorized
	}
	no := func() error { httpx.JSON(w, 200, map[string]bool{"ok": false}); return nil }
	// the app may retry a stuck ad with "<proof id>_2": the part before the underscore is the proof
	ymid, _, _ := strings.Cut(strings.TrimSpace(q.Get("ymid")), "_")
	if !ymidRe.MatchString(ymid) || q.Get("reward_event_type") != "valued" {
		return no()
	}
	// a postback of another zone (not the one in the admin settings) is not ours to pay
	if want := strings.TrimSpace(d.Settings.StrOr(r.Context(), settings.KeyAdsMonetagZone, "")); want != "" && q.Get("zone_id") != "" && strings.TrimSpace(q.Get("zone_id")) != want {
		return no()
	}
	tg, _ := strconv.ParseInt(strings.TrimSpace(q.Get("telegram_id")), 10, 64) // empty when Telegram did not tell: not checked then
	ok, err := d.Ads.CompleteView(r.Context(), ymid, max(tg, 0), "monetag")
	if err != nil {
		return err
	}
	httpx.JSON(w, 200, map[string]bool{"ok": ok})
	return nil
}

// ---- admin: the Ads page (providers, on/off, priority)

func (d *Deps) adProviderRoutes(r chi.Router) {
	r.With(d.adminAuth("settings.read")).Get("/ads/providers", httpx.Wrap(d.adminProviders))
	r.With(d.adminAuth("settings.write.ads")).Put("/ads/providers", httpx.Wrap(d.adminProvidersSave))
}

func (d *Deps) adminProviders(w http.ResponseWriter, r *http.Request) error {
	httpx.JSON(w, 200, map[string]any{
		"providers": d.Ads.ProvidersView(r.Context()),
		"required":  d.Ads.Required(r.Context()),
	})
	return nil
}

type providersReq struct {
	// Providers is the whole list in priority order: first = asked first.
	Providers []ads.Entry `json:"providers"`
	// Fields are provider settings by key (only the keys the providers list as their fields are accepted).
	Fields map[string]any `json:"fields"`
}

func (d *Deps) adminProvidersSave(w http.ResponseWriter, r *http.Request) error {
	var in providersReq
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	who := httpx.AdminFrom(r.Context())
	before := d.Ads.ProvidersView(r.Context())
	if in.Fields == nil {
		in.Fields = map[string]any{}
	}
	if err := d.Ads.SaveProviders(r.Context(), in.Providers, in.Fields, who.ID); err != nil {
		return err
	}
	_ = admin.Audit(r.Context(), d.Pool, who, "ads.providers", "setting", settings.KeyAdsProviders, before, in)
	httpx.JSON(w, 200, map[string]any{"providers": d.Ads.ProvidersView(r.Context()), "required": d.Ads.Required(r.Context())})
	return nil
}
