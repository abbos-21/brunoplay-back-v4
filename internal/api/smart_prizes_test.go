package api_test

import (
	"context"
	"fmt"
	"testing"

	"brunoplay/internal/ledger"
	"brunoplay/internal/settings"
)

// smartConfig: four gifts (50 / 100 / 200 / 400 stars) in one 100-star spin tier, a 30-star chest of the same prizes; the weights are
// deliberately silly (the cheapest gift has weight 0.0001) to prove that the smart system does not use them.
func (e *env) smartConfig() {
	e.gamesConfig()
	e.setSetting(settings.KeyGifts, []map[string]any{
		{"key": "g50", "name_en": "G50", "name_ru": "G50", "star_value": 50},
		{"key": "g100", "name_en": "G100", "name_ru": "G100", "star_value": 100},
		{"key": "g200", "name_en": "G200", "name_ru": "G200", "star_value": 200},
		{"key": "g400", "name_en": "G400", "name_ru": "G400", "star_value": 400},
	})
	e.setSetting(settings.KeySpinTiers, []map[string]any{{"tier": "TIER_1", "price_stars": 100, "name_en": "Bronze", "name_ru": "Бронза"}})
	e.setSetting(settings.KeySpinOdds, map[string]any{"TIER_1": map[string]float64{"g50": 0.0001, "g100": 1, "g200": 1, "g400": 1}})
	e.setSetting(settings.KeyChestCost, 30)
	e.setSetting(settings.KeyChestPicks, 3)
	e.setSetting(settings.KeyChestPool, []map[string]any{
		{"type": "gift", "gift": "g50", "weight": 0.0001}, {"type": "gift", "gift": "g100", "weight": 1},
		{"type": "gift", "gift": "g200", "weight": 1}, {"type": "gift", "gift": "g400", "weight": 1},
	})
	e.setSetting(settings.KeySmartEnabled, true)
	e.setSetting(settings.KeySmartWelcomeSpin, 5)
	e.setSetting(settings.KeySmartWelcomeChest, 2)
	e.setSetting(settings.KeySmartWelcomePct, 150)
	e.setSetting(settings.KeySmartTargetPct, 80)
	e.setSetting(settings.KeySmartNeedsTopup, true)
}

func (e *env) smartState(p player, game string) (plays, welcome int64, done bool, spent, paid float64) {
	err := e.d.Pool.QueryRow(context.Background(), `SELECT plays, welcome_used, welcome_done, spent, paid FROM smart_state WHERE player_id=$1 AND game=$2`, p.ID, game).
		Scan(&plays, &welcome, &done, &spent, &paid)
	if err != nil {
		e.t.Fatalf("smart_state %s: %v", game, err)
	}
	return
}

func (e *env) topped(p player) {
	e.exec(`INSERT INTO topup_orders (id, player_id, method, status, pack_stars, price, expires_at, paid_at) VALUES ($1,$2,'TG_STARS','PAID',100,100, now() + interval '1 day', now())`,
		fmt.Sprintf("smart-%d", p.ID), p.ID)
}

// With the system off nothing is remembered and the weights decide, as before.
func TestSmartOffUsesTheWeights(t *testing.T) {
	e := newEnv(t)
	e.smartConfig()
	e.setSetting(settings.KeySmartEnabled, false)
	p := e.login(6001, "en", "")
	e.give(p, ledger.Star, 100000)
	if r := e.spin(p, "TIER_1", 10); r.Status != 200 {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	if e.countRows("smart_state") != 0 {
		t.Fatal("the smart system is off: no state is kept")
	}
}

// The welcome plays wait for the first top-up, then the first N plays of the game are welcome plays (a spin pack counts each draw).
func TestSmartWelcomePlaysStartAfterTheFirstTopup(t *testing.T) {
	e := newEnv(t)
	e.smartConfig()
	p := e.login(6002, "en", "")
	e.give(p, ledger.Star, 1000000)

	e.spin(p, "TIER_1", 3) // no top-up yet: normal plays
	if plays, welcome, _, _, _ := e.smartState(p, "spin"); plays != 3 || welcome != 0 {
		t.Fatalf("before the top-up: plays %d welcome %d", plays, welcome)
	}
	e.topped(p)
	e.spin(p, "TIER_1", 10) // 5 welcome draws, then 5 normal ones
	plays, welcome, done, _, _ := e.smartState(p, "spin")
	if plays != 13 || welcome != 5 || done {
		t.Fatalf("after the top-up: plays %d welcome %d done %v", plays, welcome, done)
	}
	e.spin(p, "TIER_1", 10)
	if _, welcome, _, _, _ := e.smartState(p, "spin"); welcome != 5 {
		t.Fatalf("the welcome never grows past its number: %d", welcome)
	}
}

// A player who played before the system existed has had their first plays: no welcome for them.
func TestSmartExistingPlayersGetNoWelcome(t *testing.T) {
	e := newEnv(t)
	e.smartConfig()
	e.setSetting(settings.KeySmartEnabled, false)
	p := e.login(6003, "en", "")
	e.give(p, ledger.Star, 1000000)
	e.topped(p)
	e.spin(p, "TIER_1", 3)
	e.setSetting(settings.KeySmartEnabled, true)
	e.spin(p, "TIER_1", 5)
	plays, welcome, done, _, _ := e.smartState(p, "spin")
	if plays != 8 || welcome != 0 || !done {
		t.Fatalf("plays %d welcome %d done %v", plays, welcome, done)
	}
}

// A chest round is one play; its picks are drawn without the weights.
func TestSmartChestRoundsAreWelcomePlays(t *testing.T) {
	e := newEnv(t)
	e.smartConfig()
	p := e.login(6004, "en", "")
	e.give(p, ledger.Star, 1000000)
	e.topped(p)
	finish := func() {
		t.Helper()
		if r := e.do("POST", "/api/chest/round", p.Token, nil); r.Status != 200 {
			t.Fatalf("start: %d %s", r.Status, r.Body)
		}
		for s := 0; s < 3; s++ {
			if r := e.do("POST", "/api/chest/pick", p.Token, map[string]any{"slot": s}); r.Status != 200 {
				t.Fatalf("pick: %d %s", r.Status, r.Body)
			}
		}
	}
	for i := 0; i < 3; i++ {
		finish()
	}
	plays, welcome, _, spent, paid := e.smartState(p, "chest")
	if plays != 3 || welcome != 2 || spent != 90 || paid <= 0 {
		t.Fatalf("plays %d welcome %d spent %v paid %v", plays, welcome, spent, paid)
	}
}

// Welcome draws are exempt from the payout guard; later draws are not. And over many plays the player's total settles at the target.
func TestSmartSteersTheLifetimePayback(t *testing.T) {
	e := newEnv(t)
	e.smartConfig()
	e.setSetting(settings.KeyGuardRatio, 0.01) // would step every draw down to the cheapest gift
	p := e.login(6005, "en", "")
	e.give(p, ledger.Star, 100000000)
	e.topped(p)
	e.spin(p, "TIER_1", 5) // five welcome draws
	if e.countRows("guard_events") != 0 {
		t.Fatal("welcome draws skip the guard")
	}
	e.setSetting(settings.KeyGuardRatio, 0)
	for i := 0; i < 60; i++ {
		e.spin(p, "TIER_1", 10)
	}
	_, _, _, spent, paid := e.smartState(p, "spin")
	if spent != 605*100 {
		t.Fatalf("spent %v", spent)
	}
	if ratio := paid / spent; ratio < 0.7 || ratio > 0.9 {
		t.Fatalf("lifetime payback %.3f, the target is 0.80", ratio)
	}
	// what the ledger says agrees with the state: stars charged vs the value of the gifts won
	var won float64
	_ = e.d.Pool.QueryRow(context.Background(), `SELECT COALESCE(sum(star_value),0) FROM spin_draws WHERE player_id=$1`, p.ID).Scan(&won)
	if won != paid {
		t.Fatalf("draw log says %v, the state says %v", won, paid)
	}
}
