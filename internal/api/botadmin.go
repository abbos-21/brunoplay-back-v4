package api

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"brunoplay/internal/admin"
	"brunoplay/internal/httpx"
	"brunoplay/internal/notify"
	"brunoplay/internal/tournaments"
)

// botAdminRoutes: notification templates, broadcasts and tournaments (milestone 6).
func (d *Deps) botAdminRoutes(r chi.Router) {
	r.With(d.adminAuth("notifications.read")).Get("/notifications/templates", httpx.Wrap(d.adminTemplates))
	r.With(d.adminAuth("notifications.write")).Put("/notifications/templates/{type}/{lang}", httpx.Wrap(d.adminTemplateSet))
	r.With(d.adminAuth("notifications.write")).Delete("/notifications/templates/{type}/{lang}", httpx.Wrap(d.adminTemplateReset))
	r.With(d.adminAuth("dashboard.read")).Get("/notifications/stats", httpx.Wrap(d.adminNotifStats))

	r.With(d.adminAuth("notifications.read")).Get("/broadcasts", httpx.Wrap(d.adminBroadcasts))
	r.With(d.adminAuth("notifications.write")).Post("/broadcasts", httpx.Wrap(d.adminBroadcastCreate))
	r.With(d.adminAuth("notifications.read")).Get("/broadcasts/{id}", httpx.Wrap(d.adminBroadcastGet))
	r.With(d.adminAuth("notifications.write")).Put("/broadcasts/{id}", httpx.Wrap(d.adminBroadcastUpdate))
	r.With(d.adminAuth("notifications.write")).Post("/broadcasts/{id}/test", httpx.Wrap(d.adminBroadcastTest))
	r.With(d.adminAuth("notifications.write")).Post("/broadcasts/{id}/start", httpx.Wrap(d.adminBroadcastStart))
	r.With(d.adminAuth("notifications.write")).Post("/broadcasts/{id}/cancel", httpx.Wrap(d.adminBroadcastCancel))

	r.With(d.adminAuth("tournaments.read")).Get("/tournaments", httpx.Wrap(d.adminTournaments))
	r.With(d.adminAuth("tournaments.write")).Post("/tournaments", httpx.Wrap(d.adminTournamentCreate))
	r.With(d.adminAuth("tournaments.write")).Put("/tournaments/{id}", httpx.Wrap(d.adminTournamentUpdate))
	r.With(d.adminAuth("tournaments.read")).Get("/tournaments/{id}/review", httpx.Wrap(d.adminTournamentReview))
	r.With(d.adminAuth("tournaments.write")).Post("/tournaments/{id}/disqualify", httpx.Wrap(d.adminTournamentDisqualify))
	r.With(d.adminAuth("payouts.tournament")).Post("/tournaments/{id}/pay", httpx.Wrap(d.adminTournamentPay))
	r.With(d.adminAuth("tournaments.write")).Post("/tournaments/{id}/cancel", httpx.Wrap(d.adminTournamentCancel))
}

func (d *Deps) adminTemplates(w http.ResponseWriter, r *http.Request) error {
	l, err := d.Notify.Templates(r.Context())
	if err != nil {
		return err
	}
	httpx.JSON(w, 200, map[string]any{"templates": l})
	return nil
}

type templateReq struct {
	Text   string `json:"text" validate:"required,max=1000"`
	Button string `json:"button" validate:"max=40"`
}

func (d *Deps) adminTemplateSet(w http.ResponseWriter, r *http.Request) error {
	var in templateReq
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	who := httpx.AdminFrom(r.Context())
	typ, lang := chi.URLParam(r, "type"), chi.URLParam(r, "lang")
	if err := d.Notify.SetTemplate(r.Context(), typ, lang, in.Text, in.Button, who.ID); err != nil {
		return err
	}
	_ = admin.Audit(r.Context(), d.Pool, who, "notification.template", "template", typ+"/"+lang, nil, in)
	httpx.JSON(w, 200, map[string]bool{"ok": true})
	return nil
}

func (d *Deps) adminTemplateReset(w http.ResponseWriter, r *http.Request) error {
	typ, lang := chi.URLParam(r, "type"), chi.URLParam(r, "lang")
	if err := d.Notify.ResetTemplate(r.Context(), typ, lang); err != nil {
		return err
	}
	_ = admin.Audit(r.Context(), d.Pool, httpx.AdminFrom(r.Context()), "notification.template_reset", "template", typ+"/"+lang, nil, nil)
	httpx.JSON(w, 200, map[string]bool{"ok": true})
	return nil
}

func (d *Deps) adminNotifStats(w http.ResponseWriter, r *http.Request) error {
	since := d.now().Add(-time.Duration(queryInt(r, "hours", 24)) * time.Hour)
	s, err := d.Notify.Stats(r.Context(), since)
	if err != nil {
		return err
	}
	httpx.JSON(w, 200, map[string]any{"stats": s})
	return nil
}

func (d *Deps) adminBroadcasts(w http.ResponseWriter, r *http.Request) error {
	l, err := d.Notify.ListBroadcasts(r.Context())
	if err != nil {
		return err
	}
	httpx.JSON(w, 200, map[string]any{"broadcasts": l})
	return nil
}

func (d *Deps) adminBroadcastCreate(w http.ResponseWriter, r *http.Request) error {
	var in notify.BroadcastInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	who := httpx.AdminFrom(r.Context())
	b, err := d.Notify.CreateBroadcast(r.Context(), in, who.ID)
	if err != nil {
		return err
	}
	_ = admin.Audit(r.Context(), d.Pool, who, "broadcast.create", "broadcast", itoa(b.ID), nil, b)
	httpx.JSON(w, 201, b)
	return nil
}

func (d *Deps) adminBroadcastGet(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	b, err := d.Notify.GetBroadcast(r.Context(), id)
	if err != nil {
		return err
	}
	stats, err := d.Notify.BroadcastStats(r.Context(), id)
	if err != nil {
		return err
	}
	httpx.JSON(w, 200, map[string]any{"broadcast": b, "stats": stats})
	return nil
}

func (d *Deps) adminBroadcastUpdate(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	var in notify.BroadcastInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	b, err := d.Notify.UpdateBroadcast(r.Context(), id, in)
	if err != nil {
		return err
	}
	_ = admin.Audit(r.Context(), d.Pool, httpx.AdminFrom(r.Context()), "broadcast.update", "broadcast", itoa(id), nil, b)
	httpx.JSON(w, 200, b)
	return nil
}

func (d *Deps) adminBroadcastTest(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	who := httpx.AdminFrom(r.Context())
	a, err := d.Admin.Get(r.Context(), who.ID)
	if err != nil {
		return err
	}
	var tg int64
	if a.TelegramID != nil {
		tg = *a.TelegramID
	}
	if err := d.Notify.TestBroadcast(r.Context(), id, tg); err != nil {
		return err
	}
	httpx.JSON(w, 200, map[string]bool{"ok": true})
	return nil
}

type startReq struct {
	At *time.Time `json:"at"`
}

func (d *Deps) adminBroadcastStart(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	var in startReq
	if err := httpx.DecodeOptional(r, &in); err != nil {
		return err
	}
	b, err := d.Notify.StartBroadcast(r.Context(), id, in.At)
	if err != nil {
		return err
	}
	_ = admin.Audit(r.Context(), d.Pool, httpx.AdminFrom(r.Context()), "broadcast.start", "broadcast", itoa(id), nil, map[string]any{"at": in.At})
	httpx.JSON(w, 200, b)
	return nil
}

func (d *Deps) adminBroadcastCancel(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	if err := d.Notify.CancelBroadcast(r.Context(), id); err != nil {
		return err
	}
	_ = admin.Audit(r.Context(), d.Pool, httpx.AdminFrom(r.Context()), "broadcast.cancel", "broadcast", itoa(id), nil, nil)
	httpx.JSON(w, 200, map[string]bool{"ok": true})
	return nil
}

// ---- tournaments

func (d *Deps) adminTournaments(w http.ResponseWriter, r *http.Request) error {
	l, err := d.Tournaments.AdminList(r.Context())
	if err != nil {
		return err
	}
	httpx.JSON(w, 200, map[string]any{"tournaments": l})
	return nil
}

func (d *Deps) adminTournamentCreate(w http.ResponseWriter, r *http.Request) error {
	var in tournaments.Input
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	who := httpx.AdminFrom(r.Context())
	t, err := d.Tournaments.Create(r.Context(), in, who.ID)
	if err != nil {
		return err
	}
	_ = admin.Audit(r.Context(), d.Pool, who, "tournament.create", "tournament", itoa(t.ID), nil, t)
	httpx.JSON(w, 201, t)
	return nil
}

func (d *Deps) adminTournamentUpdate(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	var in tournaments.Input
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	t, err := d.Tournaments.Update(r.Context(), id, in)
	if err != nil {
		return err
	}
	_ = admin.Audit(r.Context(), d.Pool, httpx.AdminFrom(r.Context()), "tournament.update", "tournament", itoa(id), nil, t)
	httpx.JSON(w, 200, t)
	return nil
}

func (d *Deps) adminTournamentReview(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	t, err := d.Tournaments.Get(r.Context(), id)
	if err != nil {
		return err
	}
	rows, err := d.Tournaments.Review(r.Context(), id, int(queryInt(r, "limit", 100)))
	if err != nil {
		return err
	}
	httpx.JSON(w, 200, map[string]any{"tournament": t, "entries": rows})
	return nil
}

type dqReq struct {
	PlayerID int64 `json:"playerId" validate:"required,gt=0"`
	On       bool  `json:"on"`
}

func (d *Deps) adminTournamentDisqualify(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	var in dqReq
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	if err := d.Tournaments.Disqualify(r.Context(), id, in.PlayerID, in.On); err != nil {
		return err
	}
	_ = admin.Audit(r.Context(), d.Pool, httpx.AdminFrom(r.Context()), "tournament.disqualify", "tournament", itoa(id), nil, in)
	httpx.JSON(w, 200, map[string]bool{"ok": true})
	return nil
}

func (d *Deps) adminTournamentPay(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	rows, err := d.Tournaments.Pay(r.Context(), id)
	if err != nil {
		return err
	}
	_ = admin.Audit(r.Context(), d.Pool, httpx.AdminFrom(r.Context()), "tournament.pay", "tournament", itoa(id), nil, map[string]any{"winners": len(rows)})
	httpx.JSON(w, 200, map[string]any{"payouts": rows})
	return nil
}

func (d *Deps) adminTournamentCancel(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	n, err := d.Tournaments.Cancel(r.Context(), id)
	if err != nil {
		return err
	}
	_ = admin.Audit(r.Context(), d.Pool, httpx.AdminFrom(r.Context()), "tournament.cancel", "tournament", itoa(id), nil, map[string]any{"refunded": n})
	httpx.JSON(w, 200, map[string]any{"refunded": n})
	return nil
}
