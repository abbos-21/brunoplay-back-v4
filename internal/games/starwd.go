package games

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"brunoplay/internal/apperr"
	"brunoplay/internal/db"
	"brunoplay/internal/gameday"
	"brunoplay/internal/ledger"
	"brunoplay/internal/players"
	"brunoplay/internal/settings"
)

const (
	StarWdPending   = "PENDING"
	StarWdDelivered = "DELIVERED"
	StarWdCancelled = "CANCELLED"
)

type starWdRules struct {
	Enabled    bool
	Min        int64
	MinLevel   int64
	MinAgeDays int64
	Daily      int64
	Total      int64
}

func (s *Service) starWdRules(ctx context.Context) (*starWdRules, error) {
	on, err := s.settings.Bool(ctx, settings.KeyStarWdEnabled)
	if err != nil {
		return nil, err
	}
	r := &starWdRules{Enabled: on}
	for _, f := range []struct {
		key string
		to  *int64
	}{{settings.KeyStarWdMin, &r.Min}, {settings.KeyStarWdMinLevel, &r.MinLevel}, {settings.KeyStarWdMinAgeDays, &r.MinAgeDays},
		{settings.KeyStarWdDaily, &r.Daily}, {settings.KeyStarWdTotal, &r.Total}} {
		v, e := s.settings.Int(ctx, f.key)
		if e != nil {
			return nil, e
		}
		*f.to = v
	}
	if r.Min < 1 {
		r.Min = 1
	}
	return r, nil
}

// starWdEligibility checks the rules about the player (not about the amount).
func (s *Service) starWdEligibility(ctx context.Context, q db.Querier, p *players.Player, r *starWdRules) error {
	if !r.Enabled {
		return apperr.ConfigMissing
	}
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
	if err := q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM star_withdrawals WHERE player_id=$1 AND status='PENDING')`, p.ID).Scan(&open); err != nil {
		return err
	}
	if open {
		return apperr.WdOpen
	}
	return nil
}

// StarWdInfo is what the player's screen needs.
type StarWdInfo struct {
	MinStars      int64  `json:"minStars"`
	Balance       int64  `json:"balance"`
	Notice        string `json:"notice"`
	CanWithdraw   bool   `json:"canWithdraw"`
	BlockedReason string `json:"blockedReason,omitempty"` // an error code
	MinLevel      int64  `json:"minLevel"`
}

func (s *Service) StarWithdrawalInfo(ctx context.Context, p *players.Player) (*StarWdInfo, error) {
	r, err := s.starWdRules(ctx)
	if err != nil {
		return nil, err
	}
	in := &StarWdInfo{MinStars: r.Min, Balance: p.Stars, MinLevel: r.MinLevel, CanWithdraw: true}
	key := settings.KeyStarWdNoticeEN
	if p.Lang() == apperr.RU {
		key = settings.KeyStarWdNoticeRU
	}
	in.Notice = s.settings.StrOr(ctx, key, "")
	if err := s.starWdEligibility(ctx, s.pool, p, r); err != nil {
		in.CanWithdraw = false
		if ae := apperr.As(err); ae != nil {
			in.BlockedReason = ae.Code
		}
	}
	return in, nil
}

// StarWithdrawal is one request.
type StarWithdrawal struct {
	ID           int64      `json:"id"`
	Stars        int64      `json:"stars"`
	Status       string     `json:"status"`
	RejectReason string     `json:"rejectReason,omitempty"`
	CreatedAt    time.Time  `json:"createdAt"`
	ProcessedAt  *time.Time `json:"processedAt"`
}

// RequestStarWithdrawal takes the stars out of the balance and opens a request for an admin. The stars come back if it is cancelled.
func (s *Service) RequestStarWithdrawal(ctx context.Context, playerID, stars int64) (*StarWithdrawal, int64, error) {
	r, err := s.starWdRules(ctx)
	if err != nil {
		return nil, 0, err
	}
	if stars < r.Min {
		return nil, 0, apperr.StarWdMin.With("min", r.Min)
	}
	var out StarWithdrawal
	var balance int64
	now := s.now()
	err = db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		p, e := s.players.Lock(ctx, tx, playerID)
		if e != nil {
			return e
		}
		if e := s.starWdEligibility(ctx, tx, p, r); e != nil {
			return e
		}
		var today, total int64
		if e := tx.QueryRow(ctx, `SELECT COALESCE(sum(stars) FILTER (WHERE created_at >= $2), 0), COALESCE(sum(stars), 0)
			FROM star_withdrawals WHERE player_id=$1 AND status <> 'CANCELLED'`, playerID, gameday.Of(now)).Scan(&today, &total); e != nil {
			return e
		}
		if today+stars > r.Daily || total+stars > r.Total {
			return apperr.WdLimit
		}
		if e := tx.QueryRow(ctx, `INSERT INTO star_withdrawals (player_id, stars, created_at) VALUES ($1,$2,$3) RETURNING id, stars, status, created_at`,
			playerID, stars, now).Scan(&out.ID, &out.Stars, &out.Status, &out.CreatedAt); e != nil {
			return e
		}
		balance, e = ledger.Apply(ctx, tx, ledger.Entry{PlayerID: playerID, Currency: ledger.Star, Amount: -stars, Reason: ledger.ReasonStarWithdrawal,
			RefType: "star_withdrawal", RefID: ledger.RefID(out.ID), IdemKey: fmt.Sprintf("starwd:%d", out.ID)})
		return e
	})
	if err != nil {
		return nil, 0, err
	}
	return &out, balance, nil
}

// StarWithdrawals lists the player's own requests, newest first.
func (s *Service) StarWithdrawals(ctx context.Context, playerID int64, limit int) ([]StarWithdrawal, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	rows, err := s.pool.Query(ctx, `SELECT id, stars, status, reject_reason, created_at, processed_at FROM star_withdrawals WHERE player_id=$1 ORDER BY id DESC LIMIT $2`, playerID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []StarWithdrawal{}
	for rows.Next() {
		var w StarWithdrawal
		if err := rows.Scan(&w.ID, &w.Stars, &w.Status, &w.RejectReason, &w.CreatedAt, &w.ProcessedAt); err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// ---- admin

type StarWdRow struct {
	StarWithdrawal
	PlayerID   int64  `json:"playerId"`
	Username   string `json:"username"`
	TelegramID int64  `json:"telegramId"`
}

// AdminStarWithdrawals lists requests of one status (PENDING by default): the open queue oldest first, the others newest first.
func (s *Service) AdminStarWithdrawals(ctx context.Context, status string, beforeID int64, limit int) ([]StarWdRow, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if status == "" {
		status = StarWdPending
	}
	order, cond := "DESC", "($2=0 OR w.id<$2)"
	if status == StarWdPending {
		order, cond = "ASC", "($2=0 OR w.id>$2)"
	}
	rows, err := s.pool.Query(ctx, `SELECT w.id, w.stars, w.status, w.reject_reason, w.created_at, w.processed_at, p.id, p.username, p.telegram_id
		FROM star_withdrawals w JOIN players p ON p.id=w.player_id WHERE w.status=$1 AND `+cond+` ORDER BY w.id `+order+` LIMIT $3`, status, beforeID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []StarWdRow{}
	for rows.Next() {
		var w StarWdRow
		if err := rows.Scan(&w.ID, &w.Stars, &w.Status, &w.RejectReason, &w.CreatedAt, &w.ProcessedAt, &w.PlayerID, &w.Username, &w.TelegramID); err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// DeliverStarWithdrawal marks a request as delivered (the admin sent the Stars). Returns the player and the amount.
func (s *Service) DeliverStarWithdrawal(ctx context.Context, id, adminID int64) (playerID, stars int64, err error) {
	err = s.pool.QueryRow(ctx, `UPDATE star_withdrawals SET status='DELIVERED', processed_at=$2, admin_id=$3 WHERE id=$1 AND status='PENDING' RETURNING player_id, stars`,
		id, s.now(), adminID).Scan(&playerID, &stars)
	if db.IsNoRows(err) {
		return 0, 0, apperr.NotFound
	}
	return
}

// CancelStarWithdrawal cancels an open request and returns the stars to the player, with the reason.
func (s *Service) CancelStarWithdrawal(ctx context.Context, id, adminID int64, reason string) (playerID, stars int64, err error) {
	err = db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		e := tx.QueryRow(ctx, `UPDATE star_withdrawals SET status='CANCELLED', reject_reason=$2, processed_at=$3, admin_id=$4 WHERE id=$1 AND status='PENDING' RETURNING player_id, stars`,
			id, reason, s.now(), adminID).Scan(&playerID, &stars)
		if db.IsNoRows(e) {
			return apperr.NotFound
		}
		if e != nil {
			return e
		}
		if _, e = s.players.Lock(ctx, tx, playerID); e != nil {
			return e
		}
		_, e = ledger.Apply(ctx, tx, ledger.Entry{PlayerID: playerID, Currency: ledger.Star, Amount: stars, Reason: ledger.ReasonStarWithdrawalRefund,
			RefType: "star_withdrawal", RefID: ledger.RefID(id), IdemKey: fmt.Sprintf("starwdrefund:%d", id)})
		return e
	})
	return
}
