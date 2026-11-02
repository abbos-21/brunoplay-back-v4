package games

import (
	"context"
	"errors"
	"math"

	"github.com/jackc/pgx/v5"

	"brunoplay/internal/db"
	"brunoplay/internal/settings"
)

// Smart prizes (settings smart.*). A separate way to pick the prize of a spin draw or a chest pick, used instead of the weights:
//
//   - The first N plays of a player in a game (N = smart.welcome_plays_*) are WELCOME plays: the prizes pay back about
//     smart.welcome_payback_percent of the price on average. They start after the player's first real top-up (smart.welcome_needs_topup).
//   - After that the engine steers the player's lifetime winnings toward smart.target_payback_percent of everything they spent in the
//     game: while the player is above the target they get small prizes, when below, a bit more. Their total hovers around the target, so
//     the project keeps (100 - target) percent of what the player spends, including the cost of the welcome plays.
//
// Only the prizes the tier (or the chest pool) lists are used, picked by value instead of by weight. The payout guard still applies to
// normal plays; welcome plays are exempt (they are limited by their number per player and by the first top-up).

// smartCatchUp is how much of the gap between the player's winnings and the target is closed by the very next play (0..1).
const smartCatchUp = 0.3

type smartCfg struct {
	Enabled      bool
	WelcomeSpin  int64
	WelcomeChest int64
	WelcomePct   float64
	TargetPct    float64
	NeedsTopup   bool
}

func (s *Service) smartConfig(ctx context.Context) (smartCfg, error) {
	var c smartCfg
	if !s.settings.BoolOr(ctx, settings.KeySmartEnabled, false) {
		return c, nil
	}
	c.Enabled = true
	var err error
	if c.WelcomeSpin, err = s.settings.Int(ctx, settings.KeySmartWelcomeSpin); err != nil {
		return c, err
	}
	if c.WelcomeChest, err = s.settings.Int(ctx, settings.KeySmartWelcomeChest); err != nil {
		return c, err
	}
	if c.WelcomePct, err = s.settings.Float(ctx, settings.KeySmartWelcomePct); err != nil {
		return c, err
	}
	if c.TargetPct, err = s.settings.Float(ctx, settings.KeySmartTargetPct); err != nil {
		return c, err
	}
	c.NeedsTopup = s.settings.BoolOr(ctx, settings.KeySmartNeedsTopup, true)
	return c, nil
}

type smartState struct {
	Plays       int64
	WelcomeUsed int64
	WelcomeDone bool
	Spent       float64
	Paid        float64
}

// smartSession lives for one purchase (a spin pack or a chest round): it reads the player's state, hands out prizes, and saves the state.
// A nil session means the smart system is off.
type smartSession struct {
	game      string
	playerID  int64
	cfg       smartCfg
	coinStars float64
	eligible  bool // the welcome plays may start (the first top-up is done, or none is needed)
	welcomeN  int64
	st        smartState
}

// smartBegin loads the state of a player in a game. The caller holds the player's lock.
func (s *Service) smartBegin(ctx context.Context, tx db.Querier, playerID int64, game string) (*smartSession, error) {
	cfg, err := s.smartConfig(ctx)
	if err != nil || !cfg.Enabled {
		return nil, err
	}
	ss := &smartSession{game: game, playerID: playerID, cfg: cfg, coinStars: s.coinStars(ctx)}
	ss.welcomeN = cfg.WelcomeSpin
	if game == GameChest {
		ss.welcomeN = cfg.WelcomeChest
	}
	err = tx.QueryRow(ctx, `SELECT plays, welcome_used, welcome_done, spent, paid FROM smart_state WHERE player_id=$1 AND game=$2`, playerID, game).
		Scan(&ss.st.Plays, &ss.st.WelcomeUsed, &ss.st.WelcomeDone, &ss.st.Spent, &ss.st.Paid)
	if errors.Is(err, pgx.ErrNoRows) {
		// the first time the system sees this player: somebody who already played before it existed has had their first plays
		table := "spin_draws"
		if game == GameChest {
			table = "chest_rounds"
		}
		var prior int64
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM `+table+` WHERE player_id=$1`, playerID).Scan(&prior); err != nil {
			return nil, err
		}
		ss.st = smartState{Plays: prior, WelcomeDone: prior > 0}
	} else if err != nil {
		return nil, err
	}
	ss.eligible = true
	if cfg.NeedsTopup && !ss.st.WelcomeDone && ss.st.WelcomeUsed < ss.welcomeN {
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM topup_orders WHERE player_id=$1 AND status='PAID')`, playerID).Scan(&ss.eligible); err != nil {
			return nil, err
		}
	}
	return ss, nil
}

// startPlay counts one play and says whether it is a welcome play.
func (ss *smartSession) startPlay() bool {
	ss.st.Plays++
	if ss.eligible && !ss.st.WelcomeDone && ss.st.WelcomeUsed < ss.welcomeN {
		ss.st.WelcomeUsed++
		return true
	}
	return false
}

func (ss *smartSession) worth(p Prize) float64 {
	if p.Type == TypeCoins {
		return float64(p.Value) * ss.coinStars
	}
	return float64(p.Value)
}

// mean is what the next prize should be worth on average, in stars.
func (ss *smartSession) mean(price float64, welcome bool) float64 {
	if welcome {
		return ss.cfg.WelcomePct / 100 * price
	}
	t := ss.cfg.TargetPct / 100
	m := t*price + smartCatchUp*(t*ss.st.Spent-ss.st.Paid)
	// the most a normal play may be steered up to is the welcome size
	return math.Min(m, ss.cfg.WelcomePct/100*price)
}

// draw chooses the prize of one play (one spin draw, or one chest pick) that costs `price` stars.
func (ss *smartSession) draw(pool []Prize, price float64, welcome bool) Prize {
	vals := make([]float64, len(pool))
	for i, p := range pool {
		vals[i] = ss.worth(p)
	}
	probs := smartProbs(vals, ss.mean(price, welcome))
	r := randFloat()
	for i, pr := range probs {
		if r < pr {
			return pool[i]
		}
		r -= pr
	}
	return pool[len(pool)-1]
}

// settle records what a play really cost and paid (after the guard may have changed the prize).
func (ss *smartSession) settle(price float64, paid Prize) {
	ss.st.Spent += price
	ss.st.Paid += ss.worth(paid)
}

func (s *Service) smartSave(ctx context.Context, tx db.Querier, ss *smartSession) error {
	if ss == nil {
		return nil
	}
	_, err := tx.Exec(ctx, `INSERT INTO smart_state (player_id, game, plays, welcome_used, welcome_done, spent, paid, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
		ON CONFLICT (player_id, game) DO UPDATE SET plays=EXCLUDED.plays, welcome_used=EXCLUDED.welcome_used, welcome_done=EXCLUDED.welcome_done,
			spent=EXCLUDED.spent, paid=EXCLUDED.paid, updated_at=EXCLUDED.updated_at`,
		ss.playerID, ss.game, ss.st.Plays, ss.st.WelcomeUsed, ss.st.WelcomeDone, ss.st.Spent, ss.st.Paid, s.now())
	return err
}

// smartProbs gives every prize a chance so that the expected value of the draw is m (the prizes' values are vals). The chances follow
// value^g, with g found by bisection: g < 0 means mostly small prizes with a rare big one, g > 0 the other way round. A target at or below
// the cheapest prize always gives the cheapest, at or above the dearest always the dearest.
func smartProbs(vals []float64, m float64) []float64 {
	n := len(vals)
	probs := make([]float64, n)
	lo, hi := vals[0], vals[0]
	for _, v := range vals {
		lo, hi = math.Min(lo, v), math.Max(hi, v)
	}
	flat := func(target float64) {
		c := 0
		for _, v := range vals {
			if v == target {
				c++
			}
		}
		for i, v := range vals {
			if v == target {
				probs[i] = 1 / float64(c)
			}
		}
	}
	if m <= lo {
		flat(lo)
		return probs
	}
	if m >= hi {
		flat(hi)
		return probs
	}
	const floor = 0.001 // a prize worth (almost) nothing, such as a few coins, still has a finite logarithm
	lv := make([]float64, n)
	top := math.Inf(-1)
	for i, v := range vals {
		lv[i] = math.Log(math.Max(v, floor))
		top = math.Max(top, lv[i])
	}
	at := func(g float64) ([]float64, float64) {
		w := make([]float64, n)
		var sum float64
		for i := range w {
			w[i] = math.Exp(g * (lv[i] - top))
			sum += w[i]
		}
		var mean float64
		for i := range w {
			w[i] /= sum
			mean += w[i] * vals[i]
		}
		return w, mean
	}
	a, b := -8.0, 8.0
	for i := 0; i < 80; i++ {
		mid := (a + b) / 2
		if _, mean := at(mid); mean < m {
			a = mid
		} else {
			b = mid
		}
	}
	probs, _ = at((a + b) / 2)
	return probs
}
