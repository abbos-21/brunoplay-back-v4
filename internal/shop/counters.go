package shop

import (
	"context"
	"fmt"

	"brunoplay/internal/apperr"
	"brunoplay/internal/db"
	"brunoplay/internal/gamecfg"
)

// Card requirements that count what the player does. Only what happens after the requirement started to apply counts: when the player
// reaches the level before the one that has it, the counter's value is remembered (the baseline) and the requirement is met when the
// counter has grown by the required amount since then.
const (
	ReqCollectTank = "collect_tank" // the tank was collected (a collection that brought coins)
	ReqRideFinish  = "ride_finish"  // a Bruno's Ride run was finished (any accepted run)
)

func isCounterReq(t string) bool { return t == ReqCollectTank || t == ReqRideFinish }

// baseline is the counter's value when the requirement started to apply to the player; it is remembered on first sight.
func (s *Service) baseline(ctx context.Context, q db.Querier, playerID int64, card string, level int, typ string) (int64, error) {
	if _, err := q.Exec(ctx, `INSERT INTO card_req_baselines (player_id, card, level, req_type, base)
		SELECT $1,$2,$3,$4, COALESCE((SELECT total FROM player_counters WHERE player_id=$1 AND kind=$4), 0)
		ON CONFLICT DO NOTHING`, playerID, card, level, typ); err != nil {
		return 0, err
	}
	var base int64
	err := q.QueryRow(ctx, `SELECT base FROM card_req_baselines WHERE player_id=$1 AND card=$2 AND level=$3 AND req_type=$4`, playerID, card, level, typ).Scan(&base)
	return base, err
}

// counterProgress is how much of the requirement the player has done towards `level` of the card.
func (s *Service) counterProgress(ctx context.Context, q db.Querier, playerID int64, card string, level int, typ string) (int64, error) {
	base, err := s.baseline(ctx, q, playerID, card, level, typ)
	if err != nil {
		return 0, err
	}
	var total int64
	if err := q.QueryRow(ctx, `SELECT COALESCE((SELECT total FROM player_counters WHERE player_id=$1 AND kind=$2), 0)`, playerID, typ).Scan(&total); err != nil {
		return 0, err
	}
	if total < base {
		return 0, nil
	}
	return total - base, nil
}

// startCounting remembers the baselines of the requirements of the next level of a card (called when the player reaches the level before it).
func (s *Service) startCounting(ctx context.Context, q db.Querier, playerID int64, card string, currentLevel int) error {
	t, err := s.cfg.Card(ctx, card)
	if err != nil || currentLevel < 1 || currentLevel >= len(t) {
		return nil // no table or no next level: nothing to count
	}
	next := t[currentLevel]
	for _, r := range next.Requirements {
		if isCounterReq(r.Type) {
			if _, err := s.baseline(ctx, q, playerID, card, next.Level, r.Type); err != nil {
				return err
			}
		}
	}
	return nil
}

// Record counts one thing the player did (kind is ReqCollectTank or ReqRideFinish). It runs inside the transaction of the action. The
// baselines of the requirements that apply now are taken first, so the thing being counted is the first one that counts for them.
func (s *Service) Record(ctx context.Context, tx db.Querier, playerID int64, kind string) error {
	if !isCounterReq(kind) {
		return fmt.Errorf("shop: %w", apperr.BadRequest)
	}
	p, err := s.players.Get(ctx, tx, playerID)
	if err != nil {
		return err
	}
	for _, card := range []string{gamecfg.CardMining, gamecfg.CardCapacity} {
		if err := s.startCounting(ctx, tx, playerID, card, cardLevel(p, card)); err != nil {
			return err
		}
	}
	_, err = tx.Exec(ctx, `INSERT INTO player_counters (player_id, kind, total) VALUES ($1,$2,1)
		ON CONFLICT (player_id, kind) DO UPDATE SET total = player_counters.total + 1`, playerID, kind)
	return err
}
