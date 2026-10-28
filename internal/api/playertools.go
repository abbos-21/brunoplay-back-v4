package api

import (
	"net/http"

	"brunoplay/internal/admin"
	"brunoplay/internal/apperr"
	"brunoplay/internal/httpx"
)

// Admin tools for one player (perm players.manage: super admin only): set the balances, start the player over.

type adjustReq struct {
	Coins  *int64 `json:"coins" validate:"omitempty,gte=0"`
	Stars  *int64 `json:"stars" validate:"omitempty,gte=0"`
	Reason string `json:"reason" validate:"required,min=3,max=200"`
}

func (d *Deps) adminAdjust(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	var in adjustReq
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	if in.Coins == nil && in.Stars == nil {
		return apperr.Validation.With("field", "coins")
	}
	who := httpx.AdminFrom(r.Context())
	var was map[string]any
	if p, err := d.Players.Get(r.Context(), d.Pool, id); err == nil && p != nil {
		was = map[string]any{"coins": p.Coins, "stars": p.Stars}
	}
	coins, stars, err := d.Players.SetBalances(r.Context(), id, in.Coins, in.Stars, who.ID, in.Reason)
	if err != nil {
		return err
	}
	_ = admin.Audit(r.Context(), d.Pool, who, "player.balance", "player", itoa(id), was, map[string]any{"coins": coins, "stars": stars, "reason": in.Reason})
	httpx.JSON(w, 200, map[string]any{"coins": coins, "stars": stars})
	return nil
}

type resetReq struct {
	Confirm bool   `json:"confirm"`
	Reason  string `json:"reason" validate:"required,min=3,max=200"`
}

func (d *Deps) adminReset(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	var in resetReq
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	if !in.Confirm {
		return apperr.BadRequest.With("reason", "confirm must be true")
	}
	who := httpx.AdminFrom(r.Context())
	tg, err := d.Players.Reset(r.Context(), id, who.ID, in.Reason)
	if err != nil {
		return err
	}
	_ = admin.Audit(r.Context(), d.Pool, who, "player.reset", "player", itoa(id), nil, map[string]any{"telegramId": tg, "reason": in.Reason})
	httpx.JSON(w, 200, map[string]any{"ok": true, "telegramId": tg})
	return nil
}
