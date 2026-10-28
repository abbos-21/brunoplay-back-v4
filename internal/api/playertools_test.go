package api_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"brunoplay/internal/admin"
)

func (e *env) ledgerTotal(playerID int64, currency string) int64 {
	var s int64
	if err := e.d.Pool.QueryRow(context.Background(), `SELECT COALESCE(sum(amount),0) FROM ledger_entries WHERE player_id=$1 AND currency=$2`, playerID, currency).Scan(&s); err != nil {
		e.t.Fatal(err)
	}
	return s
}

func TestAdminSetsBalancesThroughTheLedger(t *testing.T) {
	e := newEnv(t)
	e.gameConfig()
	boss := e.adminLogin("boss@example.com", admin.RoleSuper)
	help := e.adminLogin("help@example.com", admin.RoleSupport)
	p := e.login(6001, "en", "")
	url := fmt.Sprintf("/api/admin/players/%d/adjust", p.ID)

	if r := e.do("POST", url, help, map[string]any{"coins": 10, "reason": "test"}); r.Status != 403 {
		t.Fatalf("support must not edit balances: %d %s", r.Status, r.Body)
	}
	for _, bad := range []map[string]any{
		{"coins": 10},                               // no reason
		{"reason": "no amounts"},                    // nothing to set
		{"coins": -5, "reason": "negative"},         // below zero
		{"stars": 2_000_000_000_000, "reason": "x"}, // absurd
	} {
		if r := e.do("POST", url, boss, bad); r.Status != 422 {
			t.Fatalf("bad request %v: %d %s", bad, r.Status, r.Body)
		}
	}

	r := e.do("POST", url, boss, map[string]any{"coins": 5000, "stars": 12, "reason": "compensation"})
	if r.Status != 200 || r.JSON(t)["coins"] != float64(5000) || r.JSON(t)["stars"] != float64(12) {
		t.Fatalf("set: %d %s", r.Status, r.Body)
	}
	if e.coins(p) != 5000 || e.ledgerTotal(p.ID, "COIN") != 5000 || e.ledgerTotal(p.ID, "STAR") != 12 {
		t.Fatalf("balance and ledger must agree: coins %d, ledger %d/%d", e.coins(p), e.ledgerTotal(p.ID, "COIN"), e.ledgerTotal(p.ID, "STAR"))
	}
	// lowering works, and leaving a field out leaves it alone
	if r := e.do("POST", url, boss, map[string]any{"coins": 100, "reason": "correction"}); r.Status != 200 {
		t.Fatalf("lower: %d %s", r.Status, r.Body)
	}
	if e.coins(p) != 100 || e.ledgerTotal(p.ID, "COIN") != 100 || e.ledgerTotal(p.ID, "STAR") != 12 {
		t.Fatalf("after lowering: %d %d %d", e.coins(p), e.ledgerTotal(p.ID, "COIN"), e.ledgerTotal(p.ID, "STAR"))
	}
}

func TestAdsSwitchedOffForOnePlayer(t *testing.T) {
	e := newEnv(t)
	e.gameConfig() // ads are required for everybody
	boss := e.adminLogin("boss@example.com", admin.RoleSuper)
	help := e.adminLogin("help@example.com", admin.RoleSupport)
	p := e.login(6002, "en", "")
	other := e.login(6003, "en", "")
	flag := fmt.Sprintf("/api/admin/players/%d/flags", p.ID)

	if r := e.do("PATCH", flag, help, map[string]any{"adsDisabled": true}); r.Status != 403 {
		t.Fatalf("support must not switch ads: %d %s", r.Status, r.Body)
	}
	if r := e.do("PATCH", flag, boss, map[string]any{"adsDisabled": true}); r.Status != 200 {
		t.Fatalf("switch off: %d %s", r.Status, r.Body)
	}
	if v := e.do("GET", fmt.Sprintf("/api/admin/players/%d", p.ID), boss, nil).JSON(t); v["adsDisabled"] != true {
		t.Fatalf("admin view: %v", v["adsDisabled"])
	}

	// the proof is handed over already completed (skip), and fishing works with it
	view := e.do("POST", "/api/ads", p.Token, map[string]any{"placement": "FISHING_START"}).JSON(t)
	if view["skip"] != true || view["adViewId"] == "" {
		t.Fatalf("view: %v", view)
	}
	if r := e.do("POST", "/api/fishing/start", p.Token, map[string]any{"adViewId": view["adViewId"]}); r.Status != 200 {
		t.Fatalf("start without an ad: %d %s", r.Status, r.Body)
	}
	e.now = e.now.Add(time.Hour)
	v2 := e.do("POST", "/api/ads", p.Token, map[string]any{"placement": "COLLECT"}).JSON(t)
	if r := e.do("POST", "/api/fishing/collect", p.Token, map[string]any{"adViewId": v2["adViewId"]}); r.Status != 200 {
		t.Fatalf("collect without an ad: %d %s", r.Status, r.Body)
	}
	// a client that sends no proof at all is fine too
	if r := e.do("POST", "/api/fishing/start", p.Token, map[string]any{}); r.Status != 200 {
		t.Fatalf("start with no proof: %d %s", r.Status, r.Body)
	}
	// no interstitial and no sponsored task for this player
	cfg := e.do("GET", "/api/ads/config", p.Token, nil).JSON(t)
	if cfg["interstitial"].(map[string]any)["enabled"] != false || cfg["task"].(map[string]any)["enabled"] != false {
		t.Fatalf("config: %v", cfg)
	}

	// everybody else still needs the ad
	if r := e.do("POST", "/api/fishing/start", other.Token, map[string]any{}); r.Code(t) != "AD_NOT_COMPLETED" {
		t.Fatalf("another player: %d %s", r.Status, r.Body)
	}
	if v := e.do("POST", "/api/ads", other.Token, map[string]any{"placement": "FISHING_START"}).JSON(t); v["skip"] == true {
		t.Fatalf("skip must be for that player only: %v", v)
	}
}

func TestResetStartsThePlayerOverAndKeepsTheMoneyTrail(t *testing.T) {
	e := newEnv(t)
	e.gameConfig()
	boss := e.adminLogin("boss@example.com", admin.RoleSuper)
	help := e.adminLogin("help@example.com", admin.RoleSupport)
	p := e.login(6004, "en", "")
	adj := fmt.Sprintf("/api/admin/players/%d/adjust", p.ID)
	reset := fmt.Sprintf("/api/admin/players/%d/reset", p.ID)
	if r := e.do("POST", adj, boss, map[string]any{"coins": 3000, "stars": 40, "reason": "setup"}); r.Status != 200 {
		t.Fatalf("setup: %d %s", r.Status, r.Body)
	}
	e.exec(`UPDATE players SET mining_level=4, capacity_level=3, level=3 WHERE id=$1`, p.ID)

	if r := e.do("POST", reset, help, map[string]any{"confirm": true, "reason": "test"}); r.Status != 403 {
		t.Fatalf("support must not reset: %d %s", r.Status, r.Body)
	}
	if r := e.do("POST", reset, boss, map[string]any{"reason": "no confirm"}); r.Status == 200 {
		t.Fatalf("a reset needs confirm: %s", r.Body)
	}
	if r := e.do("POST", reset, boss, map[string]any{"confirm": true}); r.Status != 422 {
		t.Fatalf("a reset needs a reason: %d %s", r.Status, r.Body)
	}
	// money on its way blocks it
	e.exec(`INSERT INTO withdrawals (player_id, amount_coins, coins_per_ton, fee_nano, amount_nano, address, status) VALUES ($1, 100, 1, 0, 1, 'x', 'PENDING')`, p.ID)
	if r := e.do("POST", reset, boss, map[string]any{"confirm": true, "reason": "test"}); r.Code(t) != "ADMIN_ACTION_REFUSED" {
		t.Fatalf("open withdrawal: %d %s", r.Status, r.Body)
	}
	e.exec(`DELETE FROM withdrawals WHERE player_id=$1`, p.ID)
	// an invoice the player opened and never paid does not block a reset: it is closed
	e.exec(`INSERT INTO topup_orders (id, player_id, method, status, pack_stars, bonus_stars, bonus_percent, price, created_at, expires_at)
		VALUES ('ord-unpaid', $1, 'TG_STARS', 'PENDING', 100, 0, 0, 100, now(), now() + interval '1 day')`, p.ID)
	// a blocked player cannot reset its way out
	e.exec(`UPDATE players SET is_blocked=TRUE WHERE id=$1`, p.ID)
	if r := e.do("POST", reset, boss, map[string]any{"confirm": true, "reason": "test"}); r.Code(t) != "ADMIN_ACTION_REFUSED" {
		t.Fatalf("blocked: %d %s", r.Status, r.Body)
	}
	e.exec(`UPDATE players SET is_blocked=FALSE WHERE id=$1`, p.ID)

	if r := e.do("POST", reset, boss, map[string]any{"confirm": true, "reason": "fresh start"}); r.Status != 200 {
		t.Fatalf("reset: %d %s", r.Status, r.Body)
	}
	var orderStatus string
	if err := e.d.Pool.QueryRow(context.Background(), `SELECT status FROM topup_orders WHERE id='ord-unpaid'`).Scan(&orderStatus); err != nil || orderStatus != "FAILED" {
		t.Fatalf("the unpaid invoice must be closed: %q %v", orderStatus, err)
	}
	// the old record: archived, balances zero through the ledger, history kept
	if e.coins(p) != 0 || e.ledgerTotal(p.ID, "COIN") != 0 || e.ledgerTotal(p.ID, "STAR") != 0 {
		t.Fatalf("old record: coins %d, ledger %d/%d", e.coins(p), e.ledgerTotal(p.ID, "COIN"), e.ledgerTotal(p.ID, "STAR"))
	}
	var entries int
	if err := e.d.Pool.QueryRow(context.Background(), `SELECT count(*) FROM ledger_entries WHERE player_id=$1`, p.ID).Scan(&entries); err != nil || entries < 4 {
		t.Fatalf("the ledger history must stay: %d %v", entries, err)
	}
	old := e.do("GET", fmt.Sprintf("/api/admin/players/%d", p.ID), boss, nil).JSON(t)
	if old["archived"] != true || old["resetAt"] == nil {
		t.Fatalf("old record in admin: %v", old)
	}
	// the old session no longer works...
	if r := e.do("GET", "/api/ads/config", p.Token, nil); r.Status != 401 {
		t.Fatalf("old token: %d %s", r.Status, r.Body)
	}
	// ...and the same Telegram account signs up as a brand-new player
	fresh := e.login(6004, "en", "")
	if fresh.ID == p.ID {
		t.Fatal("a new player record was expected")
	}
	var coins, stars int64
	var level, mining, capacity int
	if err := e.d.Pool.QueryRow(context.Background(), `SELECT coins, stars, level, mining_level, capacity_level FROM players WHERE id=$1`, fresh.ID).Scan(&coins, &stars, &level, &mining, &capacity); err != nil {
		t.Fatal(err)
	}
	if coins != 0 || stars != 0 || level != 1 || mining != 1 || capacity != 1 {
		t.Fatalf("fresh player: %d coins, %d stars, level %d, cards %d/%d", coins, stars, level, mining, capacity)
	}
	// the dashboard totals no longer count the archived record
	var archivedCoins int64
	_ = e.d.Pool.QueryRow(context.Background(), `SELECT coins FROM players WHERE id=$1 AND deleted_at IS NOT NULL`, p.ID).Scan(&archivedCoins)
	if archivedCoins != 0 {
		t.Fatalf("archived coins: %d", archivedCoins)
	}
}
