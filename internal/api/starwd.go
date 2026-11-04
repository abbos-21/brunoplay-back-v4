package api

import (
	"fmt"
	"net/http"

	"brunoplay/internal/admin"
	"brunoplay/internal/httpx"
)

// Star withdrawals: the player asks to take game stars out as real Telegram Stars; an admin sends them by hand.

func (d *Deps) starWdInfo(w http.ResponseWriter, r *http.Request) error {
	in, err := d.Games.StarWithdrawalInfo(r.Context(), httpx.PlayerFrom(r.Context()))
	if err != nil {
		return err
	}
	httpx.JSON(w, 200, in)
	return nil
}

func (d *Deps) starWdList(w http.ResponseWriter, r *http.Request) error {
	l, err := d.Games.StarWithdrawals(r.Context(), httpx.PlayerFrom(r.Context()).ID, 50)
	if err != nil {
		return err
	}
	httpx.JSON(w, 200, map[string]any{"data": l})
	return nil
}

type starWdReq struct {
	Stars int64 `json:"stars" validate:"required,gt=0,lte=1000000000"`
}

func (d *Deps) starWdCreate(w http.ResponseWriter, r *http.Request) error {
	var in starWdReq
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	wd, balance, err := d.Games.RequestStarWithdrawal(r.Context(), httpx.PlayerFrom(r.Context()).ID, in.Stars)
	if err != nil {
		return err
	}
	httpx.JSON(w, 200, map[string]any{"withdrawal": wd, "newBalance": balance})
	return nil
}

// ---- admin

func (d *Deps) adminStarWdList(w http.ResponseWriter, r *http.Request) error {
	l, err := d.Games.AdminStarWithdrawals(r.Context(), r.URL.Query().Get("status"), queryInt(r, "before", 0), int(queryInt(r, "limit", 50)))
	if err != nil {
		return err
	}
	httpx.JSON(w, 200, map[string]any{"withdrawals": l})
	return nil
}

func (d *Deps) adminStarWdDeliver(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	who := httpx.AdminFrom(r.Context())
	owner, stars, err := d.Games.DeliverStarWithdrawal(r.Context(), id, who.ID)
	if err != nil {
		return err
	}
	_ = admin.Audit(r.Context(), d.Pool, who, "star_withdrawal.deliver", "star_withdrawal", itoa(id), nil, map[string]any{"player": owner, "stars": stars})
	_ = d.Notify.Send(r.Context(), d.Pool, owner, "star_withdrawal_delivered", map[string]any{"stars": stars}, fmt.Sprintf("starwd:%d:delivered", id))
	httpx.JSON(w, 200, map[string]bool{"ok": true})
	return nil
}

func (d *Deps) adminStarWdCancel(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	var in rejectReq
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	who := httpx.AdminFrom(r.Context())
	owner, stars, err := d.Games.CancelStarWithdrawal(r.Context(), id, who.ID, in.Reason)
	if err != nil {
		return err
	}
	_ = admin.Audit(r.Context(), d.Pool, who, "star_withdrawal.cancel", "star_withdrawal", itoa(id), nil, map[string]any{"player": owner, "stars": stars, "reason": in.Reason})
	_ = d.Notify.Send(r.Context(), d.Pool, owner, "star_withdrawal_cancelled", map[string]any{"stars": stars, "reason": in.Reason}, fmt.Sprintf("starwd:%d:cancelled", id))
	httpx.JSON(w, 200, map[string]bool{"ok": true})
	return nil
}
