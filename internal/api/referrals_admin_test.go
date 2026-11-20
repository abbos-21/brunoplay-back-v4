package api_test

import (
	"fmt"
	"testing"

	"brunoplay/internal/admin"
)

func TestAdminPlayerReferralsArePagedNewestFirst(t *testing.T) {
	e := newEnv(t)
	super := e.adminLogin("owner@example.com", admin.RoleSuper)
	e.login(840, "en", "")
	var boss int64
	if err := e.d.Pool.QueryRow(t.Context(), `SELECT id FROM players WHERE telegram_id=840`).Scan(&boss); err != nil {
		t.Fatal(err)
	}
	var ids []int64
	for i := 0; i < 5; i++ {
		e.login(int64(850+i), "en", "")
		var pid int64
		if err := e.d.Pool.QueryRow(t.Context(), `SELECT id FROM players WHERE telegram_id=$1`, 850+i).Scan(&pid); err != nil {
			t.Fatal(err)
		}
		e.exec(`UPDATE players SET referred_by=$1, referral_counted=$3 WHERE id=$2`, boss, pid, i < 2)
		ids = append(ids, pid)
	}
	url := fmt.Sprintf("/api/admin/players/%d/referrals", boss)
	first := e.do("GET", url+"?limit=3", super, nil).JSON(t)
	list := first["referrals"].([]any)
	if len(list) != 3 || first["total"].(float64) != 5 || first["counted"].(float64) != 2 || first["more"] != true ||
		int64(list[0].(map[string]any)["id"].(float64)) != ids[4] {
		t.Fatalf("first page: %v", first)
	}
	last := int64(list[2].(map[string]any)["id"].(float64))
	second := e.do("GET", fmt.Sprintf("%s?limit=3&before=%d", url, last), super, nil).JSON(t)
	if len(second["referrals"].([]any)) != 2 || second["more"] != false {
		t.Fatalf("second page: %v", second)
	}
}
