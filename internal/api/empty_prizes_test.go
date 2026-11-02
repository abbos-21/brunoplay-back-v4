package api_test

import (
	"context"
	"testing"

	"brunoplay/internal/ledger"
	"brunoplay/internal/settings"
)

func spinTypes(t *testing.T, r resp) map[string]int {
	t.Helper()
	if r.Status != 200 {
		t.Fatalf("spin: %d %s", r.Status, r.Body)
	}
	out := map[string]int{}
	for _, x := range r.JSON(t)["results"].([]any) {
		out[x.(map[string]any)["type"].(string)]++
	}
	return out
}

// A spin can end on an EMPTY slot: the stars are charged, nothing is paid, nothing enters the collection or the winners list.
func TestSpinEmptySlot(t *testing.T) {
	e := newEnv(t)
	e.gamesConfig()
	e.setSetting(settings.KeyWinnersMin, 0) // every prize would be listed, but an empty one must not be
	p := e.login(8001, "en", "")
	e.give(p, ledger.Star, 1000000)

	// no weight set: there is no empty slot
	for i := 0; i < 20; i++ {
		if n := spinTypes(t, e.spin(p, "TIER_1", 10))["empty"]; n != 0 {
			t.Fatalf("without a weight nothing is empty: %d", n)
		}
	}
	// a huge weight: every draw is empty
	before := e.stars(p)
	e.setSetting(settings.KeySpinEmptyWeight, 1e9)
	r := e.spin(p, "TIER_1", 10)
	if got := spinTypes(t, r); got["empty"] != 10 {
		t.Fatalf("all empty: %v", got)
	}
	if e.stars(p) != before-1000 {
		t.Fatalf("the stars are charged and nothing comes back: %d -> %d", before, e.stars(p))
	}
	var rows, emptyRows int
	_ = e.d.Pool.QueryRow(context.Background(), `SELECT count(*) FILTER (WHERE gift_key='empty'), count(*) FILTER (WHERE gift_key='empty' AND star_value<>0) FROM spin_draws`).Scan(&rows, &emptyRows)
	if rows != 10 || emptyRows != 0 {
		t.Fatalf("draw log: %d empty rows, %d with a value", rows, emptyRows)
	}
	var paid int
	_ = e.d.Pool.QueryRow(context.Background(), `SELECT count(*) FROM winners_feed WHERE prize_type='empty'`).Scan(&paid)
	if paid != 0 {
		t.Fatal("an empty draw is never in the winners list")
	}
	// the tier lists the empty slot so the wheel can show it
	tiers := e.do("GET", "/api/spin/tiers", p.Token, nil).JSON(t)["tiers"].([]any)
	found := false
	for _, pr := range tiers[0].(map[string]any)["prizes"].([]any) {
		found = found || pr.(map[string]any)["type"] == "empty"
	}
	if !found {
		t.Fatalf("the tier must list the empty slot: %v", tiers[0])
	}
}

// The weight is relative to the other weights: equal weight = half of the draws.
func TestSpinEmptyWeightIsRelative(t *testing.T) {
	e := newEnv(t)
	e.gamesConfig()
	e.setSetting(settings.KeySpinOdds, map[string]any{"TIER_1": map[string]float64{"small": 50, "mid": 50}, "TIER_2": map[string]float64{"small": 1}})
	e.setSetting(settings.KeySpinEmptyWeight, 100) // 100 of 200
	p := e.login(8002, "en", "")
	e.give(p, ledger.Star, 100000000)
	empty, total := 0, 0
	for i := 0; i < 80; i++ {
		got := spinTypes(t, e.spin(p, "TIER_1", 10))
		empty += got["empty"]
		for _, n := range got {
			total += n
		}
	}
	if f := float64(empty) / float64(total); f < 0.44 || f > 0.56 {
		t.Fatalf("half of %d draws should be empty, got %.3f", total, f)
	}
}

// One tier can have its own empty weight in the odds; the other tiers keep the general one.
func TestSpinEmptyWeightPerTier(t *testing.T) {
	e := newEnv(t)
	e.gamesConfig()
	e.setSetting(settings.KeySpinOdds, map[string]any{"TIER_1": map[string]float64{"small": 1, "empty": 1e9}, "TIER_2": map[string]float64{"small": 1}})
	p := e.login(8003, "en", "")
	e.give(p, ledger.Star, 10000000)
	if got := spinTypes(t, e.spin(p, "TIER_1", 10)); got["empty"] != 10 {
		t.Fatalf("tier 1 has its own empty weight: %v", got)
	}
	if got := spinTypes(t, e.spin(p, "TIER_2", 10)); got["empty"] != 0 {
		t.Fatalf("tier 2 has no empty slot: %v", got)
	}
	// "empty": 0 in the odds switches the general weight off for that tier
	e.setSetting(settings.KeySpinEmptyWeight, 1e9)
	e.setSetting(settings.KeySpinOdds, map[string]any{"TIER_1": map[string]float64{"small": 1, "empty": 0}, "TIER_2": map[string]float64{"small": 1}})
	if got := spinTypes(t, e.spin(p, "TIER_1", 10)); got["empty"] != 0 {
		t.Fatalf("an explicit 0 turns the empty slot off for the tier: %v", got)
	}
	if got := spinTypes(t, e.spin(p, "TIER_2", 10)); got["empty"] != 10 {
		t.Fatalf("the other tier uses the general weight: %v", got)
	}
}

// The payout guard steps a big prize down to a smaller PRIZE, never to nothing.
func TestGuardNeverStepsDownToEmpty(t *testing.T) {
	e := newEnv(t)
	e.gamesConfig()
	e.setSetting(settings.KeySpinOdds, map[string]any{"TIER_1": map[string]float64{"small": 1}, "TIER_2": map[string]float64{"big": 100}})
	e.setSetting(settings.KeySpinEmptyWeight, 0.0000001) // there is an empty slot, but it is practically never drawn
	e.setSetting(settings.KeyGuardRatio, 3.0)
	p := e.login(8004, "en", "")
	e.give(p, ledger.Star, 1000000)
	for i := 0; i < 5; i++ {
		r := e.spin(p, "TIER_2", 1)
		if got := r.JSON(t)["results"].([]any)[0].(map[string]any)["type"]; got == "empty" {
			t.Fatalf("the guard must never turn a prize into an empty result")
		}
	}
}

// A chest can be empty: it opens with nothing, the round still ends, and nothing is paid.
func TestChestEmpty(t *testing.T) {
	e := newEnv(t)
	e.gamesConfig()
	p := e.login(8005, "en", "")
	e.give(p, ledger.Star, 1000)
	play := func() (types map[string]int, finished bool) {
		t.Helper()
		if r := e.do("POST", "/api/chest/round", p.Token, nil); r.Status != 200 {
			t.Fatalf("round: %d %s", r.Status, r.Body)
		}
		types = map[string]int{}
		for s := 0; s < 3; s++ {
			r := e.do("POST", "/api/chest/pick", p.Token, map[string]any{"slot": s})
			if r.Status != 200 {
				t.Fatalf("pick: %d %s", r.Status, r.Body)
			}
			m := r.JSON(t)
			types[m["prize"].(map[string]any)["type"].(string)]++
			finished = m["finished"] == true
		}
		return
	}
	// no weight: no empty chests
	for i := 0; i < 10; i++ {
		if got, _ := play(); got["empty"] != 0 {
			t.Fatalf("no weight, no empty chest: %v", got)
		}
	}
	// a huge weight: all three are empty, the round is over, the price is not refunded
	e.setSetting(settings.KeyChestEmptyWeight, 1e9)
	before, coinsBefore := e.stars(p), e.coins(p)
	got, fin := play()
	if got["empty"] != 3 || !fin {
		t.Fatalf("all empty and finished: %v %v", got, fin)
	}
	if e.stars(p) != before-10 || e.coins(p) != coinsBefore {
		t.Fatalf("only the price is taken: stars %d -> %d, coins %d -> %d", before, e.stars(p), coinsBefore, e.coins(p))
	}
	// an "empty" entry in the pool itself works too
	e.setSetting(settings.KeyChestEmptyWeight, 0)
	e.setSetting(settings.KeyChestPool, []map[string]any{{"type": "stars", "value": 5, "weight": 1}, {"type": "empty", "weight": 1e9}})
	if got, _ := play(); got["empty"] != 3 {
		t.Fatalf("pool entry: %v", got)
	}
}
