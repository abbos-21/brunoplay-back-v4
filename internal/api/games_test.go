package api_test

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"brunoplay/internal/admin"
	"brunoplay/internal/ledger"
	"brunoplay/internal/settings"
)

// gamesConfig: three gifts (100 / 1,000 / 100,000 stars), two spin tiers, a chest pool, Ride limits.
func (e *env) gamesConfig() {
	e.gameConfig()
	e.setSetting(settings.KeyGifts, []map[string]any{
		{"key": "small", "name_en": "Small", "name_ru": "Малый", "star_value": 100},
		{"key": "mid", "name_en": "Mid", "name_ru": "Средний", "star_value": 1000},
		{"key": "big", "name_en": "Big", "name_ru": "Большой", "star_value": 100000},
	})
	e.setSetting(settings.KeySpinTiers, []map[string]any{
		{"tier": "TIER_1", "price_stars": 100, "name_en": "Bronze", "name_ru": "Бронза"},
		{"tier": "TIER_2", "price_stars": 500, "name_en": "Silver"},
	})
	e.setSetting(settings.KeySpinOdds, map[string]any{
		"TIER_1": map[string]float64{"small": 70, "mid": 30},
		"TIER_2": map[string]float64{"small": 10, "mid": 60, "big": 30},
	})
	e.setSetting(settings.KeyChestCost, 10)
	e.setSetting(settings.KeyChestPicks, 3)
	e.setSetting(settings.KeyChestPool, []map[string]any{
		{"type": "coins", "value": 100, "weight": 40},
		{"type": "stars", "value": 5, "weight": 40},
		{"type": "gift", "gift": "small", "weight": 20},
	})
	e.setSetting(settings.KeyRideCost, 1)
	e.setSetting(settings.KeyRideDailyCoins, 100)
}

func (e *env) spin(p player, tier string, count int) resp {
	return e.do("POST", "/api/spin", p.Token, map[string]any{"tier": tier, "count": count})
}

func (e *env) ledgerSum(p player, cur string) int64 {
	var s int64
	_ = e.d.Pool.QueryRow(context.Background(), `SELECT COALESCE(sum(amount),0) FROM ledger_entries WHERE player_id=$1 AND currency=$2`, p.ID, cur).Scan(&s)
	return s
}

// The wheel can pay coins and stars next to gifts: "coins:500" and "stars:25" in the odds table.
func TestSpinCanPayCoinsAndStars(t *testing.T) {
	e := newEnv(t)
	e.gamesConfig()
	e.setSetting(settings.KeySpinOdds, map[string]any{
		"TIER_1": map[string]float64{"coins:500": 50, "stars:25": 50},
		"TIER_2": map[string]float64{"small": 1},
	})
	p := e.login(520, "en", "")
	e.give(p, ledger.Star, 10000)

	// the player's tier list shows the coin and star prizes
	tiers := e.do("GET", "/api/spin/tiers", p.Token, nil).JSON(t)["tiers"].([]any)
	seen := map[string]bool{}
	for _, pr := range tiers[0].(map[string]any)["prizes"].([]any) {
		seen[pr.(map[string]any)["type"].(string)] = true
	}
	if !seen["coins"] || !seen["stars"] || seen["gift"] {
		t.Fatalf("tier 1 shows coins and stars only: %v", tiers[0])
	}

	starsBefore, coinsBefore := e.stars(p), e.coins(p)
	r := e.spin(p, "TIER_1", 10)
	if r.Status != 200 {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	res := r.JSON(t)
	var coinsWon, starsWon float64
	for _, x := range res["results"].([]any) {
		pr := x.(map[string]any)
		switch pr["type"] {
		case "coins":
			if pr["value"].(float64) != 500 || pr["gift"] != nil {
				t.Fatalf("coin prize: %v", pr)
			}
			coinsWon += 500
		case "stars":
			if pr["value"].(float64) != 25 {
				t.Fatalf("star prize: %v", pr)
			}
			starsWon += 25
		default:
			t.Fatalf("only coins and stars are in this tier: %v", pr)
		}
	}
	if res["totalCoinsWon"].(float64) != coinsWon || res["totalStarsWon"].(float64) != starsWon {
		t.Fatalf("totals %v / %v against %v / %v", res["totalCoinsWon"], res["totalStarsWon"], coinsWon, starsWon)
	}
	if got := e.stars(p); got != starsBefore-1000+int64(starsWon) {
		t.Fatalf("stars: paid 1,000, won %v: %d from %d", starsWon, got, starsBefore)
	}
	if got := e.coins(p); got != coinsBefore+int64(coinsWon) {
		t.Fatalf("coins: won %v: %d from %d", coinsWon, got, coinsBefore)
	}
	if e.countRows("player_gifts") != 0 || e.countRows("spin_draws") != 10 {
		t.Fatalf("no gifts, ten logged draws: gifts=%d draws=%d", e.countRows("player_gifts"), e.countRows("spin_draws"))
	}
	if e.ledgerSum(p, "STAR") != e.stars(p) {
		t.Fatalf("ledger %d vs balance %d", e.ledgerSum(p, "STAR"), e.stars(p))
	}

	// the settings form refuses a malformed prize
	for _, bad := range []map[string]any{
		{"TIER_1": map[string]float64{"coins:abc": 1}},
		{"TIER_1": map[string]float64{"gems:5": 1}},
		{"TIER_1": map[string]float64{"coins:0": 1}},
		{"TIER_1": map[string]float64{"coins:500": 0}},
	} {
		if _, err := e.d.Settings.Validate(settings.KeySpinOdds, bad); err == nil {
			t.Errorf("must be refused: %v", bad)
		}
	}
	if _, err := e.d.Settings.Validate(settings.KeySpinOdds, map[string]any{"TIER_1": map[string]float64{"apple": 3, "coins:500": 2, "stars:5": 1}}); err != nil {
		t.Errorf("a valid mix must pass: %v", err)
	}
}

func TestSpinChargesDrawsAndStoresGifts(t *testing.T) {
	e := newEnv(t)
	p := e.login(500, "ru", "")
	if r := e.do("GET", "/api/spin/tiers", p.Token, nil); r.Code(t) != "CONFIG_MISSING" {
		t.Fatalf("%s", r.Body)
	}
	e.gamesConfig()
	tiers := e.do("GET", "/api/spin/tiers", p.Token, nil).JSON(t)["tiers"].([]any)
	if len(tiers) != 2 || tiers[0].(map[string]any)["name"] != "Бронза" || tiers[0].(map[string]any)["costStars"].(float64) != 100 || tiers[1].(map[string]any)["name"] != "Silver" {
		t.Fatalf("%v", tiers)
	}
	for _, pr := range tiers[1].(map[string]any)["prizes"].([]any) {
		if pr.(map[string]any)["weight"].(float64) != 1 {
			t.Fatalf("odds are hidden unless published: %v", pr)
		}
	}
	e.setSetting(settings.KeySpinPublishOdds, true)
	if w := e.do("GET", "/api/spin/tiers", p.Token, nil).JSON(t)["tiers"].([]any)[1].(map[string]any)["prizes"].([]any); w[0].(map[string]any)["weight"].(float64) == 1 && w[1].(map[string]any)["weight"].(float64) == 1 {
		t.Fatalf("published odds: %v", w)
	}

	for _, bad := range []struct {
		tier  string
		count int
	}{{"TIER_1", 2}, {"TIER_1", 11}, {"NOPE", 1}} {
		if r := e.spin(p, bad.tier, bad.count); r.Status != 422 && r.Status != 400 {
			t.Errorf("%v must be refused: %d", bad, r.Status)
		}
	}
	if r := e.spin(p, "TIER_1", 1); r.Code(t) != "INSUFFICIENT_STARS" {
		t.Fatalf("%s", r.Body)
	}
	if n := e.countRows("player_gifts"); n != 0 || e.stars(p) != 0 {
		t.Fatalf("a refused spin leaves nothing behind: gifts=%d stars=%d", n, e.stars(p))
	}

	e.give(p, ledger.Star, 2000)
	r := e.spin(p, "TIER_1", 1)
	if r.Status != 200 {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	res := r.JSON(t)
	if e.stars(p) != 1900 || res["spent"].(float64) != 100 || len(res["results"].([]any)) != 1 {
		t.Fatalf("one spin costs one price: %v stars=%d", res, e.stars(p))
	}
	pr := res["results"].([]any)[0].(map[string]any)
	if pr["type"] != "gift" || (pr["gift"] != "small" && pr["gift"] != "mid") || pr["giftId"].(float64) == 0 {
		t.Fatalf("tier 1 gives small or mid gifts: %v", pr)
	}
	// 10 spins are charged at once and every result is stored
	r = e.spin(p, "TIER_2", 10)
	if r.Status != 200 || len(r.JSON(t)["results"].([]any)) != 10 || e.stars(p) != 1900-5000+0 && e.stars(p) != -3100 {
		// not enough: 5,000 needed
	}
	if r.Code(t) != "INSUFFICIENT_STARS" {
		t.Fatalf("5,000 stars needed: %d %s", r.Status, r.Body)
	}
	e.give(p, ledger.Star, 4000)
	r = e.spin(p, "TIER_2", 10)
	if r.Status != 200 || len(r.JSON(t)["results"].([]any)) != 10 || e.stars(p) != 900 {
		t.Fatalf("10x: %d %s stars=%d", r.Status, r.Body, e.stars(p))
	}
	if n := e.countRows("player_gifts"); n != 11 {
		t.Fatalf("gifts in the collection: %d", n)
	}
	if n := e.countRows("spin_draws"); n != 11 {
		t.Fatalf("draw log: %d", n)
	}
	if e.ledgerSum(p, "STAR") != e.stars(p) {
		t.Fatalf("ledger %d vs balance %d", e.ledgerSum(p, "STAR"), e.stars(p))
	}
	// the winners feed shows @username
	w := e.do("GET", "/api/spin/winners", p.Token, nil).JSON(t)
	list := w["data"].([]any)
	if len(list) == 0 || list[0].(map[string]any)["username"] != "user500" || w["total"].(float64) != 11 {
		t.Fatalf("winners: %v", w)
	}
	// the gift list is in the player's language
	g := e.do("GET", "/api/gifts", p.Token, nil).JSON(t)["gifts"].([]any)
	if len(g) != 11 || g[0].(map[string]any)["status"] != "OWNED" || !strings.ContainsAny(g[0].(map[string]any)["name"].(string), "МСБ") {
		t.Fatalf("gifts: %v", g[0])
	}
}

func (e *env) countRows(table string) int {
	var n int
	_ = e.d.Pool.QueryRow(context.Background(), `SELECT count(*) FROM `+table).Scan(&n)
	return n
}

func TestSpinOddsAreRespected(t *testing.T) {
	e := newEnv(t)
	e.gamesConfig()
	p := e.login(501, "en", "")
	e.give(p, ledger.Star, 1000000)
	for i := 0; i < 150; i++ {
		if r := e.spin(p, "TIER_1", 10); r.Status != 200 {
			t.Fatalf("%d %s", r.Status, r.Body)
		}
	}
	var small, mid int
	_ = e.d.Pool.QueryRow(context.Background(), `SELECT count(*) FILTER (WHERE gift_key='small'), count(*) FILTER (WHERE gift_key='mid') FROM spin_draws`).Scan(&small, &mid)
	frac := float64(small) / float64(small+mid)
	if small+mid != 1500 || frac < 0.65 || frac > 0.75 {
		t.Fatalf("70/30 odds over 1500 draws: small=%d mid=%d (%.3f)", small, mid, frac)
	}
}

func TestPayoutGuardStepsBigPrizesDown(t *testing.T) {
	e := newEnv(t)
	e.gamesConfig()
	// tier 2 mostly gives "big" (100,000) for a 500-star spin
	e.setSetting(settings.KeySpinOdds, map[string]any{"TIER_1": map[string]float64{"small": 1}, "TIER_2": map[string]float64{"big": 100}})
	p := e.login(502, "en", "")
	e.give(p, ledger.Star, 1000000)

	// guard off: the drawn prize is paid
	if r := e.spin(p, "TIER_2", 1); r.JSON(t)["results"].([]any)[0].(map[string]any)["gift"] != "big" {
		t.Fatalf("guard off pays what was drawn: %s", r.Body)
	}
	if e.countRows("guard_events") != 0 {
		t.Fatal("no events with the guard off")
	}
	// guard on at 300 %: 100,000 on a 500 spin is far above, the draw is stepped down to the smallest gift
	e.setSetting(settings.KeyGuardRatio, 3.0)
	r := e.spin(p, "TIER_2", 1)
	if got := r.JSON(t)["results"].([]any)[0].(map[string]any)["gift"]; got != "small" {
		t.Fatalf("guard must step down to the smallest prize in the pool, got %v", got)
	}
	if e.countRows("guard_events") == 0 {
		t.Fatal("every intervention is logged")
	}
	var from, to string
	_ = e.d.Pool.QueryRow(context.Background(), `SELECT from_key, to_key FROM guard_events ORDER BY id DESC LIMIT 1`).Scan(&from, &to)
	if from != "gift:big" || to != "gift:small" {
		t.Fatalf("event: %s -> %s", from, to)
	}
	// the draw log keeps what the odds gave and what was paid
	var drawn, paid string
	_ = e.d.Pool.QueryRow(context.Background(), `SELECT drawn_key, gift_key FROM spin_draws ORDER BY id DESC LIMIT 1`).Scan(&drawn, &paid)
	if drawn != "big" || paid != "small" {
		t.Fatalf("draw log: drawn=%s paid=%s", drawn, paid)
	}
	// the admin sees the live ratio and the interventions
	fin := e.adminLogin("root@example.com", admin.RoleSuper)
	g := e.do("GET", "/api/admin/games/guard", fin, nil).JSON(t)
	if len(g["stats"].([]any)) == 0 || len(g["events"].([]any)) != 1 {
		t.Fatalf("%v", g)
	}
}

func TestGuardLetsPrizesThroughWhileRatioIsLow(t *testing.T) {
	e := newEnv(t)
	e.gamesConfig()
	e.setSetting(settings.KeySpinOdds, map[string]any{"TIER_1": map[string]float64{"small": 1, "mid": 1}, "TIER_2": map[string]float64{"small": 1}})
	e.setSetting(settings.KeyGuardRatio, 1.2) // pay out at most 120% of what was spent
	p := e.login(503, "en", "")
	e.give(p, ledger.Star, 10000000)
	for i := 0; i < 40; i++ {
		e.spin(p, "TIER_1", 10)
	}
	var spent, paid int64
	_ = e.d.Pool.QueryRow(context.Background(), `SELECT sum(spent), sum(paid) FROM payout_stats WHERE game='spin' AND tier='TIER_1'`).Scan(&spent, &paid)
	// the smallest gift (100) equals the price (100), so the ratio can always be met: it must not run away above the limit
	if float64(paid)/float64(spent) > 1.2+0.0001 {
		t.Fatalf("payout ratio %.3f exceeds the guard limit", float64(paid)/float64(spent))
	}
	if paid == 0 {
		t.Fatal("prizes still go out")
	}
}

func TestGiftSellClaimDeliverReject(t *testing.T) {
	e := newEnv(t)
	e.gamesConfig()
	p, q := e.login(510, "en", ""), e.login(511, "en", "")
	e.give(p, ledger.Star, 1000)
	e.spin(p, "TIER_1", 3)
	gifts := e.do("GET", "/api/gifts", p.Token, nil).JSON(t)["gifts"].([]any)
	id := func(i int) int64 { return int64(gifts[i].(map[string]any)["id"].(float64)) }
	val := func(i int) int64 { return int64(gifts[i].(map[string]any)["starValue"].(float64)) }

	// selling gives the full star value, once; nobody else can sell it
	before := e.stars(p)
	if r := e.do("POST", fmt.Sprintf("/api/gifts/%d/sell", id(0)), q.Token, nil); r.Code(t) != "GIFT_NOT_AVAILABLE" {
		t.Fatalf("someone else's gift: %s", r.Body)
	}
	var ok int64
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if e.do("POST", fmt.Sprintf("/api/gifts/%d/sell", id(0)), p.Token, nil).Status == 200 {
				atomic.AddInt64(&ok, 1)
			}
		}()
	}
	wg.Wait()
	if ok != 1 || e.stars(p) != before+val(0) {
		t.Fatalf("sold %d times, stars %d want %d", ok, e.stars(p), before+val(0))
	}
	// claim: a gift can be sold OR claimed
	if r := e.do("POST", fmt.Sprintf("/api/gifts/%d/claim", id(0)), p.Token, nil); r.Code(t) != "GIFT_NOT_AVAILABLE" {
		t.Fatalf("a sold gift cannot be claimed: %s", r.Body)
	}
	if r := e.do("POST", fmt.Sprintf("/api/gifts/%d/claim", id(1)), p.Token, nil); r.Status != 200 || r.JSON(t)["status"] != "CLAIM_REQUESTED" {
		t.Fatalf("claim: %d %s", r.Status, r.Body)
	}
	if r := e.do("POST", fmt.Sprintf("/api/gifts/%d/sell", id(1)), p.Token, nil); r.Code(t) != "GIFT_NOT_AVAILABLE" {
		t.Fatalf("a claimed gift cannot be sold: %s", r.Body)
	}
	if r := e.do("POST", fmt.Sprintf("/api/gifts/%d/claim", id(1)), p.Token, nil); r.Status != 409 {
		t.Fatalf("claiming twice: %d", r.Status)
	}
	// admin side: support sees the request and delivers; finance has no access
	sup := e.adminLogin("sup@example.com", admin.RoleSupport)
	fin := e.adminLogin("fin@example.com", admin.RoleFinance)
	if r := e.do("GET", "/api/admin/gifts/claims", fin, nil); r.Status != 403 {
		t.Fatalf("%d", r.Status)
	}
	claims := e.do("GET", "/api/admin/gifts/claims", sup, nil).JSON(t)["claims"].([]any)
	if len(claims) != 1 || claims[0].(map[string]any)["username"] != "user510" {
		t.Fatalf("%v", claims)
	}
	if r := e.do("POST", fmt.Sprintf("/api/admin/gifts/%d/deliver", id(1)), sup, nil); r.Status != 200 {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	if r := e.do("POST", fmt.Sprintf("/api/admin/gifts/%d/deliver", id(1)), sup, nil); r.Status != 409 {
		t.Fatalf("delivering twice: %d", r.Status)
	}
	// reject returns the gift to the collection with the reason
	e.do("POST", fmt.Sprintf("/api/gifts/%d/claim", id(2)), p.Token, nil)
	if r := e.do("POST", fmt.Sprintf("/api/admin/gifts/%d/reject", id(2)), sup, map[string]any{"reason": "no username"}); r.Status != 200 {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	g := e.do("GET", "/api/gifts", p.Token, nil).JSON(t)["gifts"].([]any)
	for _, x := range g {
		m := x.(map[string]any)
		if int64(m["id"].(float64)) == id(2) && (m["status"] != "OWNED" || m["rejectReason"] != "no username") {
			t.Fatalf("rejected gift: %v", m)
		}
	}
}

func TestWinnersFeedThreshold(t *testing.T) {
	e := newEnv(t)
	e.gamesConfig()
	e.setSetting(settings.KeyWinnersMin, 1000)
	p := e.login(512, "en", "")
	e.give(p, ledger.Star, 1000000)
	for i := 0; i < 10; i++ {
		e.spin(p, "TIER_2", 10)
	}
	var below int
	_ = e.d.Pool.QueryRow(context.Background(), `SELECT count(*) FROM winners_feed WHERE prize_value < 1000`).Scan(&below)
	if below != 0 || e.countRows("winners_feed") == 0 {
		t.Fatalf("only gifts worth at least the threshold are listed: below=%d total=%d", below, e.countRows("winners_feed"))
	}
}

// ---------------------------------------------------------------- chest

func TestChestRoundPaysOnceDrawsAtPaymentAndResumes(t *testing.T) {
	e := newEnv(t)
	e.gamesConfig()
	p := e.login(520, "en", "")
	if r := e.do("POST", "/api/chest/round", p.Token, nil); r.Code(t) != "INSUFFICIENT_STARS" {
		t.Fatalf("%s", r.Body)
	}
	if e.countRows("chest_rounds") != 0 {
		t.Fatal("a refused round leaves nothing")
	}
	e.give(p, ledger.Star, 100)
	if r := e.do("POST", "/api/chest/pick", p.Token, map[string]any{"slot": 0}); r.Code(t) != "CHEST_NO_ROUND" {
		t.Fatalf("%s", r.Body)
	}
	r := e.do("POST", "/api/chest/round", p.Token, nil)
	if r.Status != 200 || e.stars(p) != 90 {
		t.Fatalf("round: %d %s stars=%d", r.Status, r.Body, e.stars(p))
	}
	st := r.JSON(t)
	if st["round"].(map[string]any)["remaining"].(float64) != 3 || st["roundCostStars"].(float64) != 10 {
		t.Fatalf("%v", st)
	}
	if strings.Contains(string(r.Body), "prizes") {
		t.Fatalf("the hidden prizes must not be sent: %s", r.Body)
	}
	if r := e.do("POST", "/api/chest/round", p.Token, nil); r.Code(t) != "CHEST_ROUND_OPEN" {
		t.Fatalf("one round at a time: %s", r.Body)
	}
	// the prizes exist from the moment of payment
	var stored string
	_ = e.d.Pool.QueryRow(context.Background(), `SELECT prizes::text FROM chest_rounds`).Scan(&stored)
	if strings.Count(stored, `"type"`) != 3 {
		t.Fatalf("three prizes drawn at payment: %s", stored)
	}

	// open one chest, "leave", come back: the round continues
	p0 := e.do("POST", "/api/chest/pick", p.Token, map[string]any{"slot": 1}).JSON(t)
	if p0["remaining"].(float64) != 2 || p0["finished"] != false {
		t.Fatalf("%v", p0)
	}
	again := e.do("POST", "/api/chest/pick", p.Token, map[string]any{"slot": 1}).JSON(t)
	if again["prize"].(map[string]any)["type"] != p0["prize"].(map[string]any)["type"] || again["remaining"].(float64) != 2 {
		t.Fatalf("the same chest shows the same prize: %v vs %v", again, p0)
	}
	state := e.do("GET", "/api/chest", p.Token, nil).JSON(t)["round"].(map[string]any)
	if state["remaining"].(float64) != 2 || len(state["opened"].([]any)) != 1 {
		t.Fatalf("resume: %v", state)
	}
	if r := e.do("POST", "/api/chest/pick", p.Token, map[string]any{"slot": 12}); r.Status < 400 {
		t.Fatalf("a slot beyond the chests on screen: %d", r.Status)
	}
	// any of the chests on screen can be opened, not only the first three
	e.do("POST", "/api/chest/pick", p.Token, map[string]any{"slot": 7})
	last := e.do("POST", "/api/chest/pick", p.Token, map[string]any{"slot": 8}).JSON(t)
	if last["finished"] != true || len(last["results"].([]any)) != 3 {
		t.Fatalf("finished: %v", last)
	}
	// every prize reached the player exactly once
	var wantCoins, wantStars int64
	var gifts int
	for _, x := range last["results"].([]any) {
		m := x.(map[string]any)
		switch m["type"] {
		case "coins":
			wantCoins += int64(m["value"].(float64))
		case "stars":
			wantStars += int64(m["value"].(float64))
		case "gift":
			gifts++
		}
	}
	if e.coins(p) != wantCoins || e.stars(p) != 90+wantStars || e.countRows("player_gifts") != gifts {
		t.Fatalf("prizes: coins=%d/%d stars=%d/%d gifts=%d/%d", e.coins(p), wantCoins, e.stars(p), 90+wantStars, e.countRows("player_gifts"), gifts)
	}
	if e.ledgerSum(p, "COIN") != e.coins(p) || e.ledgerSum(p, "STAR") != e.stars(p) {
		t.Fatal("ledger and balances must agree")
	}
	// a new round can be bought now
	if r := e.do("POST", "/api/chest/round", p.Token, nil); r.Status != 200 {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
}

func TestChestConcurrentPicksOfOneSlotPayOnce(t *testing.T) {
	e := newEnv(t)
	e.gamesConfig()
	e.setSetting(settings.KeyChestPool, []map[string]any{{"type": "coins", "value": 100, "weight": 1}})
	p := e.login(521, "en", "")
	e.give(p, ledger.Star, 100)
	e.do("POST", "/api/chest/round", p.Token, nil)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); e.do("POST", "/api/chest/pick", p.Token, map[string]any{"slot": 0}) }()
	}
	wg.Wait()
	if e.coins(p) != 100 {
		t.Fatalf("one chest = one prize: %d", e.coins(p))
	}
}

func TestChestStaleRoundIsFinishedForThePlayer(t *testing.T) {
	e := newEnv(t)
	e.gamesConfig()
	e.setSetting(settings.KeyChestPool, []map[string]any{{"type": "coins", "value": 100, "weight": 1}})
	e.setSetting(settings.KeyChestExpiryH, 1)
	p := e.login(522, "en", "")
	e.give(p, ledger.Star, 100)
	e.do("POST", "/api/chest/round", p.Token, nil)
	e.do("POST", "/api/chest/pick", p.Token, map[string]any{"slot": 0})
	e.now = e.now.Add(30 * time.Minute)
	if n, _ := e.d.Games.FinishStaleRounds(context.Background()); n != 0 {
		t.Fatalf("not stale yet: %d", n)
	}
	e.now = e.now.Add(2 * time.Hour)
	if n, err := e.d.Games.FinishStaleRounds(context.Background()); err != nil || n != 1 {
		t.Fatalf("stale: %d %v", n, err)
	}
	if e.coins(p) != 300 {
		t.Fatalf("the remaining two chests are opened for the player: coins %d", e.coins(p))
	}
	if n, _ := e.d.Games.FinishStaleRounds(context.Background()); n != 0 || e.coins(p) != 300 {
		t.Fatal("never twice")
	}
}

func TestChestGuardAppliesToRounds(t *testing.T) {
	e := newEnv(t)
	e.gamesConfig()
	e.setSetting(settings.KeyChestPool, []map[string]any{{"type": "gift", "gift": "big", "weight": 100}, {"type": "stars", "value": 1, "weight": 1}})
	e.setSetting(settings.KeyGuardRatio, 2.0)
	p := e.login(523, "en", "")
	e.give(p, ledger.Star, 1000)
	e.do("POST", "/api/chest/round", p.Token, nil)
	var stored string
	_ = e.d.Pool.QueryRow(context.Background(), `SELECT prizes::text FROM chest_rounds`).Scan(&stored)
	if strings.Contains(stored, `"gift":"big"`) {
		t.Fatalf("100,000-star gifts must be stepped down on a 10-star round: %s", stored)
	}
}

// ---------------------------------------------------------------- ride

func (e *env) ride(p player) int64 {
	e.t.Helper()
	r := e.do("POST", "/api/ride/start", p.Token, map[string]any{})
	if r.Status != 200 {
		e.t.Fatalf("ride start: %d %s", r.Status, r.Body)
	}
	return int64(r.JSON(e.t)["runId"].(float64))
}

func (e *env) finishRide(p player, run, coins, score int64) resp {
	return e.do("POST", "/api/ride/finish", p.Token, map[string]any{"runId": run, "coins": coins, "score": score})
}

func TestRideRunPlausibilityAndCaps(t *testing.T) {
	e := newEnv(t)
	e.gamesConfig()
	p := e.login(530, "en", "")
	if r := e.do("POST", "/api/ride/start", p.Token, map[string]any{}); r.Code(t) != "INSUFFICIENT_STARS" {
		t.Fatalf("no stars, no ad: %s", r.Body)
	}
	e.give(p, ledger.Star, 10)

	// a run that pays out far too fast is rejected and flags the account
	id := e.ride(p)
	if e.stars(p) != 9 {
		t.Fatalf("a run costs a star: %d", e.stars(p))
	}
	e.now = e.now.Add(2 * time.Second)
	if r := e.finishRide(p, id, 50, 100); r.Code(t) != "RIDE_RUN_INVALID" {
		t.Fatalf("2 seconds cannot earn 50 coins: %s", r.Body)
	}
	var risk int
	_ = e.d.Pool.QueryRow(context.Background(), `SELECT risk_score FROM players WHERE id=$1`, p.ID).Scan(&risk)
	if risk != 10 || e.coins(p) != 0 {
		t.Fatalf("risk=%d coins=%d", risk, e.coins(p))
	}
	if r := e.finishRide(p, id, 0, 0); r.Code(t) != "RIDE_RUN_INVALID" {
		t.Fatalf("a rejected run cannot be finished again: %s", r.Body)
	}

	// a believable run pays; the same run cannot be paid twice; another player's id is useless
	id = e.ride(p)
	e.now = e.now.Add(60 * time.Second)
	q := e.login(531, "en", "")
	if r := e.finishRide(q, id, 10, 10); r.Code(t) != "RIDE_RUN_INVALID" {
		t.Fatalf("not your run: %s", r.Body)
	}
	if r := e.finishRide(p, id, 60, 900); r.Status != 200 || r.JSON(t)["coinsEarned"].(float64) != 60 || r.JSON(t)["bestScore"].(float64) != 900 || r.JSON(t)["newBest"] != true {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	if r := e.finishRide(p, id, 60, 900); r.Code(t) != "RIDE_RUN_INVALID" || e.coins(p) != 60 {
		t.Fatalf("paid once: coins=%d %s", e.coins(p), r.Body)
	}
	// the daily cap is 100 coins: 60 already, so only 40 more are paid
	id = e.ride(p)
	e.now = e.now.Add(60 * time.Second)
	r := e.finishRide(p, id, 80, 500)
	m := r.JSON(t)
	if m["coinsEarned"].(float64) != 40 || m["capped"] != true || m["bestScore"].(float64) != 900 || m["newBest"] != false || e.coins(p) != 100 {
		t.Fatalf("cap: %v coins=%d", m, e.coins(p))
	}
	id = e.ride(p)
	e.now = e.now.Add(60 * time.Second)
	if m := e.finishRide(p, id, 30, 10).JSON(t); m["coinsEarned"].(float64) != 0 || m["capped"] != true || m["coinsLeftToday"].(float64) != 0 {
		t.Fatalf("after the cap runs pay nothing: %v", m)
	}
	// a new game day resets the cap
	e.nextDay()
	e.d.PlayerTokenTTL = 100000 * time.Hour
	id = e.ride(p)
	e.now = e.now.Add(60 * time.Second)
	if m := e.finishRide(p, id, 30, 10).JSON(t); m["coinsEarned"].(float64) != 30 {
		t.Fatalf("new day: %v", m)
	}
	if e.ledgerSum(p, "COIN") != e.coins(p) {
		t.Fatal("ledger must match")
	}
}

func TestRideRunExpiryAbandonAndScoreLimit(t *testing.T) {
	e := newEnv(t)
	e.gamesConfig()
	p := e.login(532, "en", "")
	e.give(p, ledger.Star, 20)
	old := e.ride(p)
	newer := e.ride(p) // starting again abandons the first run
	e.now = e.now.Add(60 * time.Second)
	if r := e.finishRide(p, old, 5, 5); r.Code(t) != "RIDE_RUN_INVALID" {
		t.Fatalf("an abandoned run: %s", r.Body)
	}
	if r := e.finishRide(p, newer, 5, 60*70+100); r.Code(t) != "RIDE_RUN_INVALID" {
		t.Fatalf("an impossible score: %s", r.Body)
	}
	id := e.ride(p)
	e.now = e.now.Add(16 * time.Minute) // longer than the time a run can be saved
	if r := e.finishRide(p, id, 0, 0); r.Code(t) != "RIDE_RUN_INVALID" {
		t.Fatalf("an expired run: %s", r.Body)
	}
	if r := e.finishRide(p, 999999, 0, 0); r.Code(t) != "RIDE_RUN_INVALID" {
		t.Fatalf("%s", r.Body)
	}
}

func TestRideNeedsConfigAndAdPaysOnlyWhenStarsRunOut(t *testing.T) {
	e := newEnv(t)
	e.gameConfig()
	p := e.login(533, "en", "")
	e.give(p, ledger.Star, 5)
	if r := e.do("POST", "/api/ride/start", p.Token, map[string]any{}); r.Code(t) != "CONFIG_MISSING" {
		t.Fatalf("not configured: %s", r.Body)
	}
	if e.stars(p) != 5 {
		t.Fatal("nothing is charged while the game is not configured")
	}
	e.gamesConfig()
	e.setSetting(settings.KeyAdsRideFree, 1)
	// the ad option exists only when the player cannot pay
	if r := e.do("POST", "/api/ads", p.Token, map[string]any{"placement": "RIDE_FREE"}); r.Code(t) != "AD_LIMIT_REACHED" {
		t.Fatalf("with stars no ad is offered: %s", r.Body)
	}
	if info := e.do("GET", "/api/ride/me", p.Token, nil).JSON(t); info["adOffered"] != false || info["canPayWithStars"] != true {
		t.Fatalf("%v", info)
	}
	e.exec(`UPDATE players SET stars=0 WHERE id=$1`, p.ID)
	if info := e.do("GET", "/api/ride/me", p.Token, nil).JSON(t); info["adOffered"] != true || info["freeRunsLeft"].(float64) != 1 {
		t.Fatalf("%v", info)
	}
	ad := e.watchAd(p, "RIDE_FREE")
	r := e.do("POST", "/api/ride/start", p.Token, map[string]any{"adViewId": ad})
	if r.Status != 200 || r.JSON(t)["paidWith"] != "AD" || e.stars(p) != 0 {
		t.Fatalf("ad run: %d %s", r.Status, r.Body)
	}
	if r := e.do("POST", "/api/ads", p.Token, map[string]any{"placement": "RIDE_FREE"}); r.Code(t) != "AD_LIMIT_REACHED" {
		t.Fatalf("the daily limit of ad runs: %s", r.Body)
	}
}
