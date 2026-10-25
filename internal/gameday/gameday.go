// Package gameday defines the game day (00:00-23:59 UTC for every player) and per-day counters.
package gameday

import (
	"context"
	"time"

	"brunoplay/internal/db"
)

// Of returns the game day of an instant as a date (UTC).
func Of(t time.Time) time.Time {
	u := t.UTC()
	return time.Date(u.Year(), u.Month(), u.Day(), 0, 0, 0, 0, time.UTC)
}

// Key is the day as "2006-01-02" (used in idempotency keys).
func Key(t time.Time) string { return Of(t).Format("2006-01-02") }

// NextReset is the next 00:00 UTC after t.
func NextReset(t time.Time) time.Time { return Of(t).Add(24 * time.Hour) }

// Take adds delta to a per-day counter if the result stays within limit. It returns the new value and whether it was applied.
// A limit < 0 means no limit.
func Take(ctx context.Context, q db.Querier, playerID int64, now time.Time, key string, delta, limit int64) (int64, bool, error) {
	day := Of(now)
	var v int64
	var err error
	if limit < 0 {
		err = q.QueryRow(ctx, `INSERT INTO daily_counters (player_id, day, key, value) VALUES ($1,$2,$3,$4)
			ON CONFLICT (player_id, day, key) DO UPDATE SET value = daily_counters.value + EXCLUDED.value RETURNING value`,
			playerID, day, key, delta).Scan(&v)
		return v, err == nil, err
	}
	if delta > limit {
		return 0, false, nil
	}
	err = q.QueryRow(ctx, `INSERT INTO daily_counters (player_id, day, key, value) VALUES ($1,$2,$3,$4)
		ON CONFLICT (player_id, day, key) DO UPDATE SET value = daily_counters.value + EXCLUDED.value
		WHERE daily_counters.value + EXCLUDED.value <= $5 RETURNING value`, playerID, day, key, delta, limit).Scan(&v)
	if db.IsNoRows(err) {
		return 0, false, nil
	}
	return v, err == nil, err
}

// Get reads a per-day counter (0 when absent).
func Get(ctx context.Context, q db.Querier, playerID int64, now time.Time, key string) (int64, error) {
	var v int64
	err := q.QueryRow(ctx, `SELECT value FROM daily_counters WHERE player_id=$1 AND day=$2 AND key=$3`, playerID, Of(now), key).Scan(&v)
	if db.IsNoRows(err) {
		return 0, nil
	}
	return v, err
}
