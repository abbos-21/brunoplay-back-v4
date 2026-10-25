package api

import (
	"crypto/subtle"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"brunoplay/internal/admin"
	"brunoplay/internal/apperr"
	"brunoplay/internal/httpx"
	"brunoplay/internal/telegram"
)

// moneyRoutes: stars top-up and withdrawals (milestone 3).
func (d *Deps) moneyRoutes(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(d.MW.PlayerAuth(false))
		r.Get("/topup/packs", httpx.Wrap(d.topupPacks))
		r.Get("/topup/orders", httpx.Wrap(d.topupOrders))
		r.Get("/topup/orders/{id}", httpx.Wrap(d.topupOrder))
		r.Get("/withdrawals/info", httpx.Wrap(d.wdInfo))
		r.Get("/withdrawals", httpx.Wrap(d.wdList))

		r.Group(func(r chi.Router) {
			r.Use(d.MW.Limit(d.MoneyLimit, "money"), d.MW.Idempotency)
			r.Post("/topup/telegram-invoice", httpx.Wrap(d.topupTelegram))
			r.Post("/topup/ton-order", httpx.Wrap(d.topupTon))
			r.Post("/withdrawals", httpx.Wrap(d.wdCreate))
			r.Post("/withdrawals/{id}/cancel", httpx.Wrap(d.wdCancel))
		})
	})
}

func (d *Deps) topupPacks(w http.ResponseWriter, r *http.Request) error {
	v, err := d.Topup.Packs(r.Context())
	if err != nil {
		return err
	}
	httpx.JSON(w, 200, v)
	return nil
}

type packReq struct {
	PackStars int64 `json:"packStars" validate:"required,gt=0"`
}

func (d *Deps) topupTelegram(w http.ResponseWriter, r *http.Request) error {
	var in packReq
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	p := httpx.PlayerFrom(r.Context())
	o, err := d.Topup.CreateTelegramOrder(r.Context(), p.ID, in.PackStars, p.Lang())
	if err != nil {
		return err
	}
	httpx.JSON(w, 200, o)
	return nil
}

func (d *Deps) topupTon(w http.ResponseWriter, r *http.Request) error {
	var in packReq
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	o, err := d.Topup.CreateTonOrder(r.Context(), httpx.PlayerFrom(r.Context()).ID, in.PackStars)
	if err != nil {
		return err
	}
	httpx.JSON(w, 200, o)
	return nil
}

func (d *Deps) topupOrders(w http.ResponseWriter, r *http.Request) error {
	l, err := d.Topup.Orders(r.Context(), httpx.PlayerFrom(r.Context()).ID, 30)
	if err != nil {
		return err
	}
	httpx.JSON(w, 200, map[string]any{"orders": l})
	return nil
}

func (d *Deps) topupOrder(w http.ResponseWriter, r *http.Request) error {
	o, err := d.Topup.Order(r.Context(), httpx.PlayerFrom(r.Context()).ID, chi.URLParam(r, "id"))
	if err != nil {
		return err
	}
	httpx.JSON(w, 200, o)
	return nil
}

// ---- withdrawals

func (d *Deps) wdInfo(w http.ResponseWriter, r *http.Request) error {
	in, err := d.Withdrawals.Info(r.Context(), httpx.PlayerFrom(r.Context()))
	if err != nil {
		return err
	}
	httpx.JSON(w, 200, in)
	return nil
}

type wdReq struct {
	TargetAddress string `json:"targetAddress" validate:"required,max=100"`
	AmountCoins   int64  `json:"amountCoins" validate:"required,gt=0"`
}

func (d *Deps) wdCreate(w http.ResponseWriter, r *http.Request) error {
	var in wdReq
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	wd, err := d.Withdrawals.Create(r.Context(), httpx.PlayerFrom(r.Context()).ID, in.AmountCoins, in.TargetAddress)
	if err != nil {
		return err
	}
	wd.PlayerID = 0
	httpx.JSON(w, 200, map[string]any{"withdrawal": wd})
	return nil
}

func (d *Deps) wdList(w http.ResponseWriter, r *http.Request) error {
	page := int(queryInt(r, "page", 1))
	list, total, err := d.Withdrawals.List(r.Context(), httpx.PlayerFrom(r.Context()).ID, page, 20)
	if err != nil {
		return err
	}
	pages := int((total + 19) / 20)
	if pages < 1 {
		pages = 1
	}
	if page < 1 {
		page = 1
	}
	httpx.JSON(w, 200, map[string]any{"data": list, "total": total, "page": page, "totalPages": pages})
	return nil
}

func (d *Deps) wdCancel(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	wd, err := d.Withdrawals.Cancel(r.Context(), httpx.PlayerFrom(r.Context()).ID, id)
	if err != nil {
		return err
	}
	wd.PlayerID = 0
	httpx.JSON(w, 200, map[string]any{"withdrawal": wd})
	return nil
}

// ---- Telegram webhook (payments here; the bot adds commands in milestone 6)

func (d *Deps) telegramWebhook(w http.ResponseWriter, r *http.Request) error {
	secret := r.Header.Get("X-Telegram-Bot-Api-Secret-Token")
	if d.Cfg.BotWebhookSec == "" || subtle.ConstantTimeCompare([]byte(secret), []byte(d.Cfg.BotWebhookSec)) != 1 {
		return apperr.Unauthorized
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		return apperr.BadRequest
	}
	var u telegram.Update
	if err := json.Unmarshal(body, &u); err != nil {
		return apperr.BadRequest
	}
	// answer Telegram quickly; the work is bounded and idempotent, so a retry is harmless
	if err := d.handleUpdate(r, &u); err != nil {
		slog.Error("telegram update failed", "update", u.UpdateID, "err", err)
		return err
	}
	httpx.JSON(w, 200, map[string]bool{"ok": true})
	return nil
}

func (d *Deps) handleUpdate(r *http.Request, u *telegram.Update) error {
	ctx := r.Context()
	switch {
	case u.PreCheckoutQuery != nil:
		return d.Topup.HandlePreCheckout(ctx, u.PreCheckoutQuery)
	case u.Message != nil && u.Message.SuccessfulPayment != nil && u.Message.From != nil:
		return d.Topup.HandleSuccessfulPayment(ctx, u.Message.From.ID, u.Message.SuccessfulPayment)
	case u.Message != nil && u.Message.RefundedPayment != nil && u.Message.From != nil:
		return d.Topup.HandleRefund(ctx, u.Message.From.ID, u.Message.RefundedPayment)
	}
	return d.botUpdate(r, u)
}

// ---- admin: top-ups and withdrawals

func (d *Deps) moneyAdminRoutes(r chi.Router) {
	r.With(d.adminAuth("withdrawals.read")).Get("/withdrawals", httpx.Wrap(d.adminWdList))
	r.With(d.adminAuth("withdrawals.read")).Get("/withdrawals/{id}", httpx.Wrap(d.adminWdGet))
	r.With(d.adminAuth("withdrawals.write")).Post("/withdrawals/{id}/processing", httpx.Wrap(d.adminWdProcessing))
	r.With(d.adminAuth("withdrawals.write")).Post("/withdrawals/{id}/complete", httpx.Wrap(d.adminWdComplete))
	r.With(d.adminAuth("withdrawals.write")).Post("/withdrawals/{id}/reject", httpx.Wrap(d.adminWdReject))
	r.With(d.adminAuth("payments.read")).Get("/topup/orders", httpx.Wrap(d.adminTopupOrders))
	r.With(d.adminAuth("payments.write")).Post("/topup/orders/{id}/credit", httpx.Wrap(d.adminTopupCredit))
	r.With(d.adminAuth("payments.write")).Post("/topup/orders/{id}/refund", httpx.Wrap(d.adminTopupRefund))
}

func (d *Deps) adminWdList(w http.ResponseWriter, r *http.Request) error {
	l, err := d.Withdrawals.AdminList(r.Context(), r.URL.Query().Get("status"), queryInt(r, "before", 0), int(queryInt(r, "limit", 50)))
	if err != nil {
		return err
	}
	httpx.JSON(w, 200, map[string]any{"withdrawals": l})
	return nil
}

func (d *Deps) adminWdGet(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	wd, err := d.Withdrawals.Get(r.Context(), id)
	if err != nil {
		return err
	}
	httpx.JSON(w, 200, wd)
	return nil
}

func (d *Deps) adminWdProcessing(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	who := httpx.AdminFrom(r.Context())
	wd, err := d.Withdrawals.MarkProcessing(r.Context(), id, who.ID)
	if err != nil {
		return err
	}
	_ = admin.Audit(r.Context(), d.Pool, who, "withdrawal.processing", "withdrawal", itoa(id), nil, nil)
	httpx.JSON(w, 200, wd)
	return nil
}

type completeReq struct {
	TxHash string `json:"txHash" validate:"required,max=200"`
}

func (d *Deps) adminWdComplete(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	var in completeReq
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	who := httpx.AdminFrom(r.Context())
	wd, err := d.Withdrawals.Complete(r.Context(), id, who.ID, in.TxHash)
	if err != nil {
		return err
	}
	_ = admin.Audit(r.Context(), d.Pool, who, "withdrawal.complete", "withdrawal", itoa(id), nil, map[string]any{"txHash": in.TxHash})
	httpx.JSON(w, 200, wd)
	return nil
}

type rejectReq struct {
	Reason string `json:"reason" validate:"required,max=300"`
	Force  bool   `json:"force"`
}

func (d *Deps) adminWdReject(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	var in rejectReq
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	who := httpx.AdminFrom(r.Context())
	wd, err := d.Withdrawals.Reject(r.Context(), id, who.ID, in.Reason, in.Force)
	if err != nil {
		return err
	}
	_ = admin.Audit(r.Context(), d.Pool, who, "withdrawal.reject", "withdrawal", itoa(id), nil, in)
	httpx.JSON(w, 200, wd)
	return nil
}

func (d *Deps) adminTopupOrders(w http.ResponseWriter, r *http.Request) error {
	var before time.Time
	if ms := queryInt(r, "before", 0); ms > 0 {
		before = time.UnixMilli(ms)
	}
	l, err := d.Topup.AdminOrders(r.Context(), r.URL.Query().Get("status"), before, int(queryInt(r, "limit", 50)))
	if err != nil {
		return err
	}
	httpx.JSON(w, 200, map[string]any{"orders": l})
	return nil
}

type creditReq struct {
	Reason string `json:"reason" validate:"required,max=300"`
}

func (d *Deps) adminTopupCredit(w http.ResponseWriter, r *http.Request) error {
	var in creditReq
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	id := chi.URLParam(r, "id")
	if err := d.Topup.AdminCredit(r.Context(), id, in.Reason); err != nil {
		return err
	}
	_ = admin.Audit(r.Context(), d.Pool, httpx.AdminFrom(r.Context()), "topup.credit", "topup_order", id, nil, in)
	httpx.JSON(w, 200, map[string]bool{"ok": true})
	return nil
}

func (d *Deps) adminTopupRefund(w http.ResponseWriter, r *http.Request) error {
	id := chi.URLParam(r, "id")
	if err := d.Topup.RefundByAdmin(r.Context(), id); err != nil {
		return err
	}
	_ = admin.Audit(r.Context(), d.Pool, httpx.AdminFrom(r.Context()), "topup.refund", "topup_order", id, nil, nil)
	httpx.JSON(w, 200, map[string]bool{"ok": true})
	return nil
}

var _ = strconv.Itoa
