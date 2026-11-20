package api_test

import "testing"

func TestWinnersCanBeNarrowedToOneGame(t *testing.T) {
	e := newEnv(t)
	p := e.login(870, "en", "")
	for _, g := range []string{"spin", "roulette", "chest", "roulette"} {
		e.exec(`INSERT INTO winners_feed (player_id, username, game, prize_type, prize_value, spent_stars, created_at) VALUES (1,'u',$1,'stars',500,10,now())`, g)
	}
	count := func(url string) (int, float64) {
		w := e.do("GET", url, p.Token, nil).JSON(t)
		return len(w["data"].([]any)), w["total"].(float64)
	}
	if n, total := count("/api/spin/winners"); n != 4 || total != 4 {
		t.Fatalf("all games: %d %v", n, total)
	}
	if n, total := count("/api/spin/winners?game=roulette"); n != 2 || total != 2 {
		t.Fatalf("roulette only: %d %v", n, total)
	}
	if n, _ := count("/api/spin/winners?game=nothing"); n != 0 {
		t.Fatalf("unknown game: %d", n)
	}
}
