// Package risk finds suspicious accounts (many accounts on one address, invite bursts, withdrawals right after sign-up, ad floods),
// scores them, and puts an automatic temporary hold on withdrawals and rewards when the score passes the admin's threshold.
// It never bans anybody: only a human blocks an account.
package risk

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"brunoplay/internal/apperr"
	"brunoplay/internal/db"
	"brunoplay/internal/settings"
)

type Service struct {
	pool     *pgxpool.Pool
	settings *settings.Service
	Now      func() time.Time
	Alert    func(ctx context.Context, text string)
}

func New(pool *pgxpool.Pool, st *settings.Service) *Service {
	return &Service{pool: pool, settings: st, Now: time.Now}
}

func (s *Service) now() time.Time { return s.Now() }

type signal struct {
	flag   string
	weight int
	sql    string // returns the player ids that show the signal; $1 = now
}

// the signals, with the points each adds once per player
var signals = []signal{
	{"shared_ip", 25, `SELECT id FROM players WHERE deleted_at IS NULL AND last_ip <> '' AND last_ip IN
		(SELECT last_ip FROM players WHERE deleted_at IS NULL AND last_ip <> '' GROUP BY last_ip HAVING count(*) >= 5)`},
	{"referral_burst", 30, `SELECT referred_by FROM players WHERE referred_by IS NOT NULL AND created_at > $1::timestamptz - interval '24 hours'
		GROUP BY referred_by HAVING count(*) >= 10`},
	{"young_withdrawal", 40, `SELECT w.player_id FROM withdrawals w JOIN players p ON p.id = w.player_id WHERE w.created_at - p.created_at < interval '24 hours'`},
	{"ad_flood", 25, `SELECT player_id FROM ad_views WHERE status IN ('COMPLETED','USED') AND created_at > $1::timestamptz - interval '24 hours'
		GROUP BY player_id HAVING count(*) >= 300`},
}

// Scan adds every newly seen signal to the players that show it, then applies the automatic hold.
// Players cleared by an admin in the last 7 days are left alone. Returns (newly flagged, newly held).
func (s *Service) Scan(ctx context.Context) (flagged, held int, err error) {
	now := s.now()
	for _, sg := range signals {
		ct, err := s.pool.Exec(ctx, `UPDATE players p SET flags = flags || to_jsonb($2::text), risk_score = risk_score + $3
			WHERE p.id IN (`+sg.sql+`) AND NOT (p.flags ? $2) AND (p.risk_cleared_at IS NULL OR p.risk_cleared_at < $1::timestamptz - interval '7 days')`,
			now, sg.flag, sg.weight)
		if err != nil {
			return flagged, held, fmt.Errorf("signal %s: %w", sg.flag, err)
		}
		flagged += int(ct.RowsAffected())
	}
	th, err := s.settings.Int(ctx, settings.KeyRiskHold)
	if err != nil || th <= 0 {
		return flagged, 0, nil
	}
	rows, err := s.pool.Query(ctx, `UPDATE players SET hold_withdrawals=TRUE, hold_rewards=TRUE, flags = flags || '"auto_hold"'::jsonb
		WHERE risk_score >= $1 AND NOT (flags ? 'auto_hold') AND deleted_at IS NULL AND (risk_cleared_at IS NULL OR risk_cleared_at < $2::timestamptz - interval '7 days')
		RETURNING id, risk_score`, th, now)
	if err != nil {
		return flagged, held, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var score int
		if err := rows.Scan(&id, &score); err != nil {
			return flagged, held, err
		}
		held++
		if s.Alert != nil {
			s.Alert(ctx, fmt.Sprintf("Player %d was put on an automatic hold (risk score %d). Review in the admin panel.", id, score))
		}
	}
	return flagged, held, rows.Err()
}

type Flagged struct {
	ID              int64    `json:"id"`
	Username        string   `json:"username"`
	RiskScore       int      `json:"riskScore"`
	Flags           []string `json:"flags"`
	HoldWithdrawals bool     `json:"holdWithdrawals"`
	HoldRewards     bool     `json:"holdRewards"`
	IsBlocked       bool     `json:"isBlocked"`
	LastIP          string   `json:"lastIp"`
}

// List returns accounts with a risk score or a hold, riskiest first.
func (s *Service) List(ctx context.Context, limit int) ([]Flagged, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.pool.Query(ctx, `SELECT id, username, risk_score, flags, hold_withdrawals, hold_rewards, is_blocked, last_ip FROM players
		WHERE deleted_at IS NULL AND (risk_score > 0 OR hold_withdrawals OR hold_rewards) ORDER BY risk_score DESC, id DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Flagged{}
	for rows.Next() {
		var f Flagged
		if err := rows.Scan(&f.ID, &f.Username, &f.RiskScore, &f.Flags, &f.HoldWithdrawals, &f.HoldRewards, &f.IsBlocked, &f.LastIP); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// Clear is the admin saying "this player is fine": holds are lifted, the score reset, and the scanner leaves them alone for a week.
func (s *Service) Clear(ctx context.Context, playerID int64) error {
	ct, err := s.pool.Exec(ctx, `UPDATE players SET hold_withdrawals=FALSE, hold_rewards=FALSE, risk_score=0, flags='[]'::jsonb, risk_cleared_at=$2 WHERE id=$1`, playerID, s.now())
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return apperr.NotFound
	}
	return nil
}

// Mismatch is a player whose balance differs from the sum of their ledger.
type Mismatch struct {
	PlayerID int64  `json:"playerId"`
	Currency string `json:"currency"`
	Balance  int64  `json:"balance"`
	Ledger   int64  `json:"ledger"`
}

// AuditLedger checks that every balance equals the sum of its ledger entries (a safety net that should always be empty).
func (s *Service) AuditLedger(ctx context.Context, limit int) ([]Mismatch, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT p.id, 'COIN', p.coins, COALESCE(l.s, 0) FROM players p
		LEFT JOIN (SELECT player_id, sum(amount) s FROM ledger_entries WHERE currency='COIN' GROUP BY player_id) l ON l.player_id = p.id
		WHERE p.coins <> COALESCE(l.s, 0)
		UNION ALL
		SELECT p.id, 'STAR', p.stars, COALESCE(l.s, 0) FROM players p
		LEFT JOIN (SELECT player_id, sum(amount) s FROM ledger_entries WHERE currency='STAR' GROUP BY player_id) l ON l.player_id = p.id
		WHERE p.stars <> COALESCE(l.s, 0)
		LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Mismatch
	for rows.Next() {
		var m Mismatch
		if err := rows.Scan(&m.PlayerID, &m.Currency, &m.Balance, &m.Ledger); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// DeletePlayer erases a player's personal data on request. Money records stay, anonymised (the player id remains, the person is gone).
func (s *Service) DeletePlayer(ctx context.Context, playerID int64) error {
	return db.WithTx(ctx, s.pool, func(tx pgxTx) error {
		var tg int64
		if err := tx.QueryRow(ctx, `SELECT telegram_id FROM players WHERE id=$1 AND deleted_at IS NULL FOR UPDATE`, playerID).Scan(&tg); err != nil {
			if db.IsNoRows(err) {
				return apperr.NotFound
			}
			return err
		}
		var open int
		if err := tx.QueryRow(ctx, `SELECT (SELECT count(*) FROM withdrawals WHERE player_id=$1 AND status IN ('PENDING','PROCESSING'))
			+ (SELECT count(*) FROM player_gifts WHERE player_id=$1 AND status='CLAIM_REQUESTED')`, playerID).Scan(&open); err != nil {
			return err
		}
		if open > 0 {
			return apperr.BadRequest.With("reason", "the player has an open withdrawal or gift claim")
		}
		for _, q := range []string{
			`DELETE FROM notifications WHERE player_id=$1`,
			`DELETE FROM task_seen WHERE player_id=$1`,
			`DELETE FROM ad_failures WHERE player_id=$1`,
			`DELETE FROM idempotency_keys WHERE player_id=$1`,
			`DELETE FROM player_days WHERE player_id=$1`,
			`UPDATE winners_feed SET username='' WHERE player_id=$1`,
		} {
			if _, err := tx.Exec(ctx, q, playerID); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `DELETE FROM bot_users WHERE telegram_id=$1`, tg); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM pending_referrals WHERE telegram_id=$1`, tg); err != nil {
			return err
		}
		// the Telegram id is freed (the same person may sign up again as a brand-new player)
		_, err := tx.Exec(ctx, `UPDATE players SET username='', first_name='Deleted', last_name='', photo_url='', last_ip='', country='', admin_note='',
			flags='[]'::jsonb, telegram_id = -id, deleted_at=$2, bot_started=FALSE, bot_blocked=FALSE, notif_off='[]'::jsonb, active_hours='{}', pref_hour=NULL,
			is_blocked=TRUE, block_reason='deleted' WHERE id=$1`, playerID, s.now())
		return err
	})
}
