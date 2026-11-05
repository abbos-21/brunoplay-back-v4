package payoutfeed

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"brunoplay/internal/settings"
	"brunoplay/internal/ton"
)

// Report says what a backfill found (and, when it was applied, queued).
type Report struct {
	Ton     int       // TON withdrawals that were paid and not posted yet
	Stars   int       // star withdrawals that were delivered and not posted yet
	First   time.Time // the oldest of them
	Last    time.Time // the newest of them
	Applied bool
}

type pastPayout struct {
	kind string
	id   int64
	at   time.Time
	p    Payload
}

func displayName(username, first string) string {
	switch {
	case username != "":
		return "@" + username
	case first != "":
		return first
	}
	return "Player"
}

// Backfill queues a post for every payout that was completed before the channel existed (or while it was off) and has no post yet:
// paid TON withdrawals and delivered star withdrawals, oldest first. The posts are spaced `spacing` apart (Telegram allows about 20
// messages a minute into one channel) and carry the date of the payout. With apply=false nothing is written. Running it again
// queues nothing twice.
func (s *Service) Backfill(ctx context.Context, apply bool, spacing time.Duration) (*Report, error) {
	if s.channel(ctx) == "" {
		return nil, fmt.Errorf("the payouts channel is not set (Settings → payouts.channel)")
	}
	var list []pastPayout
	if s.settings.BoolOr(ctx, settings.KeyPayoutsTon, true) {
		rows, err := s.pool.Query(ctx, `SELECT w.id, w.amount_coins, w.amount_nano, COALESCE(w.tx_hash, ''), COALESCE(w.processed_at, w.updated_at, w.created_at), p.username, p.first_name
			FROM withdrawals w JOIN players p ON p.id = w.player_id
			WHERE w.status = 'COMPLETED' AND NOT EXISTS (SELECT 1 FROM payout_posts x WHERE x.kind = 'ton' AND x.ref_id = w.id)`)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var pp pastPayout
			var nano int64
			var user, first string
			pp.kind = KindTon
			if err := rows.Scan(&pp.id, &pp.p.Coins, &nano, &pp.p.Tx, &pp.at, &user, &first); err != nil {
				rows.Close()
				return nil, err
			}
			pp.p.Ton = ton.TonString(nano)
			pp.p.Name = displayName(user, first)
			list = append(list, pp)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	if s.settings.BoolOr(ctx, settings.KeyPayoutsStars, true) {
		rows, err := s.pool.Query(ctx, `SELECT w.id, w.stars, COALESCE(w.processed_at, w.created_at), p.username, p.first_name
			FROM star_withdrawals w JOIN players p ON p.id = w.player_id
			WHERE w.status = 'DELIVERED' AND NOT EXISTS (SELECT 1 FROM payout_posts x WHERE x.kind = 'stars' AND x.ref_id = w.id)`)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var pp pastPayout
			var user, first string
			pp.kind = KindStars
			if err := rows.Scan(&pp.id, &pp.p.Stars, &pp.at, &user, &first); err != nil {
				rows.Close()
				return nil, err
			}
			pp.p.Name = displayName(user, first)
			list = append(list, pp)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	sort.SliceStable(list, func(i, j int) bool { return list[i].at.Before(list[j].at) })

	rep := &Report{Applied: apply}
	for _, pp := range list {
		if pp.kind == KindTon {
			rep.Ton++
		} else {
			rep.Stars++
		}
	}
	if len(list) > 0 {
		rep.First, rep.Last = list[0].at, list[len(list)-1].at
	}
	if !apply {
		return rep, nil
	}
	start := s.now().Add(spacing)
	for i, pp := range list {
		pp.p.At = pp.at.UTC().Format("02 Jan 2006")
		raw, err := jsonMarshal(pp.p)
		if err != nil {
			return rep, err
		}
		if _, err := s.pool.Exec(ctx, `INSERT INTO payout_posts (kind, ref_id, payload, next_attempt_at) VALUES ($1,$2,$3,$4) ON CONFLICT (kind, ref_id) DO NOTHING`,
			pp.kind, pp.id, raw, start.Add(time.Duration(i)*spacing)); err != nil {
			return rep, err
		}
	}
	return rep, nil
}

// Summary is a short text for the command line.
func (r *Report) Summary(spacing time.Duration) string {
	n := r.Ton + r.Stars
	if n == 0 {
		return "Nothing to post: every paid payout already has a post (or there are none)."
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d past payouts: %d TON, %d star. Oldest %s, newest %s.\n", n, r.Ton, r.Stars, r.First.UTC().Format("02 Jan 2006"), r.Last.UTC().Format("02 Jan 2006"))
	mins := (time.Duration(n) * spacing).Round(time.Minute)
	if r.Applied {
		fmt.Fprintf(&b, "Queued. The worker posts them one every %s, oldest first: about %s in all.", spacing, mins)
	} else {
		fmt.Fprintf(&b, "This was a dry run (nothing was queued). Add --apply to queue them: one post every %s, about %s in all.", spacing, mins)
	}
	return b.String()
}
