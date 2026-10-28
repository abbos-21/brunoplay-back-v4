package api

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"brunoplay/internal/admin"
	"brunoplay/internal/apperr"
	"brunoplay/internal/ads"
	"brunoplay/internal/httpx"
	"brunoplay/internal/settings"
)

// ---- TADS reward callback

// tadsWebhook is TADS's server-to-server callback for a Fullscreen (rewarded) widget, sent when the user has watched the ad:
// POST /api/webhooks/tads?secret=<shared secret>   body {"telegram_id":"123","widget_id":"456"}
// (GET with the same two names in the query works too). TADS does not say which ad it was, so the newest open proof of that
// player that offered TADS is completed.
func (d *Deps) tadsWebhook(w http.ResponseWriter, r *http.Request) error {
	q := r.URL.Query()
	secret := q.Get("secret")
	if secret == "" {
		secret = r.Header.Get("X-Webhook-Secret")
	}
	if !d.Ads.SecretOK(secret) {
		return apperr.Unauthorized
	}
	tgRaw, widget := q.Get("telegram_id"), q.Get("widget_id")
	if r.Method == http.MethodPost {
		var body struct {
			TelegramID json.Number `json:"telegram_id"`
			WidgetID   json.Number `json:"widget_id"`
		}
		if b, err := io.ReadAll(io.LimitReader(r.Body, 4096)); err == nil && len(b) > 0 {
			if err := json.Unmarshal(b, &body); err == nil {
				if tgRaw == "" {
					tgRaw = body.TelegramID.String()
				}
				if widget == "" {
					widget = body.WidgetID.String()
				}
			}
		}
	}
	tg, err := strconv.ParseInt(strings.TrimSpace(tgRaw), 10, 64)
	if err != nil || tg <= 0 {
		return apperr.BadRequest
	}
	// a callback of another widget (not the one in the admin settings) is not ours to pay
	if want := strings.TrimSpace(d.Settings.StrOr(r.Context(), settings.KeyAdsTadsWidget, "")); want != "" && widget != "" && strings.TrimSpace(widget) != want {
		httpx.JSON(w, 200, map[string]bool{"ok": false})
		return nil
	}
	ok, err := d.Ads.CompleteBy(r.Context(), tg, "tads")
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
