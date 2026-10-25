package api

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"brunoplay/internal/admin"
	"brunoplay/internal/apperr"
	"brunoplay/internal/httpx"
)

// opsAdminRoutes: dashboard, risk review, privacy deletion (milestone 7).
func (d *Deps) opsAdminRoutes(r chi.Router) {
	r.With(d.adminAuth("dashboard.read")).Get("/dashboard", httpx.Wrap(d.adminDashboard))
	r.With(d.adminAuth("players.read")).Get("/risk", httpx.Wrap(d.adminRisk))
	r.With(d.adminAuth("players.block")).Post("/players/{id}/risk/clear", httpx.Wrap(d.adminRiskClear))
	r.With(d.adminAuth("players.delete")).Post("/players/{id}/delete", httpx.Wrap(d.adminDeletePlayer))
	r.With(d.adminAuth("ledger.read")).Get("/ledger/audit", httpx.Wrap(d.adminLedgerAudit))
}

func (d *Deps) adminDashboard(w http.ResponseWriter, r *http.Request) error {
	days := int(queryInt(r, "days", 30))
	if days < 1 || days > 365 {
		days = 30
	}
	to := d.now()
	series, err := d.Stats.Dashboard(r.Context(), to.AddDate(0, 0, -days), to)
	if err != nil {
		return err
	}
	live, err := d.Stats.Live(r.Context())
	if err != nil {
		return err
	}
	httpx.JSON(w, 200, map[string]any{"from": to.AddDate(0, 0, -days).Format("2006-01-02"), "to": to.Format("2006-01-02"), "series": series, "live": live, "generatedAt": d.now().Format(time.RFC3339)})
	return nil
}

func (d *Deps) adminRisk(w http.ResponseWriter, r *http.Request) error {
	l, err := d.Risk.List(r.Context(), int(queryInt(r, "limit", 50)))
	if err != nil {
		return err
	}
	httpx.JSON(w, 200, map[string]any{"players": l})
	return nil
}

func (d *Deps) adminRiskClear(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	if err := d.Risk.Clear(r.Context(), id); err != nil {
		return err
	}
	_ = admin.Audit(r.Context(), d.Pool, httpx.AdminFrom(r.Context()), "player.risk_clear", "player", itoa(id), nil, nil)
	httpx.JSON(w, 200, map[string]bool{"ok": true})
	return nil
}

type confirmReq struct {
	Confirm bool `json:"confirm"`
}

func (d *Deps) adminDeletePlayer(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	var in confirmReq
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	if !in.Confirm {
		return apperr.BadRequest.With("reason", "confirm must be true")
	}
	if err := d.Risk.DeletePlayer(r.Context(), id); err != nil {
		return err
	}
	_ = admin.Audit(r.Context(), d.Pool, httpx.AdminFrom(r.Context()), "player.delete", "player", itoa(id), nil, nil)
	httpx.JSON(w, 200, map[string]bool{"ok": true})
	return nil
}

func (d *Deps) adminLedgerAudit(w http.ResponseWriter, r *http.Request) error {
	m, err := d.Risk.AuditLedger(r.Context(), 100)
	if err != nil {
		return err
	}
	httpx.JSON(w, 200, map[string]any{"mismatches": m, "ok": len(m) == 0})
	return nil
}
