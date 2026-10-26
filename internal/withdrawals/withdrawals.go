// Package withdrawals turns coins into TON. Coins are taken when the request is made and returned if it fails or is cancelled.
// Automatic payouts never send twice: the payout id is stored before the transfer, and an unclear result stops for a human.
package withdrawals

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"brunoplay/internal/apperr"
	"brunoplay/internal/db"
	"brunoplay/internal/gameday"
	"brunoplay/internal/ledger"
	"brunoplay/internal/players"
	"brunoplay/internal/settings"
	"brunoplay/internal/ton"
)

const (
	Pending    = "PENDING"
	Processing = "PROCESSING"
	Completed  = "COMPLETED"
	Failed     = "FAILED"
	Cancelled  = "CANCELLED"
)

// Sender sends TON from the project's payout wallet.
type Sender interface {
	// Address is the payout wallet (used to find our own outgoing transfers).
	Address() string
	// Send makes one transfer carrying payoutID as its comment. ErrNotSent means it certainly did not go out.
	Send(ctx context.Context, payoutID, to string, amountNano int64) (txHash string, err error)
}

// ErrNotSent marks a failure before anything was broadcast (bad address, empty wallet...): safe to hand to a human.
var ErrNotSent = errors.New("withdrawals: transfer was not sent")

type Withdrawal struct {
	ID          int64      `json:"id"`
	PlayerID    int64      `json:"playerId,omitempty"`
	AmountCoins int64      `json:"amountCoins"`
	AmountTon   string     `json:"amountTon"`
	AmountNano  int64      `json:"amountNano"`
	FeeNano     int64      `json:"feeNano"`
	Address     string     `json:"targetAddress"`
	Status      string     `json:"status"`
	TxHash      *string    `json:"txHash"`
	Auto        bool       `json:"auto,omitempty"`
	NeedsReview bool       `json:"needsReview,omitempty"`
	FailReason  string     `json:"failReason,omitempty"`
	CreatedAt   time.Time  `json:"createdAt"`
	ProcessedAt *time.Time `json:"processedAt"`
}

const wdCols = `id, player_id, amount_coins, amount_nano, fee_nano, address, status, tx_hash, auto, needs_review, fail_reason, created_at, processed_at`

func scanWd(r pgx.Row) (*Withdrawal, error) {
	var w Withdrawal
	err := r.Scan(&w.ID, &w.PlayerID, &w.AmountCoins, &w.AmountNano, &w.FeeNano, &w.Address, &w.Status, &w.TxHash, &w.Auto, &w.NeedsReview,
		&w.FailReason, &w.CreatedAt, &w.ProcessedAt)
	if db.IsNoRows(err) {
		return nil, apperr.NotFound
	}
	w.AmountTon = ton.TonString(w.AmountNano)
	return &w, err
}

type Service struct {
	pool       *pgxpool.Pool
	settings   *settings.Service
	players    *players.Store
	chain      ton.Chain
	sender     Sender
	autoSwitch bool // AUTO_PAYOUT from the environment
	Now        func() time.Time

	// OnStatus runs in the transaction of every status change (event: created, completed, failed, cancelled).
	OnStatus []func(ctx context.Context, tx pgx.Tx, w *Withdrawal, event string) error
	Alert    func(ctx context.Context, text string)
}

func New(pool *pgxpool.Pool, st *settings.Service, ps *players.Store, chain ton.Chain, sender Sender, autoPayout bool) *Service {
	return &Service{pool: pool, settings: st, players: ps, chain: chain, sender: sender, autoSwitch: autoPayout, Now: time.Now}
}

// SetSender enables automatic payouts (still needs AUTO_PAYOUT=true).
func (s *Service) SetSender(x Sender) { s.sender = x }

func (s *Service) now() time.Time { return s.Now() }
func (s *Service) alert(ctx context.Context, format string, a ...any) {
	slog.Warn("withdrawals", "msg", fmt.Sprintf(format, a...))
	if s.Alert != nil {
		s.Alert(ctx, fmt.Sprintf(format, a...))
	}
}

// rules are the owner's settings; every one must be configured before withdrawals work.
type rules struct {
	CoinsPerTon float64
	FeeNano     int64
	Min, Max    int64
	CooldownH   int64
	MinLevel    int64
	MinAgeDays  int64
	DailyLimit  int64
	TotalLimit  int64
}

func (s *Service) rules(ctx context.Context) (*rules, error) {
	var r rules
	var err error
	if r.CoinsPerTon, err = s.settings.Float(ctx, settings.KeyWdCoinsPerTon); err != nil {
		return nil, err
	}
	fee, err := s.settings.Float(ctx, settings.KeyWdFeeTon)
	if err != nil {
		return nil, err
	}
	r.FeeNano = ton.NanoOfTon(fee)
	for _, x := range []struct {
		k string
		p *int64
	}{{settings.KeyWdMin, &r.Min}, {settings.KeyWdMax, &r.Max}, {settings.KeyWdCooldownHours, &r.CooldownH}, {settings.KeyWdMinLevel, &r.MinLevel},
		{settings.KeyWdMinAgeDays, &r.MinAgeDays}, {settings.KeyWdDailyLimit, &r.DailyLimit}, {settings.KeyWdTotalLimit, &r.TotalLimit}} {
		if *x.p, err = s.settings.Int(ctx, x.k); err != nil {
			return nil, err
		}
	}
	return &r, nil
}

// Info is the withdraw screen's data.
type Info struct {
	CoinsPerTon      float64    `json:"coinsPerTon"`
	CoinToTonRate    float64    `json:"coinToTonRate"` // TON per coin
	MinimumCoins     int64      `json:"minimumCoinWithdrawal"`
	MaximumCoins     int64      `json:"maximumCoinWithdrawal"`
	FeeTon           string     `json:"withdrawalFeeTon"`
	Balance          int64      `json:"balance"`
	Notice           string     `json:"notice"`
	CanWithdraw      bool       `json:"canWithdraw"`
	BlockedReason    string     `json:"blockedReason,omitempty"` // an error code
	NextWithdrawalAt *time.Time `json:"nextWithdrawalAt,omitempty"`
	MinLevel         int64      `json:"minLevel"`
}

func (s *Service) Info(ctx context.Context, p *players.Player) (*Info, error) {
	r, err := s.rules(ctx)
	if err != nil {
		return nil, err
	}
	in := &Info{CoinsPerTon: r.CoinsPerTon, CoinToTonRate: 1 / r.CoinsPerTon, MinimumCoins: r.Min, MaximumCoins: r.Max, FeeTon: ton.TonString(r.FeeNano),
		Balance: p.Coins, MinLevel: r.MinLevel, CanWithdraw: true}
	key := settings.KeyWdNoticeEN
	if p.Lang() == apperr.RU {
		key = settings.KeyWdNoticeRU
	}
	in.Notice = s.settings.StrOr(ctx, key, "")
	if err := s.eligibility(ctx, s.pool, p, r); err != nil {
		in.CanWithdraw = false
		if ae := apperr.As(err); ae != nil {
			in.BlockedReason = ae.Code
		}
		if r.CooldownH > 0 {
			var last time.Time
			if e := s.pool.QueryRow(ctx, `SELECT created_at FROM withdrawals WHERE player_id=$1 AND status NOT IN ('CANCELLED','FAILED') ORDER BY id DESC LIMIT 1`, p.ID).Scan(&last); e == nil {
				t := last.Add(time.Duration(r.CooldownH) * time.Hour)
				if t.After(s.now()) {
					in.NextWithdrawalAt = &t
				}
			}
		}
	}
	return in, nil
}

// eligibility checks the rules about the player (not about the amount).
func (s *Service) eligibility(ctx context.Context, q db.Querier, p *players.Player, r *rules) error {
	if p.IsBlocked {
		return apperr.AccountBlocked
	}
	if p.HoldWithdrawals {
		return apperr.UnderReview
	}
	if int64(p.Level) < r.MinLevel {
		return apperr.WdLevel.With("level", r.MinLevel)
	}
	if s.now().Sub(p.CreatedAt) < time.Duration(r.MinAgeDays)*24*time.Hour {
		return apperr.WdAccountAge
	}
	var open bool
	if err := q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM withdrawals WHERE player_id=$1 AND status IN ('PENDING','PROCESSING'))`, p.ID).Scan(&open); err != nil {
		return err
	}
	if open {
		return apperr.WdOpen
	}
	if r.CooldownH > 0 {
		var last time.Time
		err := q.QueryRow(ctx, `SELECT created_at FROM withdrawals WHERE player_id=$1 AND status NOT IN ('CANCELLED','FAILED') ORDER BY id DESC LIMIT 1`, p.ID).Scan(&last)
		if err == nil && s.now().Sub(last) < time.Duration(r.CooldownH)*time.Hour {
			return apperr.WdCooldown
		}
		if err != nil && !db.IsNoRows(err) {
			return err
		}
	}
	return nil
}

// Create requests a withdrawal: rules, coins out, request in, all in one transaction.
func (s *Service) Create(ctx context.Context, playerID, coins int64, address string) (*Withdrawal, error) {
	r, err := s.rules(ctx)
	if err != nil {
		return nil, err
	}
	if !ton.ValidAddress(address) {
		return nil, apperr.TonAddrInvalid
	}
	if coins < r.Min {
		return nil, apperr.WdMin.With("min", r.Min)
	}
	if coins > r.Max {
		return nil, apperr.WdMax.With("max", r.Max)
	}
	receive := ton.NanoFor(coins, r.CoinsPerTon) - r.FeeNano
	if receive <= 0 {
		return nil, apperr.WdTooSmall
	}
	autoMax, _ := s.settings.Float(ctx, settings.KeyWdAutoMaxTon)
	autoCap, _ := s.settings.Float(ctx, settings.KeyWdAutoDailyCap)
	now := s.now()
	var out *Withdrawal
	err = db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		p, err := s.players.Lock(ctx, tx, playerID)
		if err != nil {
			return err
		}
		if err := s.eligibility(ctx, tx, p, r); err != nil {
			return err
		}
		var today, total int64
		if err := tx.QueryRow(ctx, `SELECT COALESCE(sum(amount_coins) FILTER (WHERE created_at >= $2), 0), COALESCE(sum(amount_coins), 0)
			FROM withdrawals WHERE player_id=$1 AND status NOT IN ('CANCELLED','FAILED')`, playerID, gameday.Of(now)).Scan(&today, &total); err != nil {
			return err
		}
		if today+coins > r.DailyLimit || total+coins > r.TotalLimit {
			return apperr.WdLimit
		}
		auto := false
		// the API process has no sender (only the worker opens the wallet), so this looks at the switch, not at the sender
		if s.autoSwitch && autoMax > 0 && receive <= ton.NanoOfTon(autoMax) {
			var usedToday int64
			if err := tx.QueryRow(ctx, `SELECT COALESCE(sum(amount_nano),0) FROM withdrawals WHERE auto AND status IN ('PENDING','PROCESSING','COMPLETED') AND created_at >= $1`,
				gameday.Of(now)).Scan(&usedToday); err != nil {
				return err
			}
			auto = usedToday+receive <= ton.NanoOfTon(autoCap)
		}
		var id int64
		if err := tx.QueryRow(ctx, `INSERT INTO withdrawals (player_id, amount_coins, coins_per_ton, fee_nano, amount_nano, address, status, auto, created_at, updated_at)
			VALUES ($1,$2,$3,$4,$5,$6,'PENDING',$7,$8,$8) RETURNING id`, playerID, coins, r.CoinsPerTon, r.FeeNano, receive, address, auto, now).Scan(&id); err != nil {
			if db.IsUnique(err, "withdrawals_one_open") {
				return apperr.WdOpen
			}
			return err
		}
		if _, err := ledger.Apply(ctx, tx, ledger.Entry{PlayerID: playerID, Currency: ledger.Coin, Amount: -coins, Reason: ledger.ReasonWithdrawal,
			RefType: "withdrawal", RefID: ledger.RefID(id), IdemKey: fmt.Sprintf("wd:%d", id)}); err != nil {
			return err
		}
		out, err = scanWd(tx.QueryRow(ctx, `SELECT `+wdCols+` FROM withdrawals WHERE id=$1`, id))
		if err != nil {
			return err
		}
		return s.fire(ctx, tx, out, "created")
	})
	return out, err
}

func (s *Service) fire(ctx context.Context, tx pgx.Tx, w *Withdrawal, event string) error {
	for _, h := range s.OnStatus {
		if err := h(ctx, tx, w, event); err != nil {
			return err
		}
	}
	return nil
}

// Cancel lets the player take back a request that nobody started to pay yet.
func (s *Service) Cancel(ctx context.Context, playerID, id int64) (*Withdrawal, error) {
	var out *Withdrawal
	err := db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := s.players.Lock(ctx, tx, playerID); err != nil {
			return err
		}
		w, err := scanWd(tx.QueryRow(ctx, `SELECT `+wdCols+` FROM withdrawals WHERE id=$1 AND player_id=$2 FOR UPDATE`, id, playerID))
		if err != nil {
			return err
		}
		if w.Status != Pending {
			return apperr.WdNotCancel
		}
		out, err = s.closeTx(ctx, tx, w, Cancelled, "cancelled by the player", nil)
		return err
	})
	return out, err
}

// closeTx moves a request to FAILED or CANCELLED and returns the coins.
func (s *Service) closeTx(ctx context.Context, tx pgx.Tx, w *Withdrawal, status, reason string, adminID *int64) (*Withdrawal, error) {
	if _, err := ledger.Apply(ctx, tx, ledger.Entry{PlayerID: w.PlayerID, Currency: ledger.Coin, Amount: w.AmountCoins, Reason: ledger.ReasonWithdrawalRefund,
		RefType: "withdrawal", RefID: ledger.RefID(w.ID), IdemKey: fmt.Sprintf("wdrefund:%d", w.ID)}); err != nil && err != ledger.ErrDuplicate {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `UPDATE withdrawals SET status=$2, fail_reason=$3, admin_id=COALESCE($4, admin_id), updated_at=$5, processed_at=$5 WHERE id=$1`,
		w.ID, status, reason, adminID, s.now()); err != nil {
		return nil, err
	}
	w2, err := scanWd(tx.QueryRow(ctx, `SELECT `+wdCols+` FROM withdrawals WHERE id=$1`, w.ID))
	if err != nil {
		return nil, err
	}
	ev := "failed"
	if status == Cancelled {
		ev = "cancelled"
	}
	return w2, s.fire(ctx, tx, w2, ev)
}

// List returns a player's withdrawals, newest first.
func (s *Service) List(ctx context.Context, playerID int64, page, perPage int) ([]Withdrawal, int64, error) {
	if perPage <= 0 || perPage > 50 {
		perPage = 20
	}
	if page < 1 {
		page = 1
	}
	var total int64
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM withdrawals WHERE player_id=$1`, playerID).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.pool.Query(ctx, `SELECT `+wdCols+` FROM withdrawals WHERE player_id=$1 ORDER BY id DESC LIMIT $2 OFFSET $3`, playerID, perPage, (page-1)*perPage)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []Withdrawal{}
	for rows.Next() {
		w, err := scanWd(rows)
		if err != nil {
			return nil, 0, err
		}
		w.PlayerID = 0
		out = append(out, *w)
	}
	return out, total, rows.Err()
}

// ---- admin

func (s *Service) AdminList(ctx context.Context, status string, beforeID int64, limit int) ([]Withdrawal, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.pool.Query(ctx, `SELECT `+wdCols+` FROM withdrawals WHERE ($1='' OR status=$1) AND ($2=0 OR id<$2) ORDER BY id DESC LIMIT $3`, status, beforeID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Withdrawal{}
	for rows.Next() {
		w, err := scanWd(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *w)
	}
	return out, rows.Err()
}

func (s *Service) Get(ctx context.Context, id int64) (*Withdrawal, error) {
	return scanWd(s.pool.QueryRow(ctx, `SELECT `+wdCols+` FROM withdrawals WHERE id=$1`, id))
}

// MarkProcessing is the admin saying "I am paying this one now".
func (s *Service) MarkProcessing(ctx context.Context, id, adminID int64) (*Withdrawal, error) {
	var out *Withdrawal
	err := db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		w, err := scanWd(tx.QueryRow(ctx, `SELECT `+wdCols+` FROM withdrawals WHERE id=$1 FOR UPDATE`, id))
		if err != nil {
			return err
		}
		if w.Status != Pending || w.Auto {
			return apperr.WdNotCancel
		}
		if _, err := tx.Exec(ctx, `UPDATE withdrawals SET status='PROCESSING', admin_id=$2, updated_at=$3 WHERE id=$1`, id, adminID, s.now()); err != nil {
			return err
		}
		out, err = scanWd(tx.QueryRow(ctx, `SELECT `+wdCols+` FROM withdrawals WHERE id=$1`, id))
		return err
	})
	return out, err
}

// Complete records that the TON was sent by hand.
func (s *Service) Complete(ctx context.Context, id, adminID int64, txHash string) (*Withdrawal, error) {
	var out *Withdrawal
	err := db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		w, err := scanWd(tx.QueryRow(ctx, `SELECT `+wdCols+` FROM withdrawals WHERE id=$1 FOR UPDATE`, id))
		if err != nil {
			return err
		}
		if w.Status != Pending && w.Status != Processing {
			return apperr.BadRequest
		}
		if _, err := tx.Exec(ctx, `UPDATE withdrawals SET status='COMPLETED', tx_hash=$2, admin_id=$3, needs_review=FALSE, updated_at=$4, processed_at=$4 WHERE id=$1`,
			id, txHash, adminID, s.now()); err != nil {
			return err
		}
		out, err = scanWd(tx.QueryRow(ctx, `SELECT `+wdCols+` FROM withdrawals WHERE id=$1`, id))
		if err != nil {
			return err
		}
		return s.fire(ctx, tx, out, "completed")
	})
	return out, err
}

// Reject fails a request and returns the coins. For an automatic payout that ended unclear, force must be set
// (the admin confirms in the explorer that nothing was sent).
func (s *Service) Reject(ctx context.Context, id, adminID int64, reason string, force bool) (*Withdrawal, error) {
	var out *Withdrawal
	err := db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		w, err := scanWd(tx.QueryRow(ctx, `SELECT `+wdCols+` FROM withdrawals WHERE id=$1 FOR UPDATE`, id))
		if err != nil {
			return err
		}
		if w.Status != Pending && w.Status != Processing {
			return apperr.BadRequest
		}
		if w.NeedsReview && !force {
			return apperr.BadRequest
		}
		out, err = s.closeTx(ctx, tx, w, Failed, reason, &adminID)
		return err
	})
	return out, err
}

// ---- automatic payouts

// RunPayouts sends the pending automatic withdrawals. Each one is claimed (status PROCESSING and its payout id stored) in its own
// transaction BEFORE anything is sent, so a crash or a second worker can never send it twice.
func (s *Service) RunPayouts(ctx context.Context, max int) (int, error) {
	if !s.autoSwitch || s.sender == nil {
		return 0, nil
	}
	done := 0
	for i := 0; i < max; i++ {
		w, payoutID, err := s.claim(ctx)
		if err != nil {
			return done, err
		}
		if w == nil {
			break
		}
		hash, err := s.sender.Send(ctx, payoutID, w.Address, w.AmountNano)
		switch {
		case err == nil:
			if e := s.finishAuto(ctx, w, hash); e != nil {
				s.alert(ctx, "Withdrawal %d was SENT (tx %s) but could not be recorded: %v. Record it by hand.", w.ID, hash, e)
			}
			done++
		case errors.Is(err, ErrNotSent):
			s.handBack(ctx, w, err.Error())
		default:
			// unclear: the transfer may or may not have gone out. Never resend; a human (or the reconcile job) decides.
			_, _ = s.pool.Exec(ctx, `UPDATE withdrawals SET needs_review=TRUE, fail_reason=$2, updated_at=$3 WHERE id=$1`, w.ID, "payout result unclear: "+err.Error(), s.now())
			s.alert(ctx, "Withdrawal %d: payout result is UNCLEAR (%v). It will NOT be resent. Check the wallet and resolve it in the admin panel.", w.ID, err)
		}
	}
	return done, nil
}

func (s *Service) claim(ctx context.Context) (*Withdrawal, string, error) {
	var w *Withdrawal
	var payoutID string
	err := db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		w = nil
		var id int64
		err := tx.QueryRow(ctx, `SELECT id FROM withdrawals WHERE status='PENDING' AND auto AND NOT needs_review ORDER BY id FOR UPDATE SKIP LOCKED LIMIT 1`).Scan(&id)
		if db.IsNoRows(err) {
			return nil
		}
		if err != nil {
			return err
		}
		x, err := scanWd(tx.QueryRow(ctx, `SELECT `+wdCols+` FROM withdrawals WHERE id=$1`, id))
		if err != nil {
			return err
		}
		// a blocked or held player never gets an automatic payout: it goes to manual review
		var blocked, held bool
		if err := tx.QueryRow(ctx, `SELECT is_blocked, hold_withdrawals FROM players WHERE id=$1`, x.PlayerID).Scan(&blocked, &held); err != nil {
			return err
		}
		if blocked || held {
			_, err := tx.Exec(ctx, `UPDATE withdrawals SET auto=FALSE, fail_reason='held for review', updated_at=$2 WHERE id=$1`, id, s.now())
			return err
		}
		payoutID = fmt.Sprintf("bpw%d", id)
		if _, err := tx.Exec(ctx, `UPDATE withdrawals SET status='PROCESSING', payout_id=$2, payout_started_at=$3, updated_at=$3 WHERE id=$1`, id, payoutID, s.now()); err != nil {
			return err
		}
		w = x
		return nil
	})
	return w, payoutID, err
}

func (s *Service) finishAuto(ctx context.Context, w *Withdrawal, hash string) error {
	return db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE withdrawals SET status='COMPLETED', tx_hash=$2, needs_review=FALSE, updated_at=$3, processed_at=$3 WHERE id=$1`, w.ID, hash, s.now()); err != nil {
			return err
		}
		w2, err := scanWd(tx.QueryRow(ctx, `SELECT `+wdCols+` FROM withdrawals WHERE id=$1`, w.ID))
		if err != nil {
			return err
		}
		return s.fire(ctx, tx, w2, "completed")
	})
}

// handBack returns a request that certainly was not sent to the manual queue.
func (s *Service) handBack(ctx context.Context, w *Withdrawal, why string) {
	_, err := s.pool.Exec(ctx, `UPDATE withdrawals SET status='PENDING', auto=FALSE, fail_reason=$2, payout_id=NULL, updated_at=$3 WHERE id=$1 AND status='PROCESSING'`, w.ID, why, s.now())
	if err != nil {
		slog.Error("hand back failed", "id", w.ID, "err", err)
	}
	s.alert(ctx, "Withdrawal %d could not be sent automatically (%s). It is waiting for a manual payout.", w.ID, why)
}

// Reconcile looks for payouts that were sent but not recorded (a crash between send and save) by finding their comment in our
// outgoing transfers; one that cannot be found after 30 minutes is flagged for a human, never resent.
func (s *Service) Reconcile(ctx context.Context) (int, error) {
	if s.sender == nil || s.chain == nil {
		return 0, nil
	}
	rows, err := s.pool.Query(ctx, `SELECT id, payout_id, payout_started_at FROM withdrawals WHERE status='PROCESSING' AND auto AND payout_id IS NOT NULL AND payout_started_at < $1`, s.now().Add(-2*time.Minute))
	if err != nil {
		return 0, err
	}
	type cand struct {
		id      int64
		payout  string
		started time.Time
	}
	var cands []cand
	for rows.Next() {
		var c cand
		if err := rows.Scan(&c.id, &c.payout, &c.started); err != nil {
			rows.Close()
			return 0, err
		}
		cands = append(cands, c)
	}
	rows.Close()
	if len(cands) == 0 {
		return 0, nil
	}
	txs, err := s.chain.Transactions(ctx, s.sender.Address(), 100)
	if err != nil {
		return 0, err
	}
	fixed := 0
	for _, c := range cands {
		var hash string
		for _, t := range txs {
			for _, o := range t.Outgoing {
				if o.Comment == c.payout {
					hash = t.Hash
				}
			}
		}
		if hash != "" {
			w := &Withdrawal{ID: c.id}
			if err := s.finishAuto(ctx, w, hash); err != nil {
				return fixed, err
			}
			fixed++
			continue
		}
		if s.now().Sub(c.started) > 30*time.Minute {
			ct, err := s.pool.Exec(ctx, `UPDATE withdrawals SET needs_review=TRUE, fail_reason='payout not found on chain after 30 minutes', updated_at=$2 WHERE id=$1 AND NOT needs_review`, c.id, s.now())
			if err == nil && ct.RowsAffected() > 0 {
				s.alert(ctx, "Withdrawal %d: no matching transfer on the chain after 30 minutes. Not resent. Check and resolve in the admin panel.", c.id)
			}
		}
	}
	return fixed, nil
}
