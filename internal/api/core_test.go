package api_test

import (
	"context"
	"fmt"
	"math"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"brunoplay/internal/ads"
	"brunoplay/internal/ledger"
	"brunoplay/internal/settings"
)

// gameConfig fills the settings the owner will supply later, with simple test numbers:
// mining rate 0.025 * level coins/s, capacity 5 * level coins, upgrades cost 100*level / 50*level coins.
func (e *env) gameConfig() {
	e.d.Cfg.AdsWebhookSec = "adsecret"
	e.d.Ads = ads.New(e.d.Pool, e.d.Settings, "adsecret")
	e.d.Ads.Now = func() time.Time { return e.now }
	e.d.Fishing.SetAds(e.d.Ads)

	mining := []map[string]any{}
	capacity := []map[string]any{}
	for l := 1; l <= 10; l++ {
		mining = append(mining, map[string]any{"level": l, "value": 0.025 * float64(l), "cost": 100 * l, "currency": "COIN"})
		capacity = append(capacity, map[string]any{"level": l, "value": 5 * float64(l), "cost": 50 * l, "currency": "COIN"})
	}
	e.setSetting(settings.KeyCardsMining, mining)
	e.setSetting(settings.KeyCardsCapacity, capacity)
	e.setSetting(settings.KeyAdsBlockIDs, map[string]string{"FISHING_START": "blk-start", "COLLECT": "blk-collect", "BONUS_COINS": "blk-bonus",
		"DOUBLE_STREAK": "b", "DOUBLE_COMBO": "b", "COMBO_EXTRA": "b", "RIDE_FREE": "b", "TASK_ADS": "b"})
	e.setSetting(settings.KeyBoosterAutoCost, 500)
	e.setSetting(settings.KeyBoosterMineCost, 100)
}

// watchAd requests an ad proof and completes it through Adsgram's callback; returns the proof id.
func (e *env) watchAd(p player, placement string) string {
	e.t.Helper()
	r := e.do("POST", "/api/ads", p.Token, map[string]any{"placement": placement})
	if r.Status != 200 {
		e.t.Fatalf("ads request: %d %s", r.Status, r.Body)
	}
	m := r.JSON(e.t)
	id := m["adViewId"].(string)
	q := url.Values{"userid": {fmt.Sprint(p.TG)}, "blockid": {m["blockId"].(string)}, "secret": {"adsecret"}}
	w := e.do("GET", "/api/webhooks/adsgram?"+q.Encode(), "", nil)
	if w.Status != 200 || w.JSON(e.t)["ok"] != true {
		e.t.Fatalf("webhook: %d %s", w.Status, w.Body)
	}
	return id
}

func (e *env) coins(p player) int64 {
	var c int64
	if err := e.d.Pool.QueryRow(context.Background(), `SELECT coins FROM players WHERE id=$1`, p.ID).Scan(&c); err != nil {
		e.t.Fatal(err)
	}
	return c
}

func (e *env) stars(p player) int64 {
	var c int64
	_ = e.d.Pool.QueryRow(context.Background(), `SELECT stars FROM players WHERE id=$1`, p.ID).Scan(&c)
	return c
}

func (e *env) give(p player, cur ledger.Currency, amount int64) {
	e.t.Helper()
	col := "coins"
	if cur == ledger.Star {
		col = "stars"
	}
	e.exec(`UPDATE players SET `+col+`=`+col+`+$2 WHERE id=$1`, p.ID, amount)
	e.exec(`INSERT INTO ledger_entries (player_id, currency, amount, balance_after, reason) VALUES ($1,$2,$3,(SELECT `+col+` FROM players WHERE id=$1),'test_seed')`,
		p.ID, string(cur), amount)
}

func (e *env) fishing(p player) map[string]any {
	e.t.Helper()
	r := e.do("GET", "/api/fishing", p.Token, nil)
	if r.Status != 200 {
		e.t.Fatalf("fishing state: %d %s", r.Status, r.Body)
	}
	return r.JSON(e.t)
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-6 }

func TestUnconfiguredFishingSaysSo(t *testing.T) {
	e := newEnv(t)
	p := e.login(100, "en", "")
	r := e.do("GET", "/api/fishing", p.Token, nil)
	if r.Status != 503 || r.Code(t) != "CONFIG_MISSING" {
		t.Fatalf("before the owner sets the card tables: %d %s", r.Status, r.Body)
	}
}

func TestFishingFullCycleWithAds(t *testing.T) {
	e := newEnv(t)
	e.gameConfig()
	p := e.login(101, "en", "")

	st := e.fishing(p)
	if st["isFishing"] != false || !near(st["tankCapacity"].(float64), 5) || !near(st["miningRate"].(float64), 0.025) {
		t.Fatalf("idle state: %v", st)
	}

	// no proof, and a proof that was requested but not watched, both refuse
	if r := e.do("POST", "/api/fishing/start", p.Token, map[string]any{}); r.Code(t) != "AD_NOT_COMPLETED" {
		t.Fatalf("start without ad: %s", r.Body)
	}
	pending := e.do("POST", "/api/ads", p.Token, map[string]any{"placement": "FISHING_START"}).JSON(t)["adViewId"].(string)
	if r := e.do("POST", "/api/fishing/start", p.Token, map[string]any{"adViewId": pending}); r.Code(t) != "AD_NOT_COMPLETED" {
		t.Fatalf("start with an unwatched ad: %s", r.Body)
	}
	// a proof for another placement cannot start fishing
	wrong := e.watchAd(p, "COLLECT")
	if r := e.do("POST", "/api/fishing/start", p.Token, map[string]any{"adViewId": wrong}); r.Code(t) != "AD_NOT_COMPLETED" {
		t.Fatalf("collect proof must not start fishing: %s", r.Body)
	}

	startAd := e.watchAd(p, "FISHING_START")
	t0 := e.now
	if r := e.do("POST", "/api/fishing/start", p.Token, map[string]any{"adViewId": startAd}); r.Status != 200 {
		t.Fatalf("start: %d %s", r.Status, r.Body)
	}
	// the same request again (retry) is accepted without starting a second session
	if r := e.do("POST", "/api/fishing/start", p.Token, map[string]any{"adViewId": startAd}); r.Status != 200 {
		t.Fatalf("retried start: %d %s", r.Status, r.Body)
	}
	other := e.watchAd(p, "FISHING_START")
	if r := e.do("POST", "/api/fishing/start", p.Token, map[string]any{"adViewId": other}); r.Code(t) != "SESSION_ALREADY_RUNNING" {
		t.Fatalf("second start: %s", r.Body)
	}
	var n int
	_ = e.d.Pool.QueryRow(context.Background(), `SELECT count(*) FROM fishing_sessions WHERE player_id=$1`, p.ID).Scan(&n)
	if n != 1 {
		t.Fatalf("one session expected, got %d", n)
	}

	// 10 s: 0.25 coins, below the 10% rule (and below one coin)
	e.now = t0.Add(10 * time.Second)
	collectAd := e.watchAd(p, "COLLECT")
	r := e.do("POST", "/api/fishing/collect", p.Token, map[string]any{"adViewId": collectAd})
	if r.Code(t) != "TANK_BELOW_MINIMUM" {
		t.Fatalf("collect at 10s: %s", r.Body)
	}
	st = e.fishing(p)
	if st["canCollect"] != false || !near(st["tankCoins"].(float64), 0.25) {
		t.Fatalf("state at 10s: %v", st)
	}
	// the refused attempt must not burn the ad proof
	e.now = t0.Add(100 * time.Second)
	st = e.fishing(p)
	if st["canCollect"] != true || !near(st["tankCoins"].(float64), 2.5) {
		t.Fatalf("state at 100s: %v", st)
	}
	if r := e.do("POST", "/api/fishing/collect", p.Token, map[string]any{}); r.Code(t) != "AD_NOT_COMPLETED" {
		t.Fatalf("collect without ad: %s", r.Body)
	}
	r = e.do("POST", "/api/fishing/collect", p.Token, map[string]any{"adViewId": collectAd})
	if r.Status != 200 {
		t.Fatalf("collect: %d %s", r.Status, r.Body)
	}
	m := r.JSON(t)
	if m["coinsCollected"].(float64) != 2 || m["newBalance"].(float64) != 2 {
		t.Fatalf("2.5 coins collect as 2 whole coins: %v", m)
	}
	// retrying the same collect never pays twice
	r = e.do("POST", "/api/fishing/collect", p.Token, map[string]any{"adViewId": collectAd})
	if r.Status != 200 || e.coins(p) != 2 {
		t.Fatalf("retried collect: %d %s coins=%d", r.Status, r.Body, e.coins(p))
	}
	if st = e.fishing(p); st["isFishing"] != false {
		t.Fatalf("collect ends the session: %v", st)
	}
	// a used proof cannot start the next session
	if r := e.do("POST", "/api/fishing/start", p.Token, map[string]any{"adViewId": collectAd}); r.Code(t) != "AD_NOT_COMPLETED" {
		t.Fatalf("collect proof for start: %s", r.Body)
	}
	// ledger agrees with the balance
	var sum int64
	_ = e.d.Pool.QueryRow(context.Background(), `SELECT COALESCE(sum(amount),0) FROM ledger_entries WHERE player_id=$1 AND currency='COIN'`, p.ID).Scan(&sum)
	if sum != 2 {
		t.Fatalf("ledger sum %d", sum)
	}
}

func TestTankStopsWhenFullAndAwayMining(t *testing.T) {
	e := newEnv(t)
	e.gameConfig()
	p := e.login(102, "en", "")
	e.do("POST", "/api/fishing/start", p.Token, map[string]any{"adViewId": e.watchAd(p, "FISHING_START")})
	e.now = e.now.Add(48 * time.Hour) // the player is away for two days
	st := e.fishing(p)
	if !near(st["tankCoins"].(float64), 5) || st["isComplete"] != true {
		t.Fatalf("a full tank never overfills: %v", st)
	}
	r := e.do("POST", "/api/fishing/collect", p.Token, map[string]any{"adViewId": e.watchAd(p, "COLLECT")})
	if r.JSON(t)["coinsCollected"].(float64) != 5 {
		t.Fatalf("%s", r.Body)
	}
}

func TestAdExpiryAndBadWebhook(t *testing.T) {
	e := newEnv(t)
	e.gameConfig()
	p := e.login(103, "en", "")
	ad := e.watchAd(p, "FISHING_START")
	e.now = e.now.Add(6 * time.Minute) // default proof lifetime is 5 minutes
	if r := e.do("POST", "/api/fishing/start", p.Token, map[string]any{"adViewId": ad}); r.Code(t) != "AD_EXPIRED" {
		t.Fatalf("expired proof: %s", r.Body)
	}
	if r := e.do("GET", "/api/webhooks/adsgram?userid=103&secret=wrong", "", nil); r.Status != 401 {
		t.Fatalf("bad secret: %d", r.Status)
	}
	if r := e.do("GET", "/api/webhooks/adsgram?userid=103", "", nil); r.Status != 401 {
		t.Fatalf("no secret: %d", r.Status)
	}
	r := e.do("GET", "/api/webhooks/adsgram?userid=999999&secret=adsecret", "", nil)
	if r.Status != 200 || r.JSON(t)["ok"] != false {
		t.Fatalf("unknown user is answered with ok=false: %d %s", r.Status, r.Body)
	}
}

func TestAnotherPlayersProofIsUseless(t *testing.T) {
	e := newEnv(t)
	e.gameConfig()
	a, b := e.login(104, "en", ""), e.login(105, "en", "")
	ad := e.watchAd(a, "FISHING_START")
	if r := e.do("POST", "/api/fishing/start", b.Token, map[string]any{"adViewId": ad}); r.Code(t) != "AD_NOT_COMPLETED" {
		t.Fatalf("someone else's proof: %s", r.Body)
	}
	if r := e.do("POST", "/api/fishing/start", a.Token, map[string]any{"adViewId": ad}); r.Status != 200 {
		t.Fatalf("the owner can still use it: %d %s", r.Status, r.Body)
	}
}

func TestNoFillFallback(t *testing.T) {
	e := newEnv(t)
	e.gameConfig()
	p := e.login(106, "en", "")
	for i := 1; i <= 2; i++ {
		r := e.do("POST", "/api/ads/failed", p.Token, map[string]any{"placement": "FISHING_START"})
		if r.JSON(t)["fallback"] != false {
			t.Fatalf("failure %d is not enough: %s", i, r.Body)
		}
	}
	r := e.do("POST", "/api/ads/failed", p.Token, map[string]any{"placement": "FISHING_START"})
	m := r.JSON(t)
	if m["fallback"] != true {
		t.Fatalf("third failure opens the fallback: %s", r.Body)
	}
	id := m["ad"].(map[string]any)["adViewId"].(string)
	if r := e.do("POST", "/api/fishing/start", p.Token, map[string]any{"adViewId": id}); r.Status != 200 {
		t.Fatalf("fallback proof starts fishing: %d %s", r.Status, r.Body)
	}
	// reward placements never fall back: no ad, no reward
	for i := 0; i < 5; i++ {
		r = e.do("POST", "/api/ads/failed", p.Token, map[string]any{"placement": "BONUS_COINS"})
		if r.JSON(t)["fallback"] != false {
			t.Fatal("bonus ads must not fall back")
		}
	}
}

func TestAdsOptionalWhenSwitchedOff(t *testing.T) {
	e := newEnv(t)
	e.gameConfig()
	e.setSetting(settings.KeyAdsRequired, false)
	p := e.login(107, "en", "")
	if r := e.do("POST", "/api/fishing/start", p.Token, map[string]any{}); r.Status != 200 {
		t.Fatalf("ads off: %d %s", r.Status, r.Body)
	}
	e.now = e.now.Add(time.Hour)
	if r := e.do("POST", "/api/fishing/collect", p.Token, map[string]any{}); r.Status != 200 {
		t.Fatalf("ads off collect: %d %s", r.Status, r.Body)
	}
}

func TestPlacementSwitchAndMissingBlockIDs(t *testing.T) {
	e := newEnv(t)
	e.gameConfig()
	p := e.login(108, "en", "")
	e.setSetting(settings.KeyAdsPlacements, map[string]bool{"BONUS_COINS": false})
	if r := e.do("POST", "/api/ads", p.Token, map[string]any{"placement": "BONUS_COINS"}); r.Code(t) != "AD_PLACEMENT_DISABLED" {
		t.Fatalf("%s", r.Body)
	}
	if r := e.do("POST", "/api/ads", p.Token, map[string]any{"placement": "NOPE"}); r.Status != 422 {
		t.Fatalf("%d", r.Status)
	}
	e.setSetting(settings.KeyAdsBlockIDs, map[string]string{})
	if r := e.do("POST", "/api/ads", p.Token, map[string]any{"placement": "FISHING_START"}); r.Code(t) != "CONFIG_MISSING" {
		t.Fatalf("no block id while ads are required: %s", r.Body)
	}
}

func TestCardUpgradeRules(t *testing.T) {
	e := newEnv(t)
	e.gameConfig()
	p := e.login(110, "ru", "")

	r := e.do("POST", "/api/shop/cards/MINING_RATE/upgrade", p.Token, nil)
	if r.Code(t) != "INSUFFICIENT_COINS" {
		t.Fatalf("no coins: %s", r.Body)
	}
	if m := r.JSON(t)["error"].(map[string]any)["message"]; m != "Недостаточно монет" {
		t.Fatalf("message in the player's language: %v", m)
	}
	e.give(p, ledger.Coin, 1000)

	cards := e.do("GET", "/api/shop/cards", p.Token, nil).JSON(t)["cards"].([]any)
	mining := cards[0].(map[string]any)
	if mining["cardName"] != "MINING_RATE" || mining["canUpgrade"] != true || mining["upgradeCost"].(float64) != 200 {
		t.Fatalf("card list: %v", mining)
	}

	// mining to level 2 does not raise the player level (the lower card decides)
	up := e.do("POST", "/api/shop/cards/MINING_RATE/upgrade", p.Token, nil).JSON(t)
	if up["newLevel"].(float64) != 2 || up["newUserLevel"].(float64) != 1 || up["levelUp"] != false || e.coins(p) != 800 {
		t.Fatalf("mining upgrade: %v coins=%d", up, e.coins(p))
	}
	// capacity to level 2 lifts the player to level 2, with the reward configured for it
	e.setSetting(settings.KeyLevelRewards, []map[string]any{{"level": 2, "coins": 200, "stars": 3}})
	up = e.do("POST", "/api/shop/cards/COIN_CAPACITY/upgrade", p.Token, nil).JSON(t)
	if up["newUserLevel"].(float64) != 2 || up["levelUp"] != true || up["rewardCoins"].(float64) != 200 || up["rewardStars"].(float64) != 3 {
		t.Fatalf("capacity upgrade: %v", up)
	}
	if e.coins(p) != 800-100+200 || e.stars(p) != 3 {
		t.Fatalf("coins=%d stars=%d", e.coins(p), e.stars(p))
	}
	// the level reward is paid once per level, ever
	var n int
	_ = e.d.Pool.QueryRow(context.Background(), `SELECT count(*) FROM ledger_entries WHERE player_id=$1 AND reason='level_reward'`, p.ID).Scan(&n)
	if n != 2 {
		t.Fatalf("level reward rows %d (coins + stars)", n)
	}
	if r := e.do("POST", "/api/shop/cards/NOPE/upgrade", p.Token, nil); r.Status != 404 {
		t.Fatalf("%d", r.Status)
	}
}

func TestCardMaxLevelAndStarCostAndRequirements(t *testing.T) {
	e := newEnv(t)
	e.gameConfig()
	e.setSetting(settings.KeyCardsMining, []map[string]any{
		{"level": 1, "value": 0.025, "cost": 0, "currency": "COIN"},
		{"level": 2, "value": 0.03, "cost": 5, "currency": "STAR"},
		{"level": 3, "value": 0.04, "cost": 10, "currency": "COIN", "requirements": []map[string]any{
			{"type": "invite_friends", "value": 1, "desc_en": "Invite a friend", "desc_ru": "Пригласите друга"},
			{"type": "other_card_level", "value": 2}}},
	})
	p := e.login(111, "ru", "")
	e.give(p, ledger.Coin, 100)
	if r := e.do("POST", "/api/shop/cards/MINING_RATE/upgrade", p.Token, nil); r.Code(t) != "INSUFFICIENT_STARS" {
		t.Fatalf("level 2 costs stars: %s", r.Body)
	}
	e.give(p, ledger.Star, 5)
	if r := e.do("POST", "/api/shop/cards/MINING_RATE/upgrade", p.Token, nil); r.Status != 200 || e.stars(p) != 0 || e.coins(p) != 100 {
		t.Fatalf("star upgrade: %d %s stars=%d coins=%d", r.Status, r.Body, e.stars(p), e.coins(p))
	}
	// level 3 has two requirements
	cards := e.do("GET", "/api/shop/cards", p.Token, nil).JSON(t)["cards"].([]any)
	reqs := cards[0].(map[string]any)["requirements"].([]any)
	if len(reqs) != 2 || reqs[0].(map[string]any)["met"] != false || reqs[0].(map[string]any)["description"] != "Пригласите друга" ||
		cards[0].(map[string]any)["canUpgrade"] != false {
		t.Fatalf("requirements: %v", reqs)
	}
	if r := e.do("POST", "/api/shop/cards/MINING_RATE/upgrade", p.Token, nil); r.Code(t) != "REQUIREMENT_NOT_MET" {
		t.Fatalf("%s", r.Body)
	}
	// meet them: a counted friend and the other card at level 2
	f := e.login(112, "en", fmt.Sprintf("ref_%d", p.ID))
	e.exec(`UPDATE players SET referral_counted=TRUE WHERE id=$1`, f.ID)
	e.exec(`UPDATE players SET capacity_level=2 WHERE id=$1`, p.ID)
	if r := e.do("POST", "/api/shop/cards/MINING_RATE/upgrade", p.Token, nil); r.Status != 200 {
		t.Fatalf("requirements met: %d %s", r.Status, r.Body)
	}
	if r := e.do("POST", "/api/shop/cards/MINING_RATE/upgrade", p.Token, nil); r.Code(t) != "CARD_MAX_LEVEL" {
		t.Fatalf("table ends at level 3: %s", r.Body)
	}
}

func TestConcurrentUpgradesPayOnce(t *testing.T) {
	e := newEnv(t)
	e.gameConfig()
	p := e.login(113, "en", "")
	e.give(p, ledger.Coin, 200) // exactly one upgrade of the mining card
	var ok, refused int64
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := e.do("POST", "/api/shop/cards/MINING_RATE/upgrade", p.Token, nil)
			if r.Status == 200 {
				atomic.AddInt64(&ok, 1)
			} else if c := r.Code(t); c == "INSUFFICIENT_COINS" {
				atomic.AddInt64(&refused, 1)
			}
		}()
	}
	wg.Wait()
	var lvl int
	_ = e.d.Pool.QueryRow(context.Background(), `SELECT mining_level FROM players WHERE id=$1`, p.ID).Scan(&lvl)
	if ok != 1 || lvl != 2 || e.coins(p) != 0 {
		t.Fatalf("ok=%d refused=%d level=%d coins=%d", ok, refused, lvl, e.coins(p))
	}
}

func TestUpgradeDuringSessionKeepsMinedCoins(t *testing.T) {
	e := newEnv(t)
	e.gameConfig()
	p := e.login(114, "en", "")
	e.give(p, ledger.Coin, 1000)
	e.do("POST", "/api/fishing/start", p.Token, map[string]any{"adViewId": e.watchAd(p, "FISHING_START")})
	t0 := e.now
	e.now = t0.Add(60 * time.Second) // 1.5 coins at 0.025/s
	if r := e.do("POST", "/api/shop/cards/MINING_RATE/upgrade", p.Token, nil); r.Status != 200 {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	if st := e.fishing(p); !near(st["tankCoins"].(float64), 1.5) || !near(st["miningRate"].(float64), 0.05) {
		t.Fatalf("right after the upgrade: %v", st)
	}
	e.now = t0.Add(80 * time.Second) // +20s at the new 0.05/s
	if st := e.fishing(p); !near(st["tankCoins"].(float64), 2.5) {
		t.Fatalf("new speed applies only from the upgrade: %v", st)
	}
	// raising the capacity keeps the amount and un-fulls a full tank
	e.now = t0.Add(10 * time.Hour)
	if st := e.fishing(p); st["isComplete"] != true || !near(st["tankCoins"].(float64), 5) {
		t.Fatalf("full: %v", st)
	}
	if r := e.do("POST", "/api/shop/cards/COIN_CAPACITY/upgrade", p.Token, nil); r.Status != 200 {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	st := e.fishing(p)
	if st["isComplete"] != false || !near(st["tankCoins"].(float64), 5) || !near(st["tankCapacity"].(float64), 10) {
		t.Fatalf("bigger tank continues from 5: %v", st)
	}
}

func TestBoosters(t *testing.T) {
	e := newEnv(t)
	e.gameConfig()
	p := e.login(120, "en", "")

	list := e.do("GET", "/api/shop/boosters", p.Token, nil).JSON(t)["boosters"].([]any)
	if len(list) != 2 {
		t.Fatalf("%v", list)
	}
	if r := e.do("POST", "/api/shop/boosters/MINING_BOOST_1_5X/buy", p.Token, nil); r.Code(t) != "INSUFFICIENT_STARS" {
		t.Fatalf("no stars: %s", r.Body)
	}
	e.give(p, ledger.Star, 1000)
	// a running session gets the boost from the purchase second on
	e.do("POST", "/api/fishing/start", p.Token, map[string]any{"adViewId": e.watchAd(p, "FISHING_START")})
	t0 := e.now
	e.give(p, ledger.Coin, 0+1) // no-op balance, keeps helper symmetrical
	e.now = t0.Add(100 * time.Second)
	if r := e.do("POST", "/api/shop/boosters/MINING_BOOST_1_5X/buy", p.Token, nil); r.Status != 200 {
		t.Fatalf("buy: %d %s", r.Status, r.Body)
	}
	if e.stars(p) != 900 {
		t.Fatalf("stars %d", e.stars(p))
	}
	// capacity is only 5 coins; use a bigger tank for the arithmetic
	e.exec(`UPDATE fishing_sessions SET capacity_micro=1000000000 WHERE player_id=$1`, p.ID)
	e.now = t0.Add(200 * time.Second)
	// 100s at 0.025 + 100s at 0.0375
	if st := e.fishing(p); !near(st["tankCoins"].(float64), 2.5+3.75) || !near(st["effectiveMiningRate"].(float64), 0.0375) {
		t.Fatalf("boosted tank: %v", st)
	}
	// the same kind cannot be bought again while active; the other kind can
	if r := e.do("POST", "/api/shop/boosters/MINING_BOOST_1_5X/buy", p.Token, nil); r.Code(t) != "BOOSTER_ALREADY_ACTIVE" {
		t.Fatalf("%s", r.Body)
	}
	if r := e.do("POST", "/api/shop/boosters/AUTO_MINING/buy", p.Token, nil); r.Status != 200 {
		t.Fatalf("both kinds together: %d %s", r.Status, r.Body)
	}
	if e.stars(p) != 400 {
		t.Fatalf("stars %d", e.stars(p))
	}
	// 30 days later the boost is over: speed back to normal, and it can be bought again
	e.now = t0.Add(31 * 24 * time.Hour)
	st := e.fishing(p)
	if !near(st["effectiveMiningRate"].(float64), 0.025) {
		t.Fatalf("after expiry: %v", st)
	}
	if r := e.do("POST", "/api/shop/boosters/MINING_BOOST_1_5X/buy", p.Token, nil); r.Status != 200 {
		t.Fatalf("rebuy after expiry: %d %s", r.Status, r.Body)
	}
	if r := e.do("POST", "/api/shop/boosters/NOPE/buy", p.Token, nil); r.Status != 404 {
		t.Fatalf("%d", r.Status)
	}
}

func TestBoosterEndsMidSessionOnlyAffectsItsSeconds(t *testing.T) {
	e := newEnv(t)
	e.gameConfig()
	p := e.login(121, "en", "")
	e.give(p, ledger.Star, 100)
	e.setSetting(settings.KeyBoosterDays, 1)
	e.exec(`UPDATE players SET capacity_level=10 WHERE id=$1`, p.ID) // 50 coins tank
	e.do("POST", "/api/fishing/start", p.Token, map[string]any{"adViewId": e.watchAd(p, "FISHING_START")})
	t0 := e.now
	e.do("POST", "/api/shop/boosters/MINING_BOOST_1_5X/buy", p.Token, nil)
	e.exec(`UPDATE fishing_sessions SET capacity_micro=100000000000 WHERE player_id=$1`, p.ID)
	e.now = t0.Add(36 * time.Hour) // a day boosted + 12h normal
	want := 24*3600*0.0375 + 12*3600*0.025
	if st := e.fishing(p); !near(st["tankCoins"].(float64), want) {
		t.Fatalf("got %v want %v", st["tankCoins"], want)
	}
}

func TestAutocollect(t *testing.T) {
	e := newEnv(t)
	e.gameConfig()
	p := e.login(130, "en", "")
	e.give(p, ledger.Star, 1000)
	e.do("POST", "/api/fishing/start", p.Token, map[string]any{"adViewId": e.watchAd(p, "FISHING_START")})
	t0 := e.now
	// no booster: a full tank just waits
	e.now = t0.Add(time.Hour)
	if n, err := e.d.Fishing.RunAutocollect(context.Background(), 50); err != nil || n != 0 || e.coins(p) != 0 {
		t.Fatalf("no booster: n=%d err=%v coins=%d", n, err, e.coins(p))
	}
	if r := e.do("POST", "/api/shop/boosters/AUTO_MINING/buy", p.Token, nil); r.Status != 200 {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	// the tank is already full when the booster starts: it is collected at once and mining restarts without an ad
	n, err := e.d.Fishing.RunAutocollect(context.Background(), 50)
	if err != nil || n != 1 || e.coins(p) != 5 {
		t.Fatalf("autocollect: n=%d err=%v coins=%d", n, err, e.coins(p))
	}
	st := e.fishing(p)
	if st["isFishing"] != true || !near(st["tankCoins"].(float64), 0) {
		t.Fatalf("a new session must be running and empty: %v", st)
	}
	var autoRows int
	_ = e.d.Pool.QueryRow(context.Background(), `SELECT count(*) FROM ledger_entries WHERE player_id=$1 AND reason='autocollect'`, p.ID).Scan(&autoRows)
	if autoRows != 1 {
		t.Fatalf("ledger rows %d", autoRows)
	}
	// running again right away changes nothing; after the next fill it collects again
	if n, _ := e.d.Fishing.RunAutocollect(context.Background(), 50); n != 0 {
		t.Fatalf("n=%d", n)
	}
	e.now = e.now.Add(time.Hour)
	// an hour holds 18 tanks of 200 s: every tank is collected and the next one starts exactly when the last one filled
	if n, _ := e.d.Fishing.RunAutocollect(context.Background(), 50); n != 1 || e.coins(p) != 5+18*5 {
		t.Fatalf("second round: n=%d coins=%d", n, e.coins(p))
	}
	// when the booster has expired the full tank stays and needs the normal ad flow
	e.now = e.now.Add(40 * 24 * time.Hour)
	if n, _ := e.d.Fishing.RunAutocollect(context.Background(), 50); n != 0 || e.coins(p) != 5+18*5 {
		t.Fatalf("expired booster must not collect: n=%d coins=%d", n, e.coins(p))
	}
	if st := e.fishing(p); !near(st["tankCoins"].(float64), 5) {
		t.Fatalf("coins wait in the tank: %v", st)
	}
	r := e.do("POST", "/api/fishing/collect", p.Token, map[string]any{"adViewId": e.watchAd(p, "COLLECT")})
	if r.Status != 200 || e.coins(p) != 5+18*5+5 {
		t.Fatalf("normal collect: %d %s", r.Status, r.Body)
	}
}

func TestAutocollectSkipsBlockedAndIsExclusive(t *testing.T) {
	e := newEnv(t)
	e.gameConfig()
	a, b := e.login(131, "en", ""), e.login(132, "en", "")
	for _, p := range []player{a, b} {
		e.give(p, ledger.Star, 1000)
		e.do("POST", "/api/fishing/start", p.Token, map[string]any{"adViewId": e.watchAd(p, "FISHING_START")})
		e.do("POST", "/api/shop/boosters/AUTO_MINING/buy", p.Token, nil)
	}
	e.exec(`UPDATE players SET is_blocked=TRUE WHERE id=$1`, b.ID)
	e.now = e.now.Add(time.Hour)
	// two workers at once must still collect exactly once
	var total int64
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			n, _ := e.d.Fishing.RunAutocollect(context.Background(), 50)
			atomic.AddInt64(&total, int64(n))
		}()
	}
	wg.Wait()
	if total != 1 || e.coins(a) != 18*5 || e.coins(b) != 0 {
		t.Fatalf("total=%d a=%d b=%d", total, e.coins(a), e.coins(b))
	}
}

func TestConcurrentCollectPaysOnce(t *testing.T) {
	e := newEnv(t)
	e.gameConfig()
	p := e.login(133, "en", "")
	e.do("POST", "/api/fishing/start", p.Token, map[string]any{"adViewId": e.watchAd(p, "FISHING_START")})
	e.now = e.now.Add(time.Hour)
	ad := e.watchAd(p, "COLLECT")
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			e.do("POST", "/api/fishing/collect", p.Token, map[string]any{"adViewId": ad})
		}()
	}
	wg.Wait()
	if e.coins(p) != 5 {
		t.Fatalf("coins %d, a collect must pay exactly once", e.coins(p))
	}
}

func TestLevelsEndpoint(t *testing.T) {
	e := newEnv(t)
	e.gameConfig()
	e.setSetting(settings.KeyLevelRewards, []map[string]any{{"level": 2, "coins": 100, "stars": 0}})
	p := e.login(140, "ru", "")
	m := e.do("GET", "/api/levels", p.Token, nil).JSON(t)
	lv := m["levels"].([]any)
	if len(lv) != 10 || lv[0].(map[string]any)["name"] != "Уголёк" || lv[1].(map[string]any)["rewardCoins"].(float64) != 100 || m["current"].(float64) != 1 {
		t.Fatalf("%v", m)
	}
}

// Adsgram's callback names only the user and the block. An ad the player abandoned must not catch the confirmation of the
// next one (the player would have to watch twice and the real one would never be accepted).
func TestAbandonedAdDoesNotStealTheCallback(t *testing.T) {
	e := newEnv(t)
	e.gameConfig()
	p := e.login(901, "en", "")

	abandoned := e.do("POST", "/api/ads", p.Token, map[string]any{"placement": "FISHING_START"}).JSON(t)["adViewId"].(string)
	watched := e.watchAd(p, "FISHING_START")
	if watched == abandoned {
		t.Fatalf("a new request must open a new proof")
	}
	if r := e.do("POST", "/api/fishing/start", p.Token, map[string]any{"adViewId": watched}); r.Status != 200 {
		t.Fatalf("the proof of the ad that was watched is accepted: %d %s", r.Status, r.Body)
	}
	var st string
	if err := e.d.Pool.QueryRow(context.Background(), `SELECT status FROM ad_views WHERE id=$1`, abandoned).Scan(&st); err != nil {
		t.Fatal(err)
	}
	if st != "EXPIRED" {
		t.Fatalf("the abandoned request is closed, got %s", st)
	}
}

// The app asks for the state when a tank fills: with Autocollect the coins must already be in and the next tank running,
// without waiting for the background job, and the new tank starts when the old one filled (no mining time is lost).
func TestStateSettlesAutocollectAtOnce(t *testing.T) {
	e := newEnv(t)
	e.gameConfig()
	p := e.login(134, "en", "")
	e.give(p, ledger.Star, 1000)
	e.do("POST", "/api/fishing/start", p.Token, map[string]any{"adViewId": e.watchAd(p, "FISHING_START")})
	if st := e.fishing(p); st["autoCollect"] != false {
		t.Fatalf("no booster yet: %v", st)
	}
	if r := e.do("POST", "/api/shop/boosters/AUTO_MINING/buy", p.Token, nil); r.Status != 200 {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	if st := e.fishing(p); st["autoCollect"] != true {
		t.Fatalf("the booster is on: %v", st)
	}
	// the tank (5 coins at 0.025/s) filled 30 seconds ago; no job has run
	e.now = e.now.Add(230 * time.Second)
	st := e.fishing(p)
	if e.coins(p) != 5 {
		t.Fatalf("the coins are paid when the state is read: %d", e.coins(p))
	}
	if st["isFishing"] != true || !near(st["tankCoins"].(float64), 0.75) {
		t.Fatalf("the next tank has the 30 seconds since the last one filled: %v", st)
	}
	// reading again changes nothing
	if e.fishing(p); e.coins(p) != 5 {
		t.Fatalf("paid twice: %d", e.coins(p))
	}
}

func TestLongAbsencePaysEveryMissedTankAtOnce(t *testing.T) {
	e := newEnv(t)
	e.gameConfig()
	p := e.login(135, "en", "")
	e.give(p, ledger.Star, 1000)
	e.do("POST", "/api/fishing/start", p.Token, map[string]any{"adViewId": e.watchAd(p, "FISHING_START")})
	if r := e.do("POST", "/api/shop/boosters/AUTO_MINING/buy", p.Token, nil); r.Status != 200 {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	// away long enough for 250 tanks (5 coins, one every 200 seconds), more than one catch-up slice
	e.now = e.now.Add(250*200*time.Second + 30*time.Second)
	e.fishing(p)
	if e.coins(p) != 250*5 {
		t.Fatalf("every missed tank is paid on the first read: %d", e.coins(p))
	}
	if e.fishing(p); e.coins(p) != 250*5 {
		t.Fatalf("a second read must not pay more: %d", e.coins(p))
	}
}
