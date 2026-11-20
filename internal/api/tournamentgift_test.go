package api_test

import (
	"fmt"
	"testing"
)

func TestATournamentGiftCannotBeSoldButCanBeClaimed(t *testing.T) {
	e := newEnv(t)
	p := e.login(830, "en", "")
	var id int64
	if err := e.d.Pool.QueryRow(t.Context(), `SELECT id FROM players WHERE telegram_id=830`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	add := func(source string) int64 {
		var gid int64
		if err := e.d.Pool.QueryRow(t.Context(), `INSERT INTO player_gifts (player_id, gift_key, star_value, status, source, source_ref, created_at)
			VALUES ($1,'mid',1000,'OWNED',$2,'1',now()) RETURNING id`, id, source).Scan(&gid); err != nil {
			t.Fatal(err)
		}
		return gid
	}
	won, bought := add("tournament"), add("spin")

	r := e.do("POST", fmt.Sprintf("/api/gifts/%d/sell", won), p.Token, map[string]any{})
	if r.Status != 409 || r.Code(t) != "GIFT_NOT_SELLABLE" || e.stars(p) != 0 {
		t.Fatalf("a tournament gift is not sold: %d %s stars=%d", r.Status, r.Body, e.stars(p))
	}
	if r := e.do("POST", fmt.Sprintf("/api/gifts/%d/sell", bought), p.Token, map[string]any{}); r.Status != 200 || e.stars(p) != 1000 {
		t.Fatalf("another gift is still sold: %d %s", r.Status, r.Body)
	}
	if r := e.do("POST", fmt.Sprintf("/api/gifts/%d/claim", won), p.Token, map[string]any{}); r.Status != 200 {
		t.Fatalf("a tournament gift can be claimed: %d %s", r.Status, r.Body)
	}
}
