package api_test

import (
	"fmt"
	"testing"

	"brunoplay/internal/admin"
)

func TestAdminPlayerShowsTheTonTakenOut(t *testing.T) {
	e := newEnv(t)
	super := e.adminLogin("owner@example.com", admin.RoleSuper)
	p := e.login(820, "en", "")
	var id int64
	if err := e.d.Pool.QueryRow(t.Context(), `SELECT id FROM players WHERE telegram_id=820`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	add := func(status string, nano int64) {
		e.exec(`INSERT INTO withdrawals (player_id, amount_coins, coins_per_ton, fee_nano, amount_nano, address, status)
			VALUES ($1, 1000, 1000, 0, $2, 'UQtest', $3)`, id, nano, status)
	}
	add("COMPLETED", 1_500_000_000)
	add("COMPLETED", 500_000_000)
	add("FAILED", 9_000_000_000)
	add("PENDING", 250_000_000)

	got := e.do("GET", fmt.Sprintf("/api/admin/players/%d", id), super, nil).JSON(t)
	if got["withdrawnNano"].(float64) != 2_000_000_000 || got["withdrawnCount"].(float64) != 2 ||
		got["pendingWithdrawalNano"].(float64) != 250_000_000 || got["pendingWithdrawalCount"].(float64) != 1 {
		t.Fatalf("totals: %v", got)
	}
	_ = p
}
