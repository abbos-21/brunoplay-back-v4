package api

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"brunoplay/internal/admin"
	"brunoplay/internal/apperr"
	"brunoplay/internal/httpx"
)

func itoa(i int64) string { return strconv.FormatInt(i, 10) }

func normalizeLang(s string) string { return strings.ToUpper(s) }

func pathID(r *http.Request, name string) (int64, error) {
	id, err := strconv.ParseInt(chi.URLParam(r, name), 10, 64)
	if err != nil || id <= 0 {
		return 0, apperr.BadRequest
	}
	return id, nil
}

func queryInt(r *http.Request, name string, def int64) int64 {
	if v, err := strconv.ParseInt(r.URL.Query().Get(name), 10, 64); err == nil {
		return v
	}
	return def
}

// adminAuth requires a valid admin token and, when perm is not empty, that the role grants it.
func (d *Deps) adminAuth(perm string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := r.Header.Get("Authorization")
			raw := strings.TrimPrefix(h, "Bearer ")
			if h == "" || raw == h {
				httpx.Fail(w, r, apperr.Unauthorized)
				return
			}
			who, err := d.Admin.Verify(r.Context(), raw)
			if err != nil {
				httpx.Fail(w, r, err)
				return
			}
			if perm != "" && !admin.Can(who.Role, perm) {
				httpx.Fail(w, r, apperr.Forbidden)
				return
			}
			next.ServeHTTP(w, r.WithContext(httpx.WithAdmin(r.Context(), who)))
		})
	}
}

// adminRoutes registers /api/admin/*. Milestones add their modules (see ToR 2.3).
func (d *Deps) adminRoutes(r chi.Router) {
	r.Route("/admin", func(r chi.Router) {
		r.With(d.MW.Limit(d.LoginLimit, "adminlogin")).Post("/auth/login", httpx.Wrap(d.adminLogin))
		r.With(d.MW.Limit(d.LoginLimit, "adminrefresh")).Post("/auth/refresh", httpx.Wrap(d.adminRefresh))
		r.Post("/auth/logout", httpx.Wrap(d.adminLogout))

		r.Group(func(r chi.Router) {
			r.Use(d.adminAuth(""))
			r.Get("/me", httpx.Wrap(d.adminMe))
			r.Post("/me/password", httpx.Wrap(d.adminPassword))
		})

		// settings
		r.With(d.adminAuth("settings.read")).Get("/settings", httpx.Wrap(d.adminSettings))
		r.With(d.adminAuth("settings.read")).Get("/settings/missing", httpx.Wrap(d.adminSettingsMissing))
		r.With(d.adminAuth("settings.read")).Get("/settings/history", httpx.Wrap(d.adminSettingsHistory))
		r.With(d.adminAuth("")).Put("/settings/{key}", httpx.Wrap(d.adminSettingSet)) // permission by the key's group

		// admins and audit
		r.With(d.adminAuth("admins.manage")).Get("/admins", httpx.Wrap(d.adminList))
		r.With(d.adminAuth("admins.manage")).Post("/admins", httpx.Wrap(d.adminCreate))
		r.With(d.adminAuth("admins.manage")).Patch("/admins/{id}", httpx.Wrap(d.adminUpdate))
		r.With(d.adminAuth("")).Get("/audit", httpx.Wrap(d.adminAudit))

		// players
		r.With(d.adminAuth("players.read")).Get("/players", httpx.Wrap(d.adminPlayers))
		r.With(d.adminAuth("players.read")).Get("/players/{id}", httpx.Wrap(d.adminPlayer))
		r.With(d.adminAuth("ledger.read")).Get("/players/{id}/ledger", httpx.Wrap(d.adminPlayerLedger))
		r.With(d.adminAuth("players.manage")).Post("/players/{id}/adjust", httpx.Wrap(d.adminAdjust))
		r.With(d.adminAuth("players.manage")).Post("/players/{id}/reset", httpx.Wrap(d.adminReset))
		r.With(d.adminAuth("players.block")).Post("/players/{id}/block", httpx.Wrap(d.adminBlock))
		r.With(d.adminAuth("players.block")).Post("/players/{id}/unblock", httpx.Wrap(d.adminUnblock))
		r.With(d.adminAuth("players.note")).Patch("/players/{id}/flags", httpx.Wrap(d.adminFlags))
		r.With(d.adminAuth("players.inviteBonus")).Put("/players/{id}/invite-bonus", httpx.Wrap(d.adminInviteBonus))

		d.adminModules(r)
	})
}

// adminModules is where later milestones attach their admin routes.
func (d *Deps) adminModules(r chi.Router) {
	d.moneyAdminRoutes(r)
	d.socialAdminRoutes(r)
	d.gameAdminRoutes(r)
	d.botAdminRoutes(r)
	d.opsAdminRoutes(r)
}
