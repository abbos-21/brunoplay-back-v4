package api_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"brunoplay/internal/db"
	"brunoplay/internal/ledger"
	"brunoplay/internal/settings"
	"brunoplay/internal/shop"
)

// the first requirement text of a card, and whether the whole requirement list is met
func (e *env) cardReq(p player, card string) (string, bool) {
	e.t.Helper()
	for _, c := range e.do("GET", "/api/shop/cards", p.Token, nil).JSON(e.t)["cards"].([]any) {
		m := c.(map[string]any)
		if m["cardName"] != card {
			continue
		}
		reqs := m["requirements"].([]any)
		if len(reqs) == 0 {
			return "", true
		}
		r := reqs[0].(map[string]any)
		return r["description"].(string), r["met"] == true
	}
	e.t.Fatal("no such card")
	return "", false
}

func (e *env) record(p player, kind string) {
	e.t.Helper()
	if err := db.WithTx(context.Background(), e.d.Pool, func(tx pgx.Tx) error { return e.d.Shop.Record(context.Background(), tx, p.ID, kind) }); err != nil {
		e.t.Fatal(err)
	}
}

func TestCardRequirementsCountWhatThePlayerDoesAfterTheyApply(t *testing.T) {
	e := newEnv(t)
	e.gamesConfig()
	lvl := func(n int, cost int, reqs ...map[string]any) map[string]any {
		m := map[string]any{"level": n, "value": 0.025 * float64(n), "cost": cost, "currency": "COIN"}
		if len(reqs) > 0 {
			m["requirements"] = reqs
		}
		return m
	}
	e.setSetting(settings.KeyCardsMining, []map[string]any{
		lvl(1, 0),
		lvl(2, 10, map[string]any{"type": "ride_finish", "value": 2}),
		lvl(3, 10, map[string]any{"type": "collect_tank", "value": 2}),
	})
	p := e.login(900, "en", "")
	e.give(p, ledger.Coin, 1000)
	e.give(p, ledger.Star, 10)
	up := func() resp { return e.do("POST", "/api/shop/cards/MINING_RATE/upgrade", p.Token, nil) }

	// a collection of the tank (real), before the collect requirement applies: it must not count towards level 3 later
	startAd := e.watchAd(p, "FISHING_START")
	t0 := e.now
	if r := e.do("POST", "/api/fishing/start", p.Token, map[string]any{"adViewId": startAd}); r.Status != 200 {
		t.Fatalf("start: %d %s", r.Status, r.Body)
	}
	e.now = t0.Add(2 * time.Hour)
	if r := e.do("POST", "/api/fishing/collect", p.Token, map[string]any{"adViewId": e.watchAd(p, "COLLECT")}); r.Status != 200 {
		t.Fatalf("collect: %d %s", r.Status, r.Body)
	}

	// level 2 needs two finished rides: counted from the first one
	if text, met := e.cardReq(p, "MINING_RATE"); text != "Finish 2 rides in Bruno's Ride (0/2)" || met {
		t.Fatalf("nothing done yet: %q %v", text, met)
	}
	finish := func() {
		id := e.ride(p)
		e.now = e.now.Add(60 * time.Second)
		if r := e.finishRide(p, id, 0, 0); r.Status != 200 {
			t.Fatalf("finish ride: %d %s", r.Status, r.Body)
		}
	}
	finish()
	if text, met := e.cardReq(p, "MINING_RATE"); text != "Finish 2 rides in Bruno's Ride (1/2)" || met {
		t.Fatalf("one ride: %q %v", text, met)
	}
	if r := up(); r.Code(t) != "REQUIREMENT_NOT_MET" {
		t.Fatalf("not yet: %s", r.Body)
	}
	finish()
	if r := up(); r.Status != 200 {
		t.Fatalf("two rides: %d %s", r.Status, r.Body)
	}

	// level 3 needs two collections done after reaching level 2: the one made before does not count
	if text, met := e.cardReq(p, "MINING_RATE"); text != "Collect the tank 2 times (0/2)" || met {
		t.Fatalf("earlier activity does not count: %q %v", text, met)
	}
	e.record(p, shop.ReqCollectTank)
	if text, _ := e.cardReq(p, "MINING_RATE"); !strings.HasSuffix(text, "(1/2)") {
		t.Fatalf("one collection: %q", text)
	}
	e.record(p, shop.ReqCollectTank)
	if text, met := e.cardReq(p, "MINING_RATE"); !met || !strings.HasSuffix(text, "(2/2)") {
		t.Fatalf("met: %q %v", text, met)
	}
	if r := up(); r.Status != 200 {
		t.Fatalf("level 3: %d %s", r.Status, r.Body)
	}
}
