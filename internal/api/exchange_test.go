package api_test

import (
	"testing"

	"brunoplay/internal/ledger"
	"brunoplay/internal/settings"
)

// Coins buy stars at the price from the settings (coins per star); both balances and the ledger move together.
func TestExchangeCoinsToStars(t *testing.T) {
	e := newEnv(t)
	p := e.login(530, "en", "")

	info := e.do("GET", "/api/exchange", p.Token, nil).JSON(t)
	if info["coinsPerStar"].(float64) != 20000 || info["minStars"].(float64) != 1 || info["enabled"] != true {
		t.Fatalf("defaults: %v", info)
	}

	if r := e.do("POST", "/api/exchange", p.Token, map[string]any{"stars": 5}); r.Code(t) != "INSUFFICIENT_COINS" {
		t.Fatalf("no coins: %s", r.Body)
	}
	e.give(p, ledger.Coin, 1000000)

	for _, bad := range []any{0, -3, "x"} {
		if r := e.do("POST", "/api/exchange", p.Token, map[string]any{"stars": bad}); r.Status < 400 {
			t.Fatalf("%v must be refused: %d", bad, r.Status)
		}
	}

	r := e.do("POST", "/api/exchange", p.Token, map[string]any{"stars": 30})
	if r.Status != 200 {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	got := r.JSON(t)
	if got["coins"].(float64) != 600000 || got["starsBalance"].(float64) != 30 || got["coinsBalance"].(float64) != 400000 {
		t.Fatalf("30 stars cost 600,000 coins: %v", got)
	}
	if e.stars(p) != 30 || e.coins(p) != 400000 || e.ledgerSum(p, "STAR") != 30 || e.ledgerSum(p, "COIN") != 400000 {
		t.Fatalf("balances: stars %d coins %d", e.stars(p), e.coins(p))
	}
	if e.countRows("ledger_entries WHERE reason='exchange_stars'") != 1 || e.countRows("ledger_entries WHERE reason='exchange_coins'") != 1 {
		t.Fatal("one ledger row for each side")
	}

	// the price is an admin setting and applies at once; a fraction of a coin is rounded up
	e.setSetting(settings.KeyExchangeRate, 2.5)
	e.setSetting(settings.KeyExchangeMinStars, 10)
	if r := e.do("POST", "/api/exchange", p.Token, map[string]any{"stars": 9}); r.Status < 400 {
		t.Fatalf("below the minimum: %d", r.Status)
	}
	got = e.do("POST", "/api/exchange", p.Token, map[string]any{"stars": 11}).JSON(t)
	if got["coins"].(float64) != 28 || e.coins(p) != 399972 || e.stars(p) != 41 {
		t.Fatalf("11 x 2.5 = 27.5, rounded up to 28: %v", got)
	}

	// more than the player can pay: refused, nothing changes
	e.setSetting(settings.KeyExchangeRate, 20000)
	if r := e.do("POST", "/api/exchange", p.Token, map[string]any{"stars": 1000}); r.Code(t) != "INSUFFICIENT_COINS" {
		t.Fatalf("%s", r.Body)
	}
	if e.stars(p) != 41 || e.coins(p) != 399972 {
		t.Fatal("a refused exchange changes nothing")
	}

	// price 0 switches the exchange off
	e.setSetting(settings.KeyExchangeRate, 0)
	if e.do("GET", "/api/exchange", p.Token, nil).JSON(t)["enabled"] != false {
		t.Fatal("price 0 = off")
	}
	if r := e.do("POST", "/api/exchange", p.Token, map[string]any{"stars": 10}); r.Status < 400 || e.stars(p) != 41 {
		t.Fatalf("off: %d", r.Status)
	}
}
