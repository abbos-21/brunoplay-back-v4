package api_test

import (
	"context"
	"testing"

	"brunoplay/internal/ledger"
	"brunoplay/internal/settings"
)

// a prize list that always draws the first prize (the other one has a negligible weight)
func (e *env) rouletteSetup(price int64, first map[string]any) {
	e.gamesConfig()
	e.setSetting(settings.KeyRoulettePublic, true)
	e.setSetting(settings.KeyRouletteCost, price)
	first["weight"] = 1e12
	e.setSetting(settings.KeyRoulettePrizes, []map[string]any{first, {"type": "coins", "value": 500, "weight": 1}})
}

func (e *env) rouletteSpin(p player, hdr ...string) resp {
	return e.do("POST", "/api/roulette/spin", p.Token, map[string]any{}, hdr...)
}

func TestRouletteInfoListsThePricesAndPrizesBestFirst(t *testing.T) {
	e := newEnv(t)
	e.gamesConfig()
	e.setSetting(settings.KeyRoulettePublic, true)
	e.setSetting(settings.KeyRouletteCost, 15)
	e.setSetting(settings.KeyRoulettePrizes, []map[string]any{
		{"type": "coins", "value": 500, "weight": 90},
		{"type": "gift", "gift": "big", "weight": 1},
		{"type": "stars", "value": 300, "weight": 9},
	})
	p := e.login(700, "ru", "")
	info := e.do("GET", "/api/roulette", p.Token, nil).JSON(t)
	prizes := info["prizes"].([]any)
	if info["costStars"].(float64) != 15 || len(prizes) != 3 {
		t.Fatalf("%v", info)
	}
	first := prizes[0].(map[string]any)
	if first["type"] != "gift" || first["gift"] != "big" || first["value"].(float64) != 100000 || first["label"] != "Большой" {
		t.Fatalf("the gift is first, worth its catalogue value, named in the player's language: %v", first)
	}
	if prizes[1].(map[string]any)["type"] != "stars" || prizes[2].(map[string]any)["type"] != "coins" {
		t.Fatalf("best first: %v", prizes)
	}
	for _, x := range prizes {
		if x.(map[string]any)["weight"].(float64) != 1 {
			t.Fatalf("the odds are hidden unless published: %v", x)
		}
	}
	e.setSetting(settings.KeySpinPublishOdds, true)
	prizes = e.do("GET", "/api/roulette", p.Token, nil).JSON(t)["prizes"].([]any)
	if prizes[0].(map[string]any)["weight"].(float64) != 1 || prizes[2].(map[string]any)["weight"].(float64) != 90 {
		t.Fatalf("published odds: %v", prizes)
	}
}

func TestRouletteCoinPrizeIsPaidOnce(t *testing.T) {
	e := newEnv(t)
	e.rouletteSetup(15, map[string]any{"type": "coins", "value": 3000})
	p := e.login(701, "en", "")
	if r := e.rouletteSpin(p); r.Code(t) != "INSUFFICIENT_STARS" {
		t.Fatalf("a spin needs the stars: %s", r.Body)
	}
	if e.countRows("spin_draws") != 0 || e.coins(p) != 0 {
		t.Fatal("a refused spin leaves nothing behind")
	}
	e.give(p, ledger.Star, 40)
	coins := e.coins(p)
	r := e.rouletteSpin(p, "Idempotency-Key", "same-spin")
	res := r.JSON(t)
	prize := res["prize"].(map[string]any)
	if r.Status != 200 || prize["type"] != "coins" || prize["value"].(float64) != 3000 || res["spent"].(float64) != 15 || res["stars"].(float64) != 25 {
		t.Fatalf("%d %v", r.Status, res)
	}
	if e.stars(p) != 25 || e.coins(p) != coins+3000 {
		t.Fatalf("stars=%d coins=%d", e.stars(p), e.coins(p))
	}
	// the same request again is answered with the stored result and charges nothing
	if again := e.rouletteSpin(p, "Idempotency-Key", "same-spin"); again.Status != 200 || e.stars(p) != 25 || e.countRows("spin_draws") != 1 {
		t.Fatalf("a repeated request must not charge or pay twice: %d stars=%d", again.Status, e.stars(p))
	}
	if e.ledgerSum(p, "STAR") != 25 {
		t.Fatalf("ledger: %d", e.ledgerSum(p, "STAR"))
	}
}

func TestRouletteGiftGoesIntoTheCollection(t *testing.T) {
	e := newEnv(t)
	e.rouletteSetup(0, map[string]any{"type": "gift", "gift": "mid"})
	p := e.login(702, "en", "")
	prize := e.rouletteSpin(p).JSON(t)["prize"].(map[string]any)
	if prize["type"] != "gift" || prize["gift"] != "mid" || prize["starValue"].(float64) != 1000 || prize["giftId"].(float64) <= 0 {
		t.Fatalf("%v", prize)
	}
	var key, source string
	var value int64
	if err := e.d.Pool.QueryRow(context.Background(), `SELECT gift_key, star_value, source FROM player_gifts WHERE player_id=$1`, p.ID).Scan(&key, &value, &source); err != nil {
		t.Fatal(err)
	}
	if key != "mid" || value != 1000 || source != "roulette" || e.coins(p) != 0 {
		t.Fatalf("%s %d %s", key, value, source)
	}
	if e.stars(p) != 0 {
		t.Fatal("a free spin charges nothing")
	}
}

func TestRouletteStarPrize(t *testing.T) {
	e := newEnv(t)
	e.rouletteSetup(10, map[string]any{"type": "stars", "value": 250})
	p := e.login(703, "en", "")
	e.give(p, ledger.Star, 10)
	res := e.rouletteSpin(p).JSON(t)
	if res["stars"].(float64) != 250 || e.stars(p) != 250 {
		t.Fatalf("10 paid, 250 won: %v stars=%d", res, e.stars(p))
	}
}

func TestRouletteGuardTurnsAHugePrizeIntoTheCheapestOne(t *testing.T) {
	e := newEnv(t)
	e.rouletteSetup(15, map[string]any{"type": "gift", "gift": "big"}) // worth 100,000 stars
	e.setSetting(settings.KeyGuardRatio, 0.9)
	e.setSetting(settings.KeyGuardMinVolume, 0)
	p := e.login(704, "en", "")
	e.give(p, ledger.Star, 15)
	prize := e.rouletteSpin(p).JSON(t)["prize"].(map[string]any)
	if prize["type"] != "coins" || prize["value"].(float64) != 500 {
		t.Fatalf("the guard gives the cheapest prize instead: %v", prize)
	}
	if e.countRows("guard_events") != 1 || e.countRows("player_gifts") != 0 {
		t.Fatal("the refused gift is logged and not given")
	}
}

func TestRouletteStopsOnAnUnknownGiftWithoutCharging(t *testing.T) {
	e := newEnv(t)
	e.rouletteSetup(15, map[string]any{"type": "gift", "gift": "no-such-gift"})
	p := e.login(705, "en", "")
	e.give(p, ledger.Star, 50)
	if r := e.rouletteSpin(p); r.Status != 422 || r.Code(t) != "GIFT_CATALOG_INVALID" {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	if e.stars(p) != 50 || e.countRows("spin_draws") != 0 {
		t.Fatal("nothing is charged while the prize list is wrong")
	}
	if r := e.do("GET", "/api/roulette", p.Token, nil); r.Status != 422 {
		t.Fatalf("the screen cannot open with a wrong list: %d", r.Status)
	}
}

func TestRoulettePrizeListIsChecked(t *testing.T) {
	e := newEnv(t)
	for name, bad := range map[string]any{
		"one prize":    []map[string]any{{"type": "coins", "value": 5, "weight": 1}},
		"zero weight":  []map[string]any{{"type": "coins", "value": 5, "weight": 0}, {"type": "coins", "value": 6, "weight": 1}},
		"no gift key":  []map[string]any{{"type": "gift", "weight": 1}, {"type": "coins", "value": 6, "weight": 1}},
		"zero coins":   []map[string]any{{"type": "coins", "value": 0, "weight": 1}, {"type": "coins", "value": 6, "weight": 1}},
		"unknown type": []map[string]any{{"type": "car", "weight": 1}, {"type": "coins", "value": 6, "weight": 1}},
	} {
		if err := e.d.Settings.Set(context.Background(), settings.KeyRoulettePrizes, bad, 0); err == nil {
			t.Errorf("%s must be refused", name)
		}
	}
}

// the draw follows the weights
func TestRouletteDrawFollowsTheWeights(t *testing.T) {
	e := newEnv(t)
	e.gamesConfig()
	e.setSetting(settings.KeyRoulettePublic, true)
	e.setSetting(settings.KeyRouletteCost, 0)
	e.setSetting(settings.KeyRoulettePrizes, []map[string]any{{"type": "coins", "value": 100, "weight": 3}, {"type": "coins", "value": 200, "weight": 1}})
	p := e.login(706, "en", "")
	const n = 400
	var low int
	for i := 0; i < n; i++ {
		if e.rouletteSpin(p).JSON(t)["prize"].(map[string]any)["value"].(float64) == 100 {
			low++
		}
	}
	if share := float64(low) / n; share < 0.65 || share > 0.85 {
		t.Fatalf("the prize with weight 3 of 4 came %.0f%% of the time", share*100)
	}
}

// Until the owner opens it (roulette.public), only a player whose Telegram id is on an active admin account can see or play the Roulette;
// for everybody else it does not exist, and a refused spin charges nothing.
func TestRouletteIsOnlyForTheAdminUntilItIsOpened(t *testing.T) {
	e := newEnv(t)
	e.rouletteSetup(15, map[string]any{"type": "coins", "value": 500})
	e.setSetting(settings.KeyRoulettePublic, false)
	stranger := e.login(710, "en", "")
	admin := e.login(711, "en", "")
	e.give(stranger, ledger.Star, 50)
	e.give(admin, ledger.Star, 50)

	// no admin account carries that Telegram id: nobody
	if r := e.do("GET", "/api/roulette", admin.Token, nil); r.Status != 404 {
		t.Fatalf("without an admin id nobody sees it: %d", r.Status)
	}
	e.adminLogin("roulette-admin@example.com", "support")
	e.exec(`UPDATE admins SET telegram_id=711 WHERE email='roulette-admin@example.com'`)
	e.exec(`UPDATE admins SET is_active=false WHERE email='roulette-admin@example.com'`)
	if r := e.do("GET", "/api/roulette", admin.Token, nil); r.Status != 404 {
		t.Fatalf("a deactivated admin does not count: %d", r.Status)
	}
	e.exec(`UPDATE admins SET is_active=true WHERE email='roulette-admin@example.com'`)
	if r := e.do("GET", "/api/roulette", stranger.Token, nil); r.Status != 404 {
		t.Fatalf("a stranger must not see it: %d %s", r.Status, r.Body)
	}
	if r := e.rouletteSpin(stranger); r.Status != 404 || e.stars(stranger) != 50 || e.countRows("spin_draws") != 0 {
		t.Fatalf("a stranger cannot spin and is not charged: %d stars=%d", r.Status, e.stars(stranger))
	}
	if r := e.do("GET", "/api/roulette", admin.Token, nil); r.Status != 200 {
		t.Fatalf("the admin sees it: %d %s", r.Status, r.Body)
	}
	if r := e.rouletteSpin(admin); r.Status != 200 || e.stars(admin) != 35 {
		t.Fatalf("the admin can spin: %d %s", r.Status, r.Body)
	}
	// opened to everybody
	e.setSetting(settings.KeyRoulettePublic, true)
	if r := e.do("GET", "/api/roulette", stranger.Token, nil); r.Status != 200 {
		t.Fatalf("open to all: %d", r.Status)
	}
}
