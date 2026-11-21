package api_test

import (
	"fmt"
	"testing"

	"brunoplay/internal/admin"
	"brunoplay/internal/ledger"
)

func TestAdminSeesWhatAPlayerWonInEachGame(t *testing.T) {
	e := newEnv(t)
	super := e.adminLogin("owner@example.com", admin.RoleSuper)
	p := e.login(880, "en", "")
	e.give(p, ledger.Star, 100)
	var id int64
	if err := e.d.Pool.QueryRow(t.Context(), `SELECT id FROM players WHERE telegram_id=880`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	// a Roulette spin that wins a gift
	e.rouletteSetup(15, map[string]any{"type": "gift", "gift": "mid", "weight": 1000000})
	if r := e.rouletteSpin(p); r.Status != 200 {
		t.Fatalf("spin: %d %s", r.Status, r.Body)
	}
	// a Spin result (stars) and a finished chest round whose opened chest held a gift
	e.exec(`INSERT INTO spin_draws (player_id, tier, batch, gift_key, star_value, price, drawn_key, created_at) VALUES ($1,'basic','b1','stars:500',500,100,'stars:500', (SELECT max(created_at) FROM spin_draws WHERE player_id=$1) + interval '1 second')`, id)
	e.exec(`INSERT INTO chest_rounds (player_id, status, cost, picks, prizes, opened, created_at, finished_at, slot_mode)
		VALUES ($1,'DONE',30,1,'[{"type":"stars","value":50},{"type":"gift","gift":"mid","value":1000}]','{"1":1}', (SELECT max(created_at) FROM spin_draws WHERE player_id=$1) + interval '2 seconds', (SELECT max(created_at) FROM spin_draws WHERE player_id=$1) + interval '2 seconds', true)`, id)

	url := fmt.Sprintf("/api/admin/players/%d/games", id)
	all := e.do("GET", url, super, nil).JSON(t)["sessions"].([]any)
	if len(all) != 3 {
		t.Fatalf("three sessions: %v", all)
	}
	first := all[0].(map[string]any) // newest: the chest
	if first["game"] != "chest" || first["prizeType"] != "gift" || first["gift"] != "mid" || first["giftName"] == "" || first["giftName"] == "mid" {
		t.Fatalf("chest gift with its name: %v", first)
	}
	second := all[1].(map[string]any)
	if second["game"] != "spin" || second["prizeType"] != "stars" || second["prizeValue"].(float64) != 500 {
		t.Fatalf("spin stars: %v", second)
	}
	third := all[2].(map[string]any)
	if third["game"] != "roulette" || third["gift"] != "mid" || third["spent"].(float64) != 15 {
		t.Fatalf("roulette gift: %v", third)
	}
	only := e.do("GET", url+"?game=roulette", super, nil).JSON(t)["sessions"].([]any)
	if len(only) != 1 {
		t.Fatalf("roulette only: %v", only)
	}
	paged := e.do("GET", url+"?limit=2", super, nil).JSON(t)
	if len(paged["sessions"].([]any)) != 2 || paged["more"] != true {
		t.Fatalf("paged: %v", paged)
	}
	if r := e.do("GET", url+"?game=bogus", super, nil); r.Status == 200 {
		t.Fatal("an unknown game is refused")
	}
}
