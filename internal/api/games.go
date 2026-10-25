package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"brunoplay/internal/admin"
	"brunoplay/internal/httpx"
)

// gameRoutes: Spin, Chest, Ride, gift collection, winners (milestone 5).
func (d *Deps) gameRoutes(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(d.MW.PlayerAuth(false))
		r.Get("/spin/tiers", httpx.Wrap(d.spinTiers))
		r.Get("/spin/winners", httpx.Wrap(d.spinWinners))
		r.Get("/chest", httpx.Wrap(d.chestState))
		r.Get("/ride/me", httpx.Wrap(d.rideMe))
		r.Get("/gifts", httpx.Wrap(d.giftList))

		r.Group(func(r chi.Router) {
			r.Use(d.MW.Limit(d.MoneyLimit, "money"), d.MW.Idempotency)
			r.Post("/spin", httpx.Wrap(d.spin))
			r.Post("/chest/round", httpx.Wrap(d.chestRound))
			r.Post("/chest/pick", httpx.Wrap(d.chestPick))
			r.Post("/ride/start", httpx.Wrap(d.rideStart))
			r.Post("/ride/finish", httpx.Wrap(d.rideFinish))
			r.Post("/gifts/{id}/sell", httpx.Wrap(d.giftSell))
			r.Post("/gifts/{id}/claim", httpx.Wrap(d.giftClaim))
		})
	})
}

func (d *Deps) spinTiers(w http.ResponseWriter, r *http.Request) error {
	t, err := d.Games.Tiers(r.Context(), httpx.PlayerFrom(r.Context()).Lang())
	if err != nil {
		return err
	}
	httpx.JSON(w, 200, map[string]any{"tiers": t})
	return nil
}

type spinReq struct {
	Tier  string `json:"tier" validate:"required,max=32"`
	Count int    `json:"count" validate:"required,gt=0,lte=10"`
}

func (d *Deps) spin(w http.ResponseWriter, r *http.Request) error {
	var in spinReq
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	p := httpx.PlayerFrom(r.Context())
	res, err := d.Games.Spin(r.Context(), p.ID, in.Tier, in.Count, p.Lang())
	if err != nil {
		return err
	}
	httpx.JSON(w, 200, res)
	return nil
}

func (d *Deps) spinWinners(w http.ResponseWriter, r *http.Request) error {
	page := int(queryInt(r, "page", 1))
	list, total, err := d.Games.Winners(r.Context(), page, 20)
	if err != nil {
		return err
	}
	pages := int((total + 19) / 20)
	if pages < 1 {
		pages = 1
	}
	httpx.JSON(w, 200, map[string]any{"data": list, "total": total, "page": page, "totalPages": pages})
	return nil
}

func (d *Deps) chestState(w http.ResponseWriter, r *http.Request) error {
	p := httpx.PlayerFrom(r.Context())
	st, err := d.Games.Chest(r.Context(), p.ID, p.Lang())
	if err != nil {
		return err
	}
	httpx.JSON(w, 200, st)
	return nil
}

func (d *Deps) chestRound(w http.ResponseWriter, r *http.Request) error {
	p := httpx.PlayerFrom(r.Context())
	st, err := d.Games.StartChestRound(r.Context(), p.ID, p.Lang())
	if err != nil {
		return err
	}
	httpx.JSON(w, 200, st)
	return nil
}

type pickReq struct {
	Slot int `json:"slot" validate:"gte=0,lte=11"`
}

func (d *Deps) chestPick(w http.ResponseWriter, r *http.Request) error {
	var in pickReq
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	p := httpx.PlayerFrom(r.Context())
	res, err := d.Games.PickChest(r.Context(), p.ID, in.Slot, p.Lang())
	if err != nil {
		return err
	}
	httpx.JSON(w, 200, res)
	return nil
}

func (d *Deps) rideMe(w http.ResponseWriter, r *http.Request) error {
	info, err := d.Games.RideInfo(r.Context(), httpx.PlayerFrom(r.Context()).ID)
	if err != nil {
		return err
	}
	httpx.JSON(w, 200, info)
	return nil
}

func (d *Deps) rideStart(w http.ResponseWriter, r *http.Request) error {
	var in adReq
	if err := httpx.DecodeOptional(r, &in); err != nil {
		return err
	}
	run, err := d.Games.StartRide(r.Context(), httpx.PlayerFrom(r.Context()).ID, in.AdViewID)
	if err != nil {
		return err
	}
	httpx.JSON(w, 200, run)
	return nil
}

type rideFinishReq struct {
	RunID int64 `json:"runId" validate:"required,gt=0"`
	Coins int64 `json:"coins" validate:"gte=0,lte=1000000"`
	Score int64 `json:"score" validate:"gte=0,lte=1000000000"`
}

func (d *Deps) rideFinish(w http.ResponseWriter, r *http.Request) error {
	var in rideFinishReq
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	res, err := d.Games.FinishRide(r.Context(), httpx.PlayerFrom(r.Context()).ID, in.RunID, in.Coins, in.Score)
	if err != nil {
		return err
	}
	httpx.JSON(w, 200, res)
	return nil
}

func (d *Deps) giftList(w http.ResponseWriter, r *http.Request) error {
	p := httpx.PlayerFrom(r.Context())
	l, err := d.Games.Collection(r.Context(), p.ID, p.Lang(), 100, queryInt(r, "before", 0))
	if err != nil {
		return err
	}
	httpx.JSON(w, 200, map[string]any{"gifts": l})
	return nil
}

func (d *Deps) giftSell(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	stars, bal, err := d.Games.SellGift(r.Context(), httpx.PlayerFrom(r.Context()).ID, id)
	if err != nil {
		return err
	}
	httpx.JSON(w, 200, map[string]any{"stars": stars, "newBalance": bal})
	return nil
}

func (d *Deps) giftClaim(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	p := httpx.PlayerFrom(r.Context())
	g, err := d.Games.ClaimGift(r.Context(), p.ID, id, p.Lang())
	if err != nil {
		return err
	}
	httpx.JSON(w, 200, g)
	return nil
}

// ---- admin

func (d *Deps) gameAdminRoutes(r chi.Router) {
	r.With(d.adminAuth("gifts.read")).Get("/gifts/claims", httpx.Wrap(d.adminClaims))
	r.With(d.adminAuth("gifts.write")).Post("/gifts/{id}/deliver", httpx.Wrap(d.adminDeliver))
	r.With(d.adminAuth("gifts.write")).Post("/gifts/{id}/reject", httpx.Wrap(d.adminGiftReject))
	r.With(d.adminAuth("dashboard.read")).Get("/games/guard", httpx.Wrap(d.adminGuard))
}

func (d *Deps) adminClaims(w http.ResponseWriter, r *http.Request) error {
	l, err := d.Games.Claims(r.Context(), r.URL.Query().Get("status"), queryInt(r, "before", 0), int(queryInt(r, "limit", 50)))
	if err != nil {
		return err
	}
	httpx.JSON(w, 200, map[string]any{"claims": l})
	return nil
}

func (d *Deps) adminDeliver(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	who := httpx.AdminFrom(r.Context())
	owner, err := d.Games.DeliverGift(r.Context(), id, who.ID)
	if err != nil {
		return err
	}
	_ = admin.Audit(r.Context(), d.Pool, who, "gift.deliver", "player_gift", itoa(id), nil, map[string]any{"player": owner})
	d.onGiftDelivered(r, owner, id, true, "")
	httpx.JSON(w, 200, map[string]bool{"ok": true})
	return nil
}

func (d *Deps) adminGiftReject(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	var in rejectReq
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	who := httpx.AdminFrom(r.Context())
	owner, err := d.Games.RejectGift(r.Context(), id, who.ID, in.Reason)
	if err != nil {
		return err
	}
	_ = admin.Audit(r.Context(), d.Pool, who, "gift.reject", "player_gift", itoa(id), nil, in)
	d.onGiftDelivered(r, owner, id, false, in.Reason)
	httpx.JSON(w, 200, map[string]bool{"ok": true})
	return nil
}

func (d *Deps) adminGuard(w http.ResponseWriter, r *http.Request) error {
	stats, err := d.Games.GuardStats(r.Context())
	if err != nil {
		return err
	}
	ev, err := d.Games.GuardEvents(r.Context(), 0, 50)
	if err != nil {
		return err
	}
	httpx.JSON(w, 200, map[string]any{"stats": stats, "events": ev})
	return nil
}
