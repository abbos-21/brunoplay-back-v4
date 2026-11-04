package api_test

import (
	"context"
	"encoding/json"
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
	e.setSetting(settings.KeyChestCost, 10)
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

// giveGift puts a gift into a player's collection (the games no longer hand gifts out; the collection still exists).
func (e *env) giveGift(p player, key string, value int64) int64 {
	e.t.Helper()
	var id int64
	if err := e.d.Pool.QueryRow(context.Background(), `INSERT INTO player_gifts (player_id, gift_key, star_value, status, source) VALUES ($1,$2,$3,'OWNED','spin') RETURNING id`,
		p.ID, key, value).Scan(&id); err != nil {
		e.t.Fatal(err)
	}
	return id
}

func (e *env) countRows(table string) int {
	var n int
	_ = e.d.Pool.QueryRow(context.Background(), `SELECT count(*) FROM `+table).Scan(&n)
	return n
}

// The wheel has two outcomes: nothing, or the price times the multiplier in stars.
func TestSpinTwoOutcomes(t *testing.T) {
	e := newEnv(t)
	e.gamesConfig()
	p := e.login(500, "ru", "")
	if r := e.do("GET", "/api/spin/tiers", p.Token, nil); r.Status != 200 {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	tiers := e.do("GET", "/api/spin/tiers", p.Token, nil).JSON(t)["tiers"].([]any)
	if len(tiers) != 2 || tiers[0].(map[string]any)["name"] != "Бронза" || tiers[0].(map[string]any)["costStars"].(float64) != 100 || tiers[1].(map[string]any)["name"] != "Silver" {
		t.Fatalf("%v", tiers)
	}
	// each tier shows the win and the empty outcome, the odds hidden until published
	t1 := tiers[0].(map[string]any)["prizes"].([]any)
	if len(t1) != 2 || t1[0].(map[string]any)["type"] != "stars" || t1[0].(map[string]any)["value"].(float64) != 200 || t1[1].(map[string]any)["type"] != "empty" {
		t.Fatalf("tier 1 shows a 200-star win and nothing: %v", t1)
	}
	if t1[0].(map[string]any)["weight"].(float64) != 1 || t1[1].(map[string]any)["weight"].(float64) != 1 {
		t.Fatalf("odds are hidden unless published: %v", t1)
	}
	e.setSetting(settings.KeySpinPublishOdds, true)
	t1 = e.do("GET", "/api/spin/tiers", p.Token, nil).JSON(t)["tiers"].([]any)[0].(map[string]any)["prizes"].([]any)
	if t1[0].(map[string]any)["weight"].(float64) != 45 || t1[1].(map[string]any)["weight"].(float64) != 55 {
		t.Fatalf("published odds are the win chance and its rest: %v", t1)
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
	if e.stars(p) != 0 || e.countRows("spin_draws") != 0 {
		t.Fatal("a refused spin leaves nothing behind")
	}

	// every spin wins: 3 spins cost 300 and pay 3 x 200
	e.setSetting(settings.KeySpinWinChance, 100)
	e.give(p, ledger.Star, 1000)
	res := e.spin(p, "TIER_1", 3).JSON(t)
	if res["spent"].(float64) != 300 || res["totalStarsWon"].(float64) != 600 || len(res["results"].([]any)) != 3 || e.stars(p) != 1300 {
		t.Fatalf("three wins: %v stars=%d", res, e.stars(p))
	}
	for _, x := range res["results"].([]any) {
		if pr := x.(map[string]any); pr["type"] != "stars" || pr["value"].(float64) != 200 {
			t.Fatalf("a win pays the price times two: %v", pr)
		}
	}

	// no spin wins: the price is gone and nothing comes back
	e.setSetting(settings.KeySpinWinChance, 0)
	res = e.spin(p, "TIER_1", 5).JSON(t)
	if res["totalStarsWon"].(float64) != 0 || e.stars(p) != 800 {
		t.Fatalf("five losses: %v stars=%d", res, e.stars(p))
	}
	for _, x := range res["results"].([]any) {
		if pr := x.(map[string]any); pr["type"] != "empty" || pr["value"].(float64) != 0 || pr["label"] != "Пусто" {
			t.Fatalf("a loss is empty: %v", pr)
		}
	}
	if e.countRows("spin_draws") != 8 || e.countRows("player_gifts") != 0 {
		t.Fatalf("eight logged draws and no gifts: %d %d", e.countRows("spin_draws"), e.countRows("player_gifts"))
	}
	if e.ledgerSum(p, "STAR") != e.stars(p) {
		t.Fatalf("ledger %d vs balance %d", e.ledgerSum(p, "STAR"), e.stars(p))
	}
	// the winners feed lists the wins, never the losses
	w := e.do("GET", "/api/spin/winners", p.Token, nil).JSON(t)
	if w["total"].(float64) != 3 || w["data"].([]any)[0].(map[string]any)["username"] != "user500" || w["data"].([]any)[0].(map[string]any)["prizeType"] != "stars" {
		t.Fatalf("winners: %v", w)
	}
}

// The multiplier is an editable setting: 2.5 on a 100-star spin pays 250.
func TestSpinMultiplierAndChanceSettings(t *testing.T) {
	e := newEnv(t)
	e.gamesConfig()
	e.setSetting(settings.KeySpinWinChance, 100)
	e.setSetting(settings.KeySpinWinMult, 2.5)
	p := e.login(501, "en", "")
	e.give(p, ledger.Star, 100)
	res := e.spin(p, "TIER_1", 1).JSON(t)
	if res["totalStarsWon"].(float64) != 250 || e.stars(p) != 250 {
		t.Fatalf("100 stars paid, 250 won: %v stars=%d", res, e.stars(p))
	}
	for _, bad := range []struct {
		key string
		v   any
	}{{settings.KeySpinWinChance, 101}, {settings.KeySpinWinChance, -1}, {settings.KeySpinWinMult, 0.5}, {settings.KeyChestPrizeMult, 0}} {
		if _, err := e.d.Settings.Validate(bad.key, bad.v); err == nil {
			t.Errorf("%s = %v must be refused", bad.key, bad.v)
		}
	}
}

// Over many spins the win chance is what the setting says.
func TestSpinWinChanceIsRespected(t *testing.T) {
	e := newEnv(t)
	e.gamesConfig()
	e.setSetting(settings.KeySpinWinChance, 40)
	p := e.login(502, "en", "")
	e.give(p, ledger.Star, 1000000)
	for i := 0; i < 150; i++ {
		if r := e.spin(p, "TIER_1", 10); r.Status != 200 {
			t.Fatalf("%d %s", r.Status, r.Body)
		}
	}
	var wins, all int
	_ = e.d.Pool.QueryRow(context.Background(), `SELECT count(*) FILTER (WHERE gift_key LIKE 'stars:%'), count(*) FROM spin_draws`).Scan(&wins, &all)
	frac := float64(wins) / float64(all)
	if all != 1500 || frac < 0.35 || frac > 0.45 {
		t.Fatalf("40%% over 1500 draws: wins=%d (%.3f)", wins, frac)
	}
}

// The payout guard turns a win into a loss while the tier's payout ratio would go over the limit.
func TestPayoutGuardTurnsWinsIntoLosses(t *testing.T) {
	e := newEnv(t)
	e.gamesConfig()
	e.setSetting(settings.KeySpinWinChance, 100)
	p := e.login(503, "en", "")
	e.give(p, ledger.Star, 1000000)

	// guard off: every win is paid
	if r := e.spin(p, "TIER_1", 1); r.JSON(t)["totalStarsWon"].(float64) != 200 {
		t.Fatalf("guard off pays the win: %s", r.Body)
	}
	if e.countRows("guard_events") != 0 {
		t.Fatal("no events with the guard off")
	}
	// guard on at 50 %, acting from the first spin: 200 won against 100 spent is far above
	e.setSetting(settings.KeyGuardRatio, 0.5)
	e.setSetting(settings.KeyGuardMinVolume, 0)
	r := e.spin(p, "TIER_2", 1)
	if got := r.JSON(t)["results"].([]any)[0].(map[string]any)["type"]; got != "empty" {
		t.Fatalf("the guard must turn the win into a loss, got %v", got)
	}
	var from, to string
	if err := e.d.Pool.QueryRow(context.Background(), `SELECT from_key, to_key FROM guard_events ORDER BY id DESC LIMIT 1`).Scan(&from, &to); err != nil {
		t.Fatal("every intervention is logged: ", err)
	}
	if from != "stars:1000" || to != "empty:0" {
		t.Fatalf("event: %s -> %s", from, to)
	}
	// the draw log keeps what the dice gave and what was paid
	var drawn, paid string
	_ = e.d.Pool.QueryRow(context.Background(), `SELECT drawn_key, gift_key FROM spin_draws ORDER BY id DESC LIMIT 1`).Scan(&drawn, &paid)
	if drawn != "stars:1000" || paid != "empty" {
		t.Fatalf("draw log: drawn=%s paid=%s", drawn, paid)
	}
	// the admin sees the live ratio and the interventions
	fin := e.adminLogin("root@example.com", admin.RoleSuper)
	g := e.do("GET", "/api/admin/games/guard", fin, nil).JSON(t)
	if len(g["stats"].([]any)) == 0 || len(g["events"].([]any)) != 1 {
		t.Fatalf("%v", g)
	}
}

// A quiet game is not blocked by one early win: the guard waits for some volume (guard.min_volume_multiple).
func TestGuardWaitsForVolumeAndKeepsTheRatioUnderTheLimit(t *testing.T) {
	e := newEnv(t)
	e.gamesConfig()
	e.setSetting(settings.KeySpinWinChance, 100)
	e.setSetting(settings.KeyGuardRatio, 1.2) // pay out at most 120% of what was spent
	p := e.login(504, "en", "")
	e.give(p, ledger.Star, 10000000)

	// the default minimum is 10 times the win: 100 spent is far below 10 x 200, so the first win goes out
	if r := e.spin(p, "TIER_1", 1); r.JSON(t)["totalStarsWon"].(float64) != 200 || e.countRows("guard_events") != 0 {
		t.Fatalf("the first win of a quiet game is paid: %s", r.Body)
	}
	// with volume, the ratio never runs above the limit even though every spin would win
	for i := 0; i < 40; i++ {
		e.spin(p, "TIER_1", 10)
	}
	var spent, paidOut int64
	_ = e.d.Pool.QueryRow(context.Background(), `SELECT sum(spent), sum(paid) FROM payout_stats WHERE game='spin' AND tier='TIER_1'`).Scan(&spent, &paidOut)
	if float64(paidOut)/float64(spent) > 1.2+200.0/float64(spent) {
		t.Fatalf("payout ratio %.3f runs away above the guard limit", float64(paidOut)/float64(spent))
	}
	if paidOut == 0 || e.countRows("guard_events") == 0 {
		t.Fatal("wins still go out, and the guard did step in")
	}
}

func TestGiftSellClaimDeliverReject(t *testing.T) {
	e := newEnv(t)
	e.gamesConfig()
	p, q := e.login(510, "en", ""), e.login(511, "en", "")
	e.give(p, ledger.Star, 1000)
	e.giveGift(p, "small", 100)
	e.giveGift(p, "mid", 1000)
	e.giveGift(p, "small", 100)
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
	e.setSetting(settings.KeySpinWinChance, 100)
	p := e.login(512, "en", "")
	e.give(p, ledger.Star, 1000000)
	for i := 0; i < 5; i++ {
		e.spin(p, "TIER_1", 10) // wins of 200
		e.spin(p, "TIER_2", 10) // wins of 1,000
	}
	var below int
	_ = e.d.Pool.QueryRow(context.Background(), `SELECT count(*) FROM winners_feed WHERE prize_value < 1000`).Scan(&below)
	if below != 0 || e.countRows("winners_feed") != 50 {
		t.Fatalf("only wins worth at least the threshold are listed: below=%d total=%d", below, e.countRows("winners_feed"))
	}
}

// ---------------------------------------------------------------- chest

// prizeSlotOf reads the open round's hidden chests and returns the slot of the prize (-1 when all three are empty).
func (e *env) prizeSlotOf(t *testing.T) (int, string) {
	t.Helper()
	var stored string
	if err := e.d.Pool.QueryRow(context.Background(), `SELECT prizes::text FROM chest_rounds WHERE status='OPEN'`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	var list []struct {
		Type  string `json:"type"`
		Value int64  `json:"value"`
	}
	if err := json.Unmarshal([]byte(stored), &list); err != nil {
		t.Fatal(err)
	}
	if len(list) != 3 {
		t.Fatalf("three chests: %s", stored)
	}
	slot := -1
	for i, x := range list {
		if x.Type == "stars" {
			if slot >= 0 {
				t.Fatalf("only one chest holds the prize: %s", stored)
			}
			slot = i
		}
	}
	return slot, stored
}

func TestChestRoundHidesOnePrizeInThreeChests(t *testing.T) {
	e := newEnv(t)
	e.gamesConfig()
	e.setSetting(settings.KeyChestPrizeMult, 3) // 10 stars -> 30
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
	if st["round"].(map[string]any)["remaining"].(float64) != 1 || st["roundCostStars"].(float64) != 10 || st["winStars"].(float64) != 30 || st["picksPerRound"].(float64) != 1 {
		t.Fatalf("%v", st)
	}
	if strings.Contains(string(r.Body), "prizes") {
		t.Fatalf("the hidden prizes must not be sent: %s", r.Body)
	}
	if r := e.do("POST", "/api/chest/round", p.Token, nil); r.Code(t) != "CHEST_ROUND_OPEN" {
		t.Fatalf("one round at a time: %s", r.Body)
	}
	// the prize is in exactly one of the three chests from the moment of payment
	prizeSlot, _ := e.prizeSlotOf(t)
	if prizeSlot < 0 {
		t.Fatal("with the guard off the prize is always in one chest")
	}
	if r := e.do("POST", "/api/chest/pick", p.Token, map[string]any{"slot": 3}); r.Status < 400 {
		t.Fatalf("there are only three chests: %d", r.Status)
	}

	// open the prize chest: 30 stars, the round is over, the others are revealed
	got := e.do("POST", "/api/chest/pick", p.Token, map[string]any{"slot": prizeSlot}).JSON(t)
	if got["finished"] != true || got["prize"].(map[string]any)["type"] != "stars" || got["prize"].(map[string]any)["value"].(float64) != 30 || got["totalStarsWon"].(float64) != 30 {
		t.Fatalf("the prize chest: %v", got)
	}
	reveal := got["reveal"].([]any)
	if len(reveal) != 3 || reveal[prizeSlot].(map[string]any)["type"] != "stars" {
		t.Fatalf("the app learns where the prize was: %v", reveal)
	}
	if e.stars(p) != 120 || e.ledgerSum(p, "STAR") != e.stars(p) {
		t.Fatalf("90 + 30: %d (ledger %d)", e.stars(p), e.ledgerSum(p, "STAR"))
	}
	if r := e.do("POST", "/api/chest/pick", p.Token, map[string]any{"slot": prizeSlot}); r.Code(t) != "CHEST_NO_ROUND" {
		t.Fatalf("a finished round cannot be opened again: %s", r.Body)
	}

	// a new round; this time open an empty chest: nothing is paid
	e.do("POST", "/api/chest/round", p.Token, nil)
	slot, _ := e.prizeSlotOf(t)
	empty := (slot + 1) % 3
	got = e.do("POST", "/api/chest/pick", p.Token, map[string]any{"slot": empty}).JSON(t)
	if got["finished"] != true || got["prize"].(map[string]any)["type"] != "empty" || got["prize"].(map[string]any)["label"] != "Empty" {
		t.Fatalf("an empty chest: %v", got)
	}
	if e.stars(p) != 110 || e.ledgerSum(p, "STAR") != e.stars(p) {
		t.Fatalf("120 - 10, nothing back: %d", e.stars(p))
	}
}

// One round in three is a win for a player who opens always the same chest: the chance is 1 in 3.
func TestChestOneInThree(t *testing.T) {
	e := newEnv(t)
	e.gamesConfig()
	p := e.login(524, "en", "")
	e.give(p, ledger.Star, 100000)
	wins := 0
	for i := 0; i < 300; i++ {
		if r := e.do("POST", "/api/chest/round", p.Token, nil); r.Status != 200 {
			t.Fatalf("%d %s", r.Status, r.Body)
		}
		if e.do("POST", "/api/chest/pick", p.Token, map[string]any{"slot": 0}).JSON(t)["prize"].(map[string]any)["type"] == "stars" {
			wins++
		}
	}
	if wins < 70 || wins > 130 {
		t.Fatalf("one chest in three over 300 rounds: %d wins", wins)
	}
	// the prize is the price times 2.7 (rounded)
	if want := int64(wins)*27 + (100000 - 3000); e.stars(p) != want {
		t.Fatalf("stars %d, want %d", e.stars(p), want)
	}
}

func TestChestConcurrentPicksOfOneSlotPayOnce(t *testing.T) {
	e := newEnv(t)
	e.gamesConfig()
	e.setSetting(settings.KeyChestPrizeMult, 3)
	p := e.login(521, "en", "")
	e.give(p, ledger.Star, 100)
	e.do("POST", "/api/chest/round", p.Token, nil)
	slot, _ := e.prizeSlotOf(t)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); e.do("POST", "/api/chest/pick", p.Token, map[string]any{"slot": slot}) }()
	}
	wg.Wait()
	if e.stars(p) != 90+30 {
		t.Fatalf("one chest = one prize: %d", e.stars(p))
	}
}

// A round nobody finishes is opened by the worker: one chest, chosen at random, so a player never loses what they paid.
func TestChestStaleRoundIsFinishedForThePlayer(t *testing.T) {
	e := newEnv(t)
	e.gamesConfig()
	e.setSetting(settings.KeyChestExpiryH, 1)
	p := e.login(522, "en", "")
	e.give(p, ledger.Star, 100)
	e.do("POST", "/api/chest/round", p.Token, nil)
	e.now = e.now.Add(30 * time.Minute)
	if n, _ := e.d.Games.FinishStaleRounds(context.Background()); n != 0 {
		t.Fatalf("not stale yet: %d", n)
	}
	e.now = e.now.Add(2 * time.Hour)
	if n, err := e.d.Games.FinishStaleRounds(context.Background()); err != nil || n != 1 {
		t.Fatalf("stale: %d %v", n, err)
	}
	if s := e.stars(p); s != 90 && s != 90+27 {
		t.Fatalf("a random chest was opened for the player: stars %d", s)
	}
	if e.countRows("chest_rounds WHERE status='OPEN'") != 0 {
		t.Fatal("the round is over")
	}
	after := e.stars(p)
	if n, _ := e.d.Games.FinishStaleRounds(context.Background()); n != 0 || e.stars(p) != after {
		t.Fatal("never twice")
	}
}

// Rounds paid in the old version (several picks, a prize pool) are opened in full at once, with the prizes they already carry.
func TestChestOldRoundsAreFinishedAtOnce(t *testing.T) {
	e := newEnv(t)
	e.gamesConfig()
	p := e.login(525, "en", "")
	if _, err := e.d.Pool.Exec(context.Background(), `INSERT INTO chest_rounds (player_id, status, cost, picks, prizes, opened, slot_mode, created_at)
		VALUES ($1,'OPEN',10,3,'[{"type":"coins","value":100},{"type":"stars","value":5},{"type":"coins","value":100}]','{"2":0}',FALSE,$2)`, p.ID, e.now); err != nil {
		t.Fatal(err)
	}
	// the first prize was opened already and paid then; the worker opens the other two (the expiry setting is 0 = never)
	if n, err := e.d.Games.FinishStaleRounds(context.Background()); err != nil || n != 1 {
		t.Fatalf("old round: %d %v", n, err)
	}
	if e.coins(p) != 100 || e.stars(p) != 5 {
		t.Fatalf("the two unopened prizes: coins %d stars %d", e.coins(p), e.stars(p))
	}
	if n, _ := e.d.Games.FinishStaleRounds(context.Background()); n != 0 {
		t.Fatal("never twice")
	}
}

func TestChestGuardTakesThePrizeOutOfTheChests(t *testing.T) {
	e := newEnv(t)
	e.gamesConfig()
	e.setSetting(settings.KeyGuardRatio, 0.5)
	e.setSetting(settings.KeyGuardMinVolume, 0)
	p := e.login(523, "en", "")
	e.give(p, ledger.Star, 1000)
	e.do("POST", "/api/chest/round", p.Token, nil)
	slot, stored := e.prizeSlotOf(t)
	if slot >= 0 {
		t.Fatalf("27 stars on a 10-star round is far above the limit: %s", stored)
	}
	if e.countRows("guard_events") != 1 {
		t.Fatal("the intervention is logged")
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
