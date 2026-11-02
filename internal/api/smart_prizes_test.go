package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"strconv"
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
	e.setSetting(settings.KeySmartGatePct, 0) // the overall limit has its own test
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

// loadRealEconomy puts the REAL seeded games economy (scripts/economy-answers.json) on the test server: the 5 spin tiers with their
// prizes, the gifts, the chest pool, the coin value of the guard.
func (e *env) loadRealEconomy() {
	e.t.Helper()
	b, err := os.ReadFile("../../scripts/economy-answers.json")
	if err != nil {
		e.t.Skip("no answers file")
	}
	var a struct {
		Tables map[string]struct {
			Rows []map[string]any `json:"rows"`
		} `json:"tables"`
	}
	if err := json.Unmarshal(b, &a); err != nil {
		e.t.Fatal(err)
	}
	e.gameConfig()
	e.setSetting(settings.KeyGifts, a.Tables["gifts"].Rows)
	tiers := []map[string]any{}
	for _, r := range a.Tables["spin_tiers"].Rows {
		tiers = append(tiers, map[string]any{"tier": r["tier"], "price_stars": r["price_stars"], "name_en": r["name_en"], "name_ru": r["name_ru"]})
	}
	e.setSetting(settings.KeySpinTiers, tiers)
	odds := map[string]map[string]float64{}
	for _, r := range a.Tables["spin_odds"].Rows {
		for col, tier := range map[string]string{"t1": "TIER_1", "t2": "TIER_2", "t3": "TIER_3", "t4": "TIER_4", "t5": "TIER_5"} {
			if w, ok := r[col].(float64); ok && w > 0 {
				if odds[tier] == nil {
					odds[tier] = map[string]float64{}
				}
				odds[tier][r["prize"].(string)] = w
			}
		}
	}
	e.setSetting(settings.KeySpinOdds, odds)
	pool := []map[string]any{}
	for _, r := range a.Tables["chest_pool"].Rows {
		if r["type"] == "gift" {
			pool = append(pool, map[string]any{"type": "gift", "gift": r["gift"], "weight": r["weight"]})
		} else {
			pool = append(pool, map[string]any{"type": r["type"], "value": r["amount"], "weight": r["weight"]})
		}
	}
	e.setSetting(settings.KeyChestPool, pool)
	e.setSetting(settings.KeyChestCost, 50)
	e.setSetting(settings.KeyChestPicks, 3)
	e.setSetting(settings.KeyGuardCoinValue, 0.00006)
}

// Many real-looking users play the real economy through the real API: they all top up, most quit after a few plays (the worst case for the
// project), some stay. With the smart system on, the overall limit and the guard, every game tier keeps the project in profit.
func TestSmartProjectStaysInProfitWithManyUsers(t *testing.T) {
	e := newEnv(t)
	e.loadRealEconomy()
	e.setSetting(settings.KeySmartEnabled, true)
	e.setSetting(settings.KeySmartWelcomeSpin, 5)
	e.setSetting(settings.KeySmartWelcomeChest, 5)
	e.setSetting(settings.KeySmartWelcomePct, 150)
	e.setSetting(settings.KeySmartTargetPct, 80)
	e.setSetting(settings.KeySmartNeedsTopup, true)
	e.setSetting(settings.KeySmartGatePct, 90)
	e.setSetting(settings.KeyGuardRatio, 0.9)
	e.setSetting(settings.KeyGuardWindowH, 72)

	tiers := []string{"TIER_1", "TIER_2", "TIER_3", "TIER_4", "TIER_5"}
	packs := []int{1, 3, 5, 10}
	seed, quit := int64(7), 0.6 // SMART_SEED / SMART_QUIT change the random users and the share who leave after 1-3 purchases
	if v, err := strconv.ParseInt(os.Getenv("SMART_SEED"), 10, 64); err == nil {
		seed = v
	}
	if v, err := strconv.ParseFloat(os.Getenv("SMART_QUIT"), 64); err == nil {
		quit = v
	}
	rng := rand.New(rand.NewSource(seed))
	const users = 90
	for u := 0; u < users; u++ {
		p := e.login(int64(7000+u), "en", "")
		e.give(p, ledger.Star, 100000000)
		e.topped(p)
		// how long this user stays: 60% quit after 1-3 purchases, 25% after ~10, 15% play 40+
		var n int
		switch r := rng.Float64(); {
		case r < quit:
			n = 1 + rng.Intn(3)
		case r < quit+(1-quit)*0.6:
			n = 8 + rng.Intn(5)
		default:
			n = 40 + rng.Intn(20)
		}
		tier := tiers[rng.Intn(len(tiers))]
		for i := 0; i < n; i++ {
			if rng.Float64() < 0.3 { // a chest round
				if r := e.do("POST", "/api/chest/round", p.Token, nil); r.Status == 200 {
					for s := 0; s < 3; s++ {
						e.do("POST", "/api/chest/pick", p.Token, map[string]any{"slot": s})
					}
				}
				continue
			}
			if r := e.spin(p, tier, packs[rng.Intn(len(packs))]); r.Status != 200 {
				t.Fatalf("spin: %d %s", r.Status, r.Body)
			}
		}
	}

	rows, err := e.d.Pool.Query(context.Background(), `SELECT game, tier, sum(spent), sum(paid) FROM payout_stats GROUP BY game, tier ORDER BY game, tier`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var allSpent, allPaid int64
	for rows.Next() {
		var game, tier string
		var spent, paid int64
		if err := rows.Scan(&game, &tier, &spent, &paid); err != nil {
			t.Fatal(err)
		}
		allSpent += spent
		allPaid += paid
		profit := (1 - float64(paid)/float64(spent)) * 100
		t.Logf("%-5s %-7s players spent %9d stars, prizes %9d stars -> project profit %+6.1f%%", game, tier, spent, paid, profit)
		if paid > spent*90/100 {
			t.Errorf("%s %s: the project paid %d on %d spent (%.1f%%), above the 90%% limit", game, tier, paid, spent, float64(paid)/float64(spent)*100)
		}
	}
	t.Logf("TOTAL players spent %d stars, prizes %d stars -> project profit %+.1f%%", allSpent, allPaid, (1-float64(allPaid)/float64(allSpent))*100)
	// the ledger agrees: what players were charged for the games equals what the stats say they spent
	var charged int64
	_ = e.d.Pool.QueryRow(context.Background(), `SELECT COALESCE(-sum(amount),0) FROM ledger_entries WHERE currency='STAR' AND reason IN ('spin_charge','chest_charge')`).Scan(&charged)
	if charged != allSpent {
		t.Fatalf("ledger charged %d, stats say %d", charged, allSpent)
	}
}

// The overall limit: while the game as a whole already paid too much, no new welcome is given; and a welcome prize can never push the
// payout above the limit (it is stepped down to a cheaper prize, here the cheapest).
func TestSmartOverallLimitStopsTheWelcome(t *testing.T) {
	e := newEnv(t)
	e.smartConfig()
	e.setSetting(settings.KeySmartGatePct, 1) // absurdly low: any payout is "too much"
	p := e.login(6010, "en", "")
	e.give(p, ledger.Star, 1000000)
	e.topped(p)

	e.setSetting(settings.KeySmartWelcomeSpin, 20)

	r := e.spin(p, "TIER_1", 5) // the gate is open at the start (nothing paid yet), but every welcome prize is held to the limit
	for _, res := range r.JSON(t)["results"].([]any) {
		if got := res.(map[string]any)["gift"]; got != "g50" {
			t.Fatalf("a welcome prize above the limit steps down to the cheapest gift, got %v", got)
		}
	}
	e.spin(p, "TIER_1", 1) // the game now paid 250 of 600 spent: above 1%, the welcome is closed
	if _, welcome, _, _, _ := e.smartState(p, "spin"); welcome != 5 {
		t.Fatalf("the welcome must pause while the game pays too much: used %d", welcome)
	}
	e.setSetting(settings.KeySmartGatePct, 0) // no limit: the welcome continues where it stopped
	e.spin(p, "TIER_1", 1)
	if _, welcome, _, _, _ := e.smartState(p, "spin"); welcome != 6 {
		t.Fatalf("welcome after the limit is lifted: %d", welcome)
	}
}
