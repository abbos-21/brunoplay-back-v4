// Package stats builds the dashboard numbers once per run into small daily tables, so the admin dashboard never scans the big
// tables (ledger, players, ad views) while someone is looking at it.
package stats

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"brunoplay/internal/gameday"
)

type Service struct {
	pool *pgxpool.Pool
	Now  func() time.Time
}

func New(pool *pgxpool.Pool) *Service { return &Service{pool: pool, Now: time.Now} }

func (s *Service) now() time.Time { return s.Now() }

func (s *Service) put(ctx context.Context, day time.Time, metric, dim string, v any) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO daily_stats (day, metric, dim, value) VALUES ($1,$2,$3,$4)
		ON CONFLICT (day, metric, dim) DO UPDATE SET value=EXCLUDED.value`, day, metric, dim, v)
	return err
}

// rows runs a "dim, value" query for one day and stores every row under the metric (and removes dims that vanished).
func (s *Service) series(ctx context.Context, day time.Time, metric, sql string, args ...any) error {
	rows, err := s.pool.Query(ctx, sql, append([]any{day, day.AddDate(0, 0, 1)}, args...)...)
	if err != nil {
		return fmt.Errorf("%s: %w", metric, err)
	}
	type kv struct {
		dim string
		val float64
	}
	var out []kv
	for rows.Next() {
		var x kv
		if err := rows.Scan(&x.dim, &x.val); err != nil {
			rows.Close()
			return err
		}
		out = append(out, x)
	}
	rows.Close()
	if _, err := s.pool.Exec(ctx, `DELETE FROM daily_stats WHERE day=$1 AND metric=$2`, day, metric); err != nil {
		return err
	}
	for _, x := range out {
		if err := s.put(ctx, day, metric, x.dim, x.val); err != nil {
			return err
		}
	}
	return nil
}

// Day (re)computes every metric of one game day. It can run any number of times; the day being lived is simply refreshed.
func (s *Service) Day(ctx context.Context, day time.Time) error {
	day = gameday.Of(day)
	q := []struct{ metric, sql string }{
		{"new_players", `SELECT language, count(*) FROM players WHERE created_at >= $1 AND created_at < $2 GROUP BY language`},
		{"dau", `SELECT p.language, count(*) FROM player_days d JOIN players p ON p.id=d.player_id WHERE d.day >= $1 AND d.day < $2 GROUP BY p.language`},
		{"stars_bought", `SELECT method, COALESCE(sum(pack_stars + bonus_stars),0) FROM topup_orders WHERE status IN ('PAID','REFUNDED') AND paid_at >= $1 AND paid_at < $2 GROUP BY method`},
		{"topups", `SELECT method, count(*) FROM topup_orders WHERE status IN ('PAID','REFUNDED') AND paid_at >= $1 AND paid_at < $2 GROUP BY method`},
		{"withdrawn_ton_nano", `SELECT 'completed', COALESCE(sum(amount_nano),0) FROM withdrawals WHERE status='COMPLETED' AND processed_at >= $1 AND processed_at < $2`},
		{"withdrawal_requests", `SELECT status, count(*) FROM withdrawals WHERE created_at >= $1 AND created_at < $2 GROUP BY status`},
		{"withdrawal_coins", `SELECT 'requested', COALESCE(sum(amount_coins),0) FROM withdrawals WHERE created_at >= $1 AND created_at < $2`},
		{"ad_views", `SELECT placement || ':' || status, count(*) FROM ad_views WHERE created_at >= $1 AND created_at < $2 GROUP BY placement, status`},
		{"ad_fallbacks", `SELECT placement, count(*) FROM ad_views WHERE fallback AND created_at >= $1 AND created_at < $2 GROUP BY placement`},
		{"coins_in", `SELECT reason, sum(amount) FROM ledger_entries WHERE currency='COIN' AND amount > 0 AND created_at >= $1 AND created_at < $2 GROUP BY reason`},
		{"coins_out", `SELECT reason, -sum(amount) FROM ledger_entries WHERE currency='COIN' AND amount < 0 AND created_at >= $1 AND created_at < $2 GROUP BY reason`},
		{"stars_in", `SELECT reason, sum(amount) FROM ledger_entries WHERE currency='STAR' AND amount > 0 AND created_at >= $1 AND created_at < $2 GROUP BY reason`},
		{"stars_out", `SELECT reason, -sum(amount) FROM ledger_entries WHERE currency='STAR' AND amount < 0 AND created_at >= $1 AND created_at < $2 GROUP BY reason`},
		{"guard_events", `SELECT game || ':' || tier, count(*) FROM guard_events WHERE created_at >= $1 AND created_at < $2 GROUP BY game, tier`},
		{"payout_spent", `SELECT game || ':' || tier, sum(spent) FROM payout_stats WHERE hour >= $1 AND hour < $2 GROUP BY game, tier`},
		{"payout_paid", `SELECT game || ':' || tier, sum(paid) FROM payout_stats WHERE hour >= $1 AND hour < $2 GROUP BY game, tier`},
		{"notifications", `SELECT type || ':' || status, count(*) FROM notifications WHERE created_at >= $1 AND created_at < $2 GROUP BY type, status`},
	}
	for _, m := range q {
		if err := s.series(ctx, day, m.metric, m.sql); err != nil {
			return err
		}
	}
	// the number of players active in the last 30 days, as seen from this day
	var mau int64
	if err := s.pool.QueryRow(ctx, `SELECT count(DISTINCT player_id) FROM player_days WHERE day > $1 AND day <= $2`, day.AddDate(0, 0, -30), day).Scan(&mau); err != nil {
		return err
	}
	if err := s.put(ctx, day, "mau", "", mau); err != nil {
		return err
	}
	// a snapshot of what is in circulation right now: only meaningful for the day being lived
	if !day.Equal(gameday.Of(s.now())) {
		return nil
	}
	var coins, stars, gifts, claims int64
	if err := s.pool.QueryRow(ctx, `SELECT COALESCE(sum(coins),0), COALESCE(sum(stars),0) FROM players WHERE deleted_at IS NULL`).Scan(&coins, &stars); err != nil {
		return err
	}
	_ = s.pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE status='OWNED'), count(*) FILTER (WHERE status='CLAIM_REQUESTED') FROM player_gifts`).Scan(&gifts, &claims)
	for k, v := range map[string]int64{"coins": coins, "stars": stars, "gifts_owned": gifts, "gift_claims_open": claims} {
		if err := s.put(ctx, day, "circulation", k, v); err != nil {
			return err
		}
	}
	return nil
}

// Retention records, for the cohort of players created N days before `today`, how many were active again exactly N days later.
func (s *Service) Retention(ctx context.Context, today time.Time) error {
	today = gameday.Of(today)
	for _, n := range []int{1, 7, 30} {
		cohort := today.AddDate(0, 0, -n)
		var size, back int64
		if err := s.pool.QueryRow(ctx, `SELECT count(*), count(d.player_id) FROM players p
			LEFT JOIN player_days d ON d.player_id=p.id AND d.day=$2
			WHERE p.created_at >= $1 AND p.created_at < $1 + interval '1 day'`, cohort, today).Scan(&size, &back); err != nil {
			return err
		}
		if err := s.put(ctx, cohort, "cohort_size", "", size); err != nil {
			return err
		}
		if err := s.put(ctx, cohort, "returned_d"+fmt.Sprint(n), "", back); err != nil {
			return err
		}
	}
	return nil
}

// Run refreshes today and yesterday (a day is final once the next one has been computed).
func (s *Service) Run(ctx context.Context) error {
	now := s.now()
	for _, d := range []time.Time{gameday.Of(now).AddDate(0, 0, -1), gameday.Of(now)} {
		if err := s.Day(ctx, d); err != nil {
			return err
		}
	}
	return s.Retention(ctx, now)
}

type Point struct {
	Day   string  `json:"day"`
	Dim   string  `json:"dim"`
	Value float64 `json:"value"`
}

// Dashboard returns every metric of a date range, grouped by metric name.
func (s *Service) Dashboard(ctx context.Context, from, to time.Time) (map[string][]Point, error) {
	rows, err := s.pool.Query(ctx, `SELECT day, metric, dim, value::float8 FROM daily_stats WHERE day BETWEEN $1 AND $2 ORDER BY metric, day, dim`, gameday.Of(from), gameday.Of(to))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]Point{}
	for rows.Next() {
		var d time.Time
		var m string
		var p Point
		if err := rows.Scan(&d, &m, &p.Dim, &p.Value); err != nil {
			return nil, err
		}
		p.Day = d.Format("2006-01-02")
		out[m] = append(out[m], p)
	}
	return out, rows.Err()
}

// Live is the small set of numbers that must be current (cheap indexed queries).
type Live struct {
	PendingWithdrawals     int64 `json:"pendingWithdrawals"`
	PendingWithdrawalCoins int64 `json:"pendingWithdrawalCoins"`
	NeedsReview            int64 `json:"withdrawalsNeedingReview"`
	PendingTopups          int64 `json:"pendingTopups"`
	QueuedMessages         int64 `json:"queuedMessages"`
	OpenGiftClaims         int64 `json:"openGiftClaims"`
	FlaggedPlayers         int64 `json:"flaggedPlayers"`
}

func (s *Service) Live(ctx context.Context) (*Live, error) {
	var l Live
	err := s.pool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM withdrawals WHERE status IN ('PENDING','PROCESSING')),
		(SELECT COALESCE(sum(amount_coins),0) FROM withdrawals WHERE status IN ('PENDING','PROCESSING')),
		(SELECT count(*) FROM withdrawals WHERE needs_review AND status IN ('PENDING','PROCESSING')),
		(SELECT count(*) FROM topup_orders WHERE status='PENDING' AND expires_at > $1),
		(SELECT count(*) FROM notifications WHERE status='PENDING'),
		(SELECT count(*) FROM player_gifts WHERE status='CLAIM_REQUESTED'),
		(SELECT count(*) FROM players WHERE hold_withdrawals OR hold_rewards)`, s.now()).
		Scan(&l.PendingWithdrawals, &l.PendingWithdrawalCoins, &l.NeedsReview, &l.PendingTopups, &l.QueuedMessages, &l.OpenGiftClaims, &l.FlaggedPlayers)
	return &l, err
}
