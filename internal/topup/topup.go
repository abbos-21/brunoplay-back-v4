// Package topup buys game stars: with Telegram Stars (an invoice) or with TON (a transfer with a unique comment that the
// server finds on the blockchain). Every credit is idempotent: one payment can pay one order once.
package topup

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"math"
	"math/big"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"brunoplay/internal/apperr"
	"brunoplay/internal/db"
	"brunoplay/internal/ledger"
	"brunoplay/internal/players"
	"brunoplay/internal/settings"
	"brunoplay/internal/telegram"
	"brunoplay/internal/ton"
)

const (
	MethodTG  = "TG_STARS"
	MethodTON = "TON"

	commentPrefix = "bp"
	lateGrace     = 7 * 24 * time.Hour // an expired order is still honoured if the money arrives within a week
)

// PaidEvent is passed to the hooks after an order was credited.
type PaidEvent struct {
	PlayerID int64
	OrderID  string
	Method   string
	Stars    int64
	Bonus    int64
}

type Service struct {
	pool     *pgxpool.Pool
	settings *settings.Service
	players  *players.Store
	tg       telegram.API
	chain    ton.Chain
	Now      func() time.Time

	OnPaid     []func(ctx context.Context, tx pgx.Tx, ev PaidEvent) error
	OnRefunded []func(ctx context.Context, tx pgx.Tx, playerID int64, orderID string, stars int64) error
	// Alert is called for things that need a human (a paid invoice without an order, an underpaid TON order).
	Alert func(ctx context.Context, text string)
}

func New(pool *pgxpool.Pool, st *settings.Service, ps *players.Store, tg telegram.API, chain ton.Chain) *Service {
	return &Service{pool: pool, settings: st, players: ps, tg: tg, chain: chain, Now: time.Now}
}

func (s *Service) now() time.Time { return s.Now() }
func (s *Service) alert(ctx context.Context, format string, a ...any) {
	slog.Warn("topup", "msg", fmt.Sprintf(format, a...))
	if s.Alert != nil {
		s.Alert(ctx, fmt.Sprintf(format, a...))
	}
}

func newID() string {
	b := make([]byte, 10)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// ---- packs

type pack struct {
	Stars int64 `json:"stars"`
}

func (s *Service) packs(ctx context.Context) ([]pack, error) {
	return settings.Get[[]pack](ctx, s.settings, settings.KeyTopupPacks)
}

func (s *Service) validPack(ctx context.Context, stars int64) error {
	ps, err := s.packs(ctx)
	if err != nil {
		return err
	}
	for _, p := range ps {
		if p.Stars == stars && stars > 0 {
			return nil
		}
	}
	return apperr.TopupPackInvalid
}

func bonusOf(stars int64, percent float64) int64 {
	if percent <= 0 {
		return 0
	}
	return int64(math.Floor(float64(stars) * percent / 100))
}

// tgPrice is the price in Telegram Stars (rounded up so the project never receives less than the pack is worth).
func tgPrice(stars int64, ratio float64) int64 {
	if ratio <= 0 {
		return 0
	}
	p := int64(math.Ceil(float64(stars) / ratio))
	if p < 1 {
		p = 1
	}
	return p
}

// tonPrice is the price in nanoTON, rounded up to a WHOLE TON (1.449 TON becomes 2, 72.46 becomes 73): round numbers are easier to read
// and to pay, and the same function prices the list and the order, so what the player sees is what the wallet asks for.
func tonPrice(stars int64, starsPerTon float64) int64 {
	if starsPerTon <= 0 {
		return 0
	}
	n := new(big.Float).SetPrec(200).SetInt64(stars)
	n.Mul(n, new(big.Float).SetInt64(ton.Nano))
	n.Quo(n, new(big.Float).SetPrec(200).SetFloat64(starsPerTon))
	i, acc := n.Int(nil)
	if acc != big.Exact {
		i.Add(i, big.NewInt(1))
	}
	whole := new(big.Int).Add(i, big.NewInt(ton.Nano-1))
	whole.Div(whole, big.NewInt(ton.Nano))
	return whole.Int64() * ton.Nano
}

type PackView struct {
	Stars         int64  `json:"stars"`
	TelegramStars int64  `json:"telegramStars"`
	TgBonusStars  int64  `json:"telegramBonusStars"`
	TonNano       int64  `json:"tonNano"`
	Ton           string `json:"ton"`
	TonBonusStars int64  `json:"tonBonusStars"`
}

type PacksView struct {
	Packs           []PackView `json:"packs"`
	TonBonusPercent float64    `json:"tonBonusPercent"`
	TgBonusPercent  float64    `json:"telegramBonusPercent"`
	TonAvailable    bool       `json:"tonAvailable"`
	TgAvailable     bool       `json:"telegramAvailable"`
}

func (s *Service) Packs(ctx context.Context) (*PacksView, error) {
	ps, err := s.packs(ctx)
	if err != nil {
		return nil, err
	}
	ratio, _ := s.settings.Float(ctx, settings.KeyTopupTGRatio)
	tgB, _ := s.settings.Float(ctx, settings.KeyTopupTGBonus)
	tonB, _ := s.settings.Float(ctx, settings.KeyTopupTonBonus)
	spt, sptErr := s.settings.Float(ctx, settings.KeyTopupStarsPerTon)
	wallet, werr := s.settings.Str(ctx, settings.KeyTopupTonWallet)
	out := &PacksView{TonBonusPercent: tonB, TgBonusPercent: tgB, TgAvailable: ratio > 0, TonAvailable: sptErr == nil && werr == nil && wallet != ""}
	for _, p := range ps {
		v := PackView{Stars: p.Stars, TelegramStars: tgPrice(p.Stars, ratio), TgBonusStars: bonusOf(p.Stars, tgB), TonBonusStars: bonusOf(p.Stars, tonB)}
		if out.TonAvailable {
			v.TonNano = tonPrice(p.Stars, spt)
			v.Ton = ton.TonString(v.TonNano)
		}
		out.Packs = append(out.Packs, v)
	}
	return out, nil
}

// ---- orders

type Order struct {
	ID         string     `json:"id"`
	Method     string     `json:"method"`
	Status     string     `json:"status"`
	Stars      int64      `json:"stars"`
	BonusStars int64      `json:"bonusStars"`
	Price      int64      `json:"price"`
	CreatedAt  time.Time  `json:"createdAt"`
	ExpiresAt  time.Time  `json:"expiresAt"`
	PaidAt     *time.Time `json:"paidAt"`

	InvoiceLink string `json:"invoiceLink,omitempty"`
	Address     string `json:"address,omitempty"`    // TON: where to send
	Comment     string `json:"comment,omitempty"`    // TON: the comment that identifies the order
	PayloadB64  string `json:"payload,omitempty"`    // TON Connect: the comment as a cell
	AmountNano  int64  `json:"amountNano,omitempty"` // TON: how much to send
}

const orderCols = `id, player_id, method, status, pack_stars, bonus_stars, price, ton_comment, ton_wallet, invoice_link,
	tg_charge_id, ton_tx_hash, paid_amount, created_at, expires_at, paid_at`

type orderRow struct {
	Order
	PlayerID int64
	TonCmt   *string
	Wallet   string
	TgCharge *string
	TxHash   *string
	PaidAmt  int64
}

func scanOrder(r pgx.Row) (*orderRow, error) {
	var o orderRow
	err := r.Scan(&o.ID, &o.PlayerID, &o.Method, &o.Status, &o.Stars, &o.BonusStars, &o.Price, &o.TonCmt, &o.Wallet, &o.InvoiceLink,
		&o.TgCharge, &o.TxHash, &o.PaidAmt, &o.CreatedAt, &o.ExpiresAt, &o.PaidAt)
	if db.IsNoRows(err) {
		return nil, apperr.NotFound
	}
	if o.TonCmt != nil {
		o.Comment = *o.TonCmt
	}
	return &o, err
}

// CreateTelegramOrder makes an order and a Telegram Stars invoice link for it.
func (s *Service) CreateTelegramOrder(ctx context.Context, playerID, packStars int64, lang apperr.Lang) (*Order, error) {
	if err := s.validPack(ctx, packStars); err != nil {
		return nil, err
	}
	ratio, err := s.settings.Float(ctx, settings.KeyTopupTGRatio)
	if err != nil {
		return nil, err
	}
	pct, _ := s.settings.Float(ctx, settings.KeyTopupTGBonus)
	price := tgPrice(packStars, ratio)
	if price <= 0 {
		return nil, apperr.TopupPackInvalid
	}
	now := s.now()
	o := &Order{ID: newID(), Method: MethodTG, Status: "PENDING", Stars: packStars, BonusStars: bonusOf(packStars, pct), Price: price, CreatedAt: now, ExpiresAt: now.Add(24 * time.Hour)}
	title, desc := fmt.Sprintf("%d stars", packStars), "Stars for BrunoPlay"
	if lang == apperr.RU {
		title, desc = fmt.Sprintf("%d звёзд", packStars), "Звёзды для BrunoPlay"
	}
	link, err := s.tg.CreateInvoiceLink(ctx, telegram.InvoiceParams{Title: title, Description: desc, Payload: o.ID, Stars: price})
	if err != nil {
		return nil, fmt.Errorf("create invoice: %w", err)
	}
	o.InvoiceLink = link
	_, err = s.pool.Exec(ctx, `INSERT INTO topup_orders (id, player_id, method, status, pack_stars, bonus_stars, bonus_percent, price, invoice_link, created_at, expires_at)
		VALUES ($1,$2,'TG_STARS','PENDING',$3,$4,$5,$6,$7,$8,$9)`, o.ID, playerID, packStars, o.BonusStars, pct, price, link, now, o.ExpiresAt)
	return o, err
}

// CreateTonOrder makes an order the player pays with a TON transfer carrying the order's unique comment.
func (s *Service) CreateTonOrder(ctx context.Context, playerID, packStars int64) (*Order, error) {
	if err := s.validPack(ctx, packStars); err != nil {
		return nil, err
	}
	spt, err := s.settings.Float(ctx, settings.KeyTopupStarsPerTon)
	if err != nil {
		return nil, err
	}
	wallet, err := s.settings.Str(ctx, settings.KeyTopupTonWallet)
	if err != nil {
		return nil, err
	}
	if !ton.ValidAddress(wallet) {
		return nil, apperr.ConfigMissing.With("key", settings.KeyTopupTonWallet)
	}
	pct, _ := s.settings.Float(ctx, settings.KeyTopupTonBonus)
	mins, err := s.settings.Int(ctx, settings.KeyTopupTonExpiry)
	if err != nil {
		return nil, err
	}
	now := s.now()
	id := newID()
	comment := commentPrefix + id
	price := tonPrice(packStars, spt)
	if price <= 0 {
		return nil, apperr.TopupPackInvalid
	}
	payload, err := ton.CommentPayloadBase64(comment)
	if err != nil {
		return nil, err
	}
	o := &Order{ID: id, Method: MethodTON, Status: "PENDING", Stars: packStars, BonusStars: bonusOf(packStars, pct), Price: price,
		CreatedAt: now, ExpiresAt: now.Add(time.Duration(mins) * time.Minute), Address: wallet, Comment: comment, PayloadB64: payload, AmountNano: price}
	_, err = s.pool.Exec(ctx, `INSERT INTO topup_orders (id, player_id, method, status, pack_stars, bonus_stars, bonus_percent, price, ton_comment, ton_wallet, created_at, expires_at)
		VALUES ($1,$2,'TON','PENDING',$3,$4,$5,$6,$7,$8,$9,$10)`, id, playerID, packStars, o.BonusStars, pct, price, comment, wallet, now, o.ExpiresAt)
	return o, err
}

// Orders lists a player's orders (newest first).
func (s *Service) Orders(ctx context.Context, playerID int64, limit int) ([]Order, error) {
	if limit <= 0 || limit > 100 {
		limit = 30
	}
	rows, err := s.pool.Query(ctx, `SELECT `+orderCols+` FROM topup_orders WHERE player_id=$1 ORDER BY created_at DESC LIMIT $2`, playerID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Order
	for rows.Next() {
		o, err := scanOrder(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, o.Order)
	}
	return out, rows.Err()
}

func (s *Service) Order(ctx context.Context, playerID int64, id string) (*Order, error) {
	o, err := scanOrder(s.pool.QueryRow(ctx, `SELECT `+orderCols+` FROM topup_orders WHERE id=$1 AND player_id=$2`, id, playerID))
	if err != nil {
		return nil, err
	}
	return &o.Order, nil
}

// credit pays an order (stars and bonus as separate ledger lines) inside a transaction. The order row must be locked.
func (s *Service) credit(ctx context.Context, tx pgx.Tx, o *orderRow, paid int64, charge, txHash string) error {
	if o.Status == "PAID" {
		return nil
	}
	if _, err := ledger.Apply(ctx, tx, ledger.Entry{PlayerID: o.PlayerID, Currency: ledger.Star, Amount: o.Stars, Reason: ledger.ReasonTopup,
		RefType: "topup", RefID: o.ID, IdemKey: "topup:" + o.ID, Meta: map[string]any{"method": o.Method}}); err != nil && err != ledger.ErrDuplicate {
		return err
	}
	if o.BonusStars > 0 {
		if _, err := ledger.Apply(ctx, tx, ledger.Entry{PlayerID: o.PlayerID, Currency: ledger.Star, Amount: o.BonusStars, Reason: ledger.ReasonTopupBonus,
			RefType: "topup", RefID: o.ID, IdemKey: "topupbonus:" + o.ID}); err != nil && err != ledger.ErrDuplicate {
			return err
		}
	}
	var c, h any
	if charge != "" {
		c = charge
	}
	if txHash != "" {
		h = txHash
	}
	if _, err := tx.Exec(ctx, `UPDATE topup_orders SET status='PAID', paid_at=$2, paid_amount=$3, tg_charge_id=COALESCE($4, tg_charge_id), ton_tx_hash=COALESCE($5, ton_tx_hash) WHERE id=$1`,
		o.ID, s.now(), paid, c, h); err != nil {
		return err
	}
	for _, hook := range s.OnPaid {
		if err := hook(ctx, tx, PaidEvent{PlayerID: o.PlayerID, OrderID: o.ID, Method: o.Method, Stars: o.Stars, Bonus: o.BonusStars}); err != nil {
			return err
		}
	}
	return nil
}

// ---- Telegram Stars

// HandlePreCheckout answers Telegram's last check before the payment is taken.
func (s *Service) HandlePreCheckout(ctx context.Context, q *telegram.PreCheckoutQuery) error {
	ok, why := true, ""
	o, err := scanOrder(s.pool.QueryRow(ctx, `SELECT `+orderCols+` FROM topup_orders WHERE id=$1`, q.InvoicePayload))
	switch {
	case err != nil:
		ok, why = false, "Order not found"
	case q.Currency != "XTR" || o.Method != MethodTG || q.TotalAmount != o.Price:
		ok, why = false, "The amount does not match the order"
	case o.Status != "PENDING":
		ok, why = false, "This order is already closed"
	default:
		var tg int64
		_ = s.pool.QueryRow(ctx, `SELECT telegram_id FROM players WHERE id=$1`, o.PlayerID).Scan(&tg)
		if tg != q.From.ID {
			ok, why = false, "This order belongs to another player"
		}
	}
	return s.tg.AnswerPreCheckoutQuery(ctx, q.ID, ok, why)
}

// HandleSuccessfulPayment credits the order after Telegram confirmed the payment. Repeated updates are harmless.
func (s *Service) HandleSuccessfulPayment(ctx context.Context, userID int64, sp *telegram.SuccessfulPayment) error {
	if sp.Currency != "XTR" {
		return nil
	}
	err := db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		o, err := scanOrder(tx.QueryRow(ctx, `SELECT `+orderCols+` FROM topup_orders WHERE id=$1 FOR UPDATE`, sp.InvoicePayload))
		if err != nil {
			s.alert(ctx, "Telegram Stars payment %s of user %d (%d XTR) has no order (payload %q): check and credit by hand",
				sp.TelegramPaymentChargeID, userID, sp.TotalAmount, sp.InvoicePayload)
			return nil
		}
		if o.Status == "PAID" || o.Status == "REFUNDED" {
			return nil
		}
		if sp.TotalAmount < o.Price {
			s.alert(ctx, "Telegram Stars payment %s for order %s is %d XTR, expected %d: not credited", sp.TelegramPaymentChargeID, o.ID, sp.TotalAmount, o.Price)
			_, err := tx.Exec(ctx, `UPDATE topup_orders SET note=$2, tg_charge_id=$3 WHERE id=$1`, o.ID, "underpaid", sp.TelegramPaymentChargeID)
			return err
		}
		return s.credit(ctx, tx, o, sp.TotalAmount, sp.TelegramPaymentChargeID, "")
	})
	return err
}

// HandleRefund reverses a refunded Telegram Stars payment: the stars are taken back (even below zero), the account is held for review.
func (s *Service) HandleRefund(ctx context.Context, userID int64, rp *telegram.RefundedPayment) error {
	return db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		o, err := scanOrder(tx.QueryRow(ctx, `SELECT `+orderCols+` FROM topup_orders WHERE tg_charge_id=$1 FOR UPDATE`, rp.TelegramPaymentChargeID))
		if err != nil {
			s.alert(ctx, "Refund of unknown Telegram payment %s (user %d)", rp.TelegramPaymentChargeID, userID)
			return nil
		}
		if o.Status != "PAID" {
			return nil
		}
		total := o.Stars + o.BonusStars
		if _, err := ledger.Apply(ctx, tx, ledger.Entry{PlayerID: o.PlayerID, Currency: ledger.Star, Amount: -total, Reason: ledger.ReasonTopupRefund,
			RefType: "topup", RefID: o.ID, IdemKey: "topuprefund:" + o.ID, AllowNegative: true}); err != nil && err != ledger.ErrDuplicate {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE topup_orders SET status='REFUNDED' WHERE id=$1`, o.ID); err != nil {
			return err
		}
		// the account is held for review: withdrawals wait until an admin looks (the stars may already be spent)
		if _, err := tx.Exec(ctx, `UPDATE players SET hold_withdrawals=TRUE, flags = flags || '["topup_refund"]'::jsonb WHERE id=$1`, o.PlayerID); err != nil {
			return err
		}
		for _, h := range s.OnRefunded {
			if err := h(ctx, tx, o.PlayerID, o.ID, total); err != nil {
				return err
			}
		}
		s.alert(ctx, "Top-up %s refunded: %d stars taken back from player %d, account held for review", o.ID, total, o.PlayerID)
		return nil
	})
}

// RefundByAdmin asks Telegram to refund a paid Stars order; the stars are taken back when the refund update arrives.
func (s *Service) RefundByAdmin(ctx context.Context, orderID string) error {
	var charge *string
	var tg int64
	var status string
	err := s.pool.QueryRow(ctx, `SELECT o.tg_charge_id, o.status, p.telegram_id FROM topup_orders o JOIN players p ON p.id=o.player_id WHERE o.id=$1`, orderID).Scan(&charge, &status, &tg)
	if db.IsNoRows(err) {
		return apperr.NotFound
	}
	if err != nil {
		return err
	}
	if status != "PAID" || charge == nil {
		return apperr.BadRequest
	}
	return s.tg.RefundStarPayment(ctx, tg, *charge)
}

// ---- TON

// PollTon looks at the latest incoming transfers of the project wallet and credits matching orders. It is safe to run often:
// every transaction hash and every order is credited at most once.
func (s *Service) PollTon(ctx context.Context) (int, error) {
	if s.chain == nil {
		return 0, nil
	}
	wallet, err := s.settings.Str(ctx, settings.KeyTopupTonWallet)
	if err != nil {
		return 0, nil // not configured yet
	}
	minAge, _ := s.settings.Int(ctx, settings.KeyTopupTonConf)
	txs, err := s.chain.Transactions(ctx, wallet, 100)
	if err != nil {
		return 0, err
	}
	now := s.now()
	credited := 0
	for _, t := range txs {
		if t.Value <= 0 || !strings.HasPrefix(t.Comment, commentPrefix) {
			continue
		}
		if now.Sub(t.Time) < time.Duration(minAge)*time.Second {
			continue
		}
		ok, err := s.matchTon(ctx, t, now)
		if err != nil {
			slog.Error("ton match failed", "tx", t.Hash, "err", err)
			continue
		}
		if ok {
			credited++
		}
	}
	return credited, nil
}

func (s *Service) matchTon(ctx context.Context, t ton.Tx, now time.Time) (bool, error) {
	done := false
	err := db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		done = false
		o, err := scanOrder(tx.QueryRow(ctx, `SELECT `+orderCols+` FROM topup_orders WHERE ton_comment=$1 FOR UPDATE`, t.Comment))
		if err != nil {
			return nil // somebody else's comment or a stale one
		}
		if o.Status == "PAID" || o.Status == "REFUNDED" || o.Status == "FAILED" {
			return nil
		}
		if t.Time.Before(o.CreatedAt.Add(-time.Minute)) {
			return nil // a transfer from before the order existed cannot pay it
		}
		if now.After(o.ExpiresAt.Add(lateGrace)) {
			return nil
		}
		if t.Value < o.Price {
			s.alert(ctx, "TON order %s: %s nanoTON arrived, %d expected (tx %s). Not credited, resolve by hand.", o.ID, fmt.Sprint(t.Value), o.Price, t.Hash)
			_, err := tx.Exec(ctx, `UPDATE topup_orders SET note=$2 WHERE id=$1`, o.ID, fmt.Sprintf("underpaid: %d of %d nano in tx %s", t.Value, o.Price, t.Hash))
			return err
		}
		if err := s.credit(ctx, tx, o, t.Value, "", t.Hash); err != nil {
			return err
		}
		done = true
		return nil
	})
	if db.IsUnique(err, "topup_txhash_uidx") {
		return false, nil // the same transaction was matched in parallel
	}
	return done, err
}

// ExpireOrders closes orders that stayed unpaid past their time (a late transfer is still honoured for a week).
func (s *Service) ExpireOrders(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, `UPDATE topup_orders SET status='EXPIRED' WHERE status='PENDING' AND expires_at < $1`, s.now())
	return err
}

// AdminCredit credits an order by hand (e.g. an underpaid TON transfer the owner decided to accept).
func (s *Service) AdminCredit(ctx context.Context, orderID, reason string) error {
	return db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		o, err := scanOrder(tx.QueryRow(ctx, `SELECT `+orderCols+` FROM topup_orders WHERE id=$1 FOR UPDATE`, orderID))
		if err != nil {
			return err
		}
		if o.Status == "PAID" || o.Status == "REFUNDED" {
			return apperr.BadRequest
		}
		if err := s.credit(ctx, tx, o, o.Price, "", ""); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE topup_orders SET note=$2 WHERE id=$1`, orderID, "credited by admin: "+reason)
		return err
	})
}

// AdminOrders lists orders for the admin panel.
func (s *Service) AdminOrders(ctx context.Context, status string, beforeCreated time.Time, limit int) ([]map[string]any, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if beforeCreated.IsZero() {
		beforeCreated = s.now().Add(24 * time.Hour)
	}
	rows, err := s.pool.Query(ctx, `SELECT o.id, o.player_id, p.username, o.method, o.status, o.pack_stars, o.bonus_stars, o.price, o.paid_amount, o.note, o.created_at, o.paid_at
		FROM topup_orders o JOIN players p ON p.id=o.player_id WHERE ($1='' OR o.status=$1) AND o.created_at < $2 ORDER BY o.created_at DESC LIMIT $3`, status, beforeCreated, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var id, username, method, st, note string
		var pid, stars, bonus, price, paid int64
		var created time.Time
		var paidAt *time.Time
		if err := rows.Scan(&id, &pid, &username, &method, &st, &stars, &bonus, &price, &paid, &note, &created, &paidAt); err != nil {
			return nil, err
		}
		out = append(out, map[string]any{"id": id, "playerId": pid, "username": username, "method": method, "status": st, "stars": stars,
			"bonusStars": bonus, "price": price, "paidAmount": paid, "note": note, "createdAt": created, "paidAt": paidAt})
	}
	return out, rows.Err()
}

// SetAPIs swaps the Telegram and chain clients (tests).
func (s *Service) SetAPIs(tg telegram.API, chain ton.Chain) { s.tg, s.chain = tg, chain }
