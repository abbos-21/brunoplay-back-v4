package api_test

import (
	"context"
	"os"
	"testing"
	"time"

	"brunoplay/internal/admin"
	"brunoplay/internal/ledger"
	"brunoplay/internal/settings"
)

// scripts/reset-game.sql removes the players and what belongs to them and keeps what the admins built.
func TestResetGameKeepsTheConfigurationAndClearsThePlayers(t *testing.T) {
	sqlText, err := os.ReadFile("../../scripts/reset-game.sql")
	if err != nil {
		t.Fatal(err)
	}
	e := newEnv(t)
	e.gamesConfig()
	boss := e.adminLogin("boss@example.com", admin.RoleSuper)
	p, q := e.login(570, "en", ""), e.login(571, "ru", "")
	e.give(p, ledger.Star, 1000)
	e.give(p, ledger.Coin, 5000)
	e.give(q, ledger.Star, 1000)
	e.spin(p, "TIER_1", 3)
	e.do("POST", "/api/chest/round", q.Token, nil)
	tid := e.tournament(boss, "STARS_SPENT", 0, -time.Hour, 5*time.Hour, cupPrizes)
	e.join(p, tid)
	e.exec(`INSERT INTO bot_users (telegram_id) VALUES (570)`)
	e.exec(`INSERT INTO player_days (player_id, day) VALUES ($1, CURRENT_DATE) ON CONFLICT DO NOTHING`, p.ID)
	e.setSetting(settings.KeyWinnersMin, 123)

	count := func(table string) int { return e.countRows(table) }
	kept := []string{"settings", "settings_history", "admins", "audit_log", "tasks", "bot_users", "player_days", "payout_stats", "daily_stats", "notification_templates", "gift_images", "combo_images", "combo_patterns", "broadcasts"}
	before := map[string]int{}
	for _, k := range kept {
		before[k] = count(k)
	}
	if count("players") != 2 || count("ledger_entries") == 0 || count("tournaments") != 1 || count("tournament_entries") != 1 || count("winners_feed")+count("spin_draws") == 0 {
		t.Fatalf("the test game is empty: players %d", count("players"))
	}
	var maxID int64
	_ = e.d.Pool.QueryRow(context.Background(), `SELECT max(id) FROM players`).Scan(&maxID)

	run := func() error {
		_, err := e.d.Pool.Exec(context.Background(), string(sqlText))
		return err
	}

	// money in flight: refused, nothing changes
	e.exec(`INSERT INTO withdrawals (player_id, amount_coins, coins_per_ton, fee_nano, amount_nano, address, status, created_at, updated_at)
		VALUES ($1, 1000, 100000, 0, 1, 'UQx', 'PENDING', now(), now())`, p.ID)
	if err := run(); err == nil {
		t.Fatal("a withdrawal in progress must stop the reset")
	}
	if count("players") != 2 {
		t.Fatal("a refused reset changes nothing")
	}
	e.exec(`UPDATE withdrawals SET status='CANCELLED'`)

	if err := run(); err != nil {
		t.Fatal(err)
	}
	// every table that belongs to players is empty, whatever tables are added later: a table with a player_id column must be empty
	// unless it is one of the statistics tables that are kept on purpose (their rows name old ids, which are never reused)
	rows, err := e.d.Pool.Query(context.Background(), `SELECT c.table_name FROM information_schema.columns c JOIN information_schema.tables t
		ON t.table_name=c.table_name AND t.table_schema=c.table_schema AND t.table_type='BASE TABLE'
		WHERE c.table_schema='public' AND c.column_name IN ('player_id','referrer_id','referred_id') AND c.table_name NOT IN ('player_days','guard_events','goose_db_version')`)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var n string
		_ = rows.Scan(&n)
		tables = append(tables, n)
	}
	rows.Close()
	for _, tb := range append(tables, "players", "tournaments", "tournament_entries", "idempotency_keys", "winners_feed") {
		if n := count(tb); n != 0 {
			t.Errorf("%s still has %d rows", tb, n)
		}
	}
	for _, k := range kept {
		if count(k) != before[k] {
			t.Errorf("%s was changed: %d -> %d", k, before[k], count(k))
		}
	}
	if v, _ := e.d.Settings.Int(context.Background(), settings.KeyWinnersMin); v != 123 {
		t.Fatalf("settings must stay: %d", v)
	}

	// the admin still logs in, and a new player gets a NEW id (ids are not reused)
	if r := e.do("POST", "/api/admin/auth/login", "", map[string]any{"email": "boss@example.com", "password": adminPW}); r.Status != 200 {
		t.Fatalf("admin login after the reset: %d %s", r.Status, r.Body)
	}
	np := e.login(580, "en", "")
	if np.ID <= maxID {
		t.Fatalf("player ids must continue (%d after %d)", np.ID, maxID)
	}
}
