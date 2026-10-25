package api_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"brunoplay/internal/admin"
	"brunoplay/internal/ledger"
	"brunoplay/internal/settings"
)

func series(t *testing.T, e *env, token, metric string) []any {
	t.Helper()
	m := e.do("GET", "/api/admin/dashboard?days=30", token, nil).JSON(t)["series"].(map[string]any)
	v, _ := m[metric].([]any)
	return v
}

func sumOf(points []any, dim string) float64 {
	var s float64
	for _, x := range points {
		p := x.(map[string]any)
		if dim == "" || p["dim"] == dim {
			s += p["value"].(float64)
		}
	}
	return s
}

func TestDashboardAggregatesAndRetention(t *testing.T) {
	e := newEnv(t)
	e.gamesConfig()
	e.moneyConfig()
	e.wdConfig()
	fin := e.adminLogin("fin@example.com", admin.RoleFinance)
	ctx := context.Background()

	a, b := e.login(800, "en", ""), e.login(801, "ru", "")
	e.give(a, ledger.Star, 5000)
	e.spin(a, "TIER_1", 3)
	e.do("POST", "/api/fishing/start", a.Token, map[string]any{"adViewId": e.watchAd(a, "FISHING_START")})
	e.now = e.now.Add(time.Hour)
	e.do("POST", "/api/fishing/collect", a.Token, map[string]any{"adViewId": e.watchAd(a, "COLLECT")})
	o := e.do("POST", "/api/topup/telegram-invoice", b.Token, map[string]any{"packStars": 500}).JSON(t)
	e.tgUpdate(telegramPaid(801, o["id"].(string), 500))
	e.give(a, ledger.Coin, 100000)
	e.wd(a, 20000, walletAddr(9))

	if err := e.d.Stats.Run(ctx); err != nil {
		t.Fatal(err)
	}
	fin = e.adminRelogin("fin@example.com") // an hour passed in this test
	if sumOf(series(t, e, fin, "new_players"), "EN") != 1 || sumOf(series(t, e, fin, "new_players"), "RU") != 1 {
		t.Fatalf("new players by language: %v", series(t, e, fin, "new_players"))
	}
	if sumOf(series(t, e, fin, "dau"), "") != 2 {
		t.Fatalf("dau: %v", series(t, e, fin, "dau"))
	}
	if sumOf(series(t, e, fin, "stars_bought"), "TG_STARS") != 500 {
		t.Fatalf("stars bought: %v", series(t, e, fin, "stars_bought"))
	}
	if sumOf(series(t, e, fin, "coins_in"), "mining_collect") != 5 {
		t.Fatalf("coins mined: %v", series(t, e, fin, "coins_in"))
	}
	if sumOf(series(t, e, fin, "stars_out"), "spin_charge") != 300 || sumOf(series(t, e, fin, "coins_out"), "withdrawal") != 20000 {
		t.Fatalf("spend: %v %v", series(t, e, fin, "stars_out"), series(t, e, fin, "coins_out"))
	}
	if sumOf(series(t, e, fin, "ad_views"), "FISHING_START:USED") != 1 || sumOf(series(t, e, fin, "ad_views"), "COLLECT:USED") != 1 {
		t.Fatalf("ads: %v", series(t, e, fin, "ad_views"))
	}
	circ := series(t, e, fin, "circulation")
	if sumOf(circ, "stars") != 5000-300+500 {
		t.Fatalf("stars in circulation: %v", circ)
	}
	// refreshing a day never doubles anything
	_ = e.d.Stats.Run(ctx)
	if sumOf(series(t, e, fin, "stars_bought"), "TG_STARS") != 500 {
		t.Fatal("a refresh must replace, not add")
	}
	live := e.do("GET", "/api/admin/dashboard", fin, nil).JSON(t)["live"].(map[string]any)
	if live["pendingWithdrawals"].(float64) != 1 || live["pendingWithdrawalCoins"].(float64) != 20000 {
		t.Fatalf("live: %v", live)
	}

	// retention: a player who comes back the next day
	e.nextDay()
	e.d.PlayerTokenTTL = 100000 * time.Hour
	e.login(800, "en", "")
	fin = e.adminRelogin("fin@example.com")
	_ = e.d.Stats.Run(ctx)
	size := sumOf(series(t, e, fin, "cohort_size"), "")
	back := sumOf(series(t, e, fin, "returned_d1"), "")
	if size != 2 || back != 1 {
		t.Fatalf("day-1 retention: cohort %v returned %v", size, back)
	}
	if mau := series(t, e, fin, "mau"); len(mau) == 0 {
		t.Fatal("mau is stored")
	}
	// content and support can read the dashboard; an anonymous caller cannot
	if r := e.do("GET", "/api/admin/dashboard", "", nil); r.Status != 401 {
		t.Fatal(r.Status)
	}
}

func TestRiskSignalsScoreOnceAndHoldAutomatically(t *testing.T) {
	e := newEnv(t)
	e.gamesConfig()
	e.wdConfig()
	ctx := context.Background()
	sup := e.adminLogin("sup@example.com", admin.RoleSupport)
	var alerts []string
	e.d.Risk.Alert = func(ctx context.Context, text string) { alerts = append(alerts, text) }

	// five accounts on one address, and one of them withdraws on the day it signed up
	var ring []player
	for i := 0; i < 5; i++ {
		p := e.login(int64(810+i), "en", "")
		e.exec(`UPDATE players SET last_ip='9.9.9.9' WHERE id=$1`, p.ID)
		ring = append(ring, p)
	}
	solo := e.login(820, "en", "")
	e.exec(`UPDATE players SET last_ip='1.1.1.1' WHERE id=$1`, solo.ID)
	e.give(ring[0], ledger.Coin, 100000)
	e.wd(ring[0], 20000, walletAddr(9))

	// no threshold set: scoring happens, nobody is held
	flagged, held, err := e.d.Risk.Scan(ctx)
	if err != nil || flagged != 6 || held != 0 {
		t.Fatalf("scan: flagged=%d held=%d err=%v", flagged, held, err)
	}
	var score int
	_ = e.d.Pool.QueryRow(ctx, `SELECT risk_score FROM players WHERE id=$1`, ring[0].ID).Scan(&score)
	if score != 65 { // shared_ip 25 + young_withdrawal 40
		t.Fatalf("score %d", score)
	}
	// the same signals are not counted twice
	if f, _, _ := e.d.Risk.Scan(ctx); f != 0 {
		t.Fatalf("a signal scores once per player: %d", f)
	}
	_ = e.d.Pool.QueryRow(ctx, `SELECT risk_score FROM players WHERE id=$1`, solo.ID).Scan(&score)
	if score != 0 {
		t.Fatalf("an ordinary player stays clean: %d", score)
	}

	// with a threshold the riskiest account is held, quietly (the player is not told why)
	e.setSetting(settings.KeyRiskHold, 60)
	_, held, _ = e.d.Risk.Scan(ctx)
	if held != 1 || len(alerts) != 1 || !strings.Contains(alerts[0], fmt.Sprint(ring[0].ID)) {
		t.Fatalf("held=%d alerts=%v", held, alerts)
	}
	e.give(ring[0], ledger.Coin, 100000)
	e.exec(`UPDATE withdrawals SET status='CANCELLED'`)
	if r := e.wd(ring[0], 20000, walletAddr(9)); r.Code(t) != "UNDER_REVIEW" {
		t.Fatalf("a held account cannot withdraw: %s", r.Body)
	}
	if r := e.wd(ring[1], 20000, walletAddr(9)); r.Code(t) == "UNDER_REVIEW" {
		t.Fatal("only the account over the threshold is held")
	}
	// support reviews the list and clears the player
	list := e.do("GET", "/api/admin/risk", sup, nil).JSON(t)["players"].([]any)
	if len(list) < 5 || list[0].(map[string]any)["id"].(float64) != float64(ring[0].ID) || list[0].(map[string]any)["holdWithdrawals"] != true {
		t.Fatalf("risk list: %v", list[0])
	}
	if r := e.do("POST", fmt.Sprintf("/api/admin/players/%d/risk/clear", ring[0].ID), sup, nil); r.Status != 200 {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	_, held, _ = e.d.Risk.Scan(ctx)
	_ = e.d.Pool.QueryRow(ctx, `SELECT risk_score FROM players WHERE id=$1`, ring[0].ID).Scan(&score)
	var hold bool
	_ = e.d.Pool.QueryRow(ctx, `SELECT hold_withdrawals FROM players WHERE id=$1`, ring[0].ID).Scan(&hold)
	if score != 0 || hold || held != 0 {
		t.Fatalf("a cleared player is left alone for a while: score=%d hold=%v held=%d", score, hold, held)
	}
	if r := e.wd(ring[0], 20000, walletAddr(9)); r.Status == 403 {
		t.Fatalf("cleared: %s", r.Body)
	}
}

func TestRiskReferralBurstAndAdFlood(t *testing.T) {
	e := newEnv(t)
	e.gameConfig()
	ctx := context.Background()
	boss := e.login(830, "en", "")
	for i := 0; i < 10; i++ {
		e.login(int64(831+i), "en", fmt.Sprintf("ref_%d", boss.ID))
	}
	grinder := e.login(850, "en", "")
	for i := 0; i < 300; i++ {
		e.exec(`INSERT INTO ad_views (id, player_id, placement, status, created_at, expires_at) VALUES ($1,$2,'COLLECT','USED',$3,$3)`, fmt.Sprintf("ad%d", i), grinder.ID, e.now)
	}
	if _, _, err := e.d.Risk.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	var flags string
	_ = e.d.Pool.QueryRow(ctx, `SELECT flags::text FROM players WHERE id=$1`, boss.ID).Scan(&flags)
	if !strings.Contains(flags, "referral_burst") {
		t.Fatalf("an invite burst: %s", flags)
	}
	_ = e.d.Pool.QueryRow(ctx, `SELECT flags::text FROM players WHERE id=$1`, grinder.ID).Scan(&flags)
	if !strings.Contains(flags, "ad_flood") {
		t.Fatalf("an ad flood: %s", flags)
	}
}

func TestPrivacyDeletionAnonymisesButKeepsTheMoneyTrail(t *testing.T) {
	e := newEnv(t)
	e.gamesConfig()
	e.wdConfig()
	fake := e.botEnv()
	_ = fake
	root := e.adminLogin("root@example.com", admin.RoleSuper)
	sup := e.adminLogin("sup@example.com", admin.RoleSupport)
	p := e.login(860, "ru", "")
	e.reach(p)
	e.give(p, ledger.Coin, 100000)
	e.give(p, ledger.Star, 500)
	e.spin(p, "TIER_1", 1)
	r := e.wd(p, 20000, walletAddr(9))
	wid := int(r.JSON(t)["withdrawal"].(map[string]any)["id"].(float64))
	body := map[string]any{"confirm": true}
	path := fmt.Sprintf("/api/admin/players/%d/delete", p.ID)

	if r := e.do("POST", path, sup, body); r.Status != 403 {
		t.Fatalf("only a super admin deletes: %d", r.Status)
	}
	if r := e.do("POST", path, root, map[string]any{"confirm": false}); r.Status != 400 {
		t.Fatalf("needs confirmation: %d", r.Status)
	}
	if r := e.do("POST", path, root, body); r.Status != 400 {
		t.Fatalf("an open withdrawal blocks deletion: %d %s", r.Status, r.Body)
	}
	e.do("POST", fmt.Sprintf("/api/admin/withdrawals/%d/complete", wid), e.adminLogin("fin@example.com", admin.RoleFinance), map[string]any{"txHash": "h"})
	if r := e.do("POST", path, root, body); r.Status != 200 {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	ctx := context.Background()
	var user, first string
	var tg int64
	_ = e.d.Pool.QueryRow(ctx, `SELECT username, first_name, telegram_id FROM players WHERE id=$1`, p.ID).Scan(&user, &first, &tg)
	if user != "" || first != "Deleted" || tg != -p.ID {
		t.Fatalf("personal data erased: %q %q %d", user, first, tg)
	}
	if e.countRows("notifications") != 0 || e.countRows("bot_users") != 0 {
		t.Fatal("messages and bot records are erased")
	}
	// the money trail stays
	var rows, wds int
	_ = e.d.Pool.QueryRow(ctx, `SELECT count(*) FROM ledger_entries WHERE player_id=$1`, p.ID).Scan(&rows)
	_ = e.d.Pool.QueryRow(ctx, `SELECT count(*) FROM withdrawals WHERE player_id=$1`, p.ID).Scan(&wds)
	if rows == 0 || wds != 1 {
		t.Fatalf("ledger rows %d withdrawals %d must be kept", rows, wds)
	}
	var feedName string
	_ = e.d.Pool.QueryRow(ctx, `SELECT username FROM winners_feed WHERE player_id=$1 LIMIT 1`, p.ID).Scan(&feedName)
	if feedName != "" {
		t.Fatalf("the winners feed no longer names them: %q", feedName)
	}
	// the old session is dead, and the same Telegram user can start over as a new player
	if r := e.do("GET", "/api/me", p.Token, nil); r.Status != 401 {
		t.Fatalf("the deleted player's token: %d", r.Status)
	}
	again := e.login(860, "ru", "")
	if again.ID == p.ID || e.coins(again) != 0 {
		t.Fatalf("a fresh account: %d vs %d", again.ID, p.ID)
	}
}

func TestLedgerAuditAndOpsAlerts(t *testing.T) {
	e := newEnv(t)
	e.gamesConfig()
	e.wdConfig()
	ctx := context.Background()
	fin := e.adminLogin("fin@example.com", admin.RoleFinance)
	p := e.login(870, "en", "")
	e.give(p, ledger.Coin, 100000)
	e.give(p, ledger.Star, 100)
	if m, err := e.d.Risk.AuditLedger(ctx, 10); err != nil || len(m) != 0 {
		t.Fatalf("consistent books: %v %v", m, err)
	}
	if r := e.do("GET", "/api/admin/ledger/audit", fin, nil).JSON(t); r["ok"] != true {
		t.Fatalf("%v", r)
	}
	// someone edits a balance behind the ledger's back
	e.exec(`UPDATE players SET coins = coins + 7 WHERE id=$1`, p.ID)
	m, _ := e.d.Risk.AuditLedger(ctx, 10)
	if len(m) != 1 || m[0].Currency != "COIN" || m[0].Balance-m[0].Ledger != 7 {
		t.Fatalf("mismatch: %v", m)
	}
	if r := e.do("GET", "/api/admin/ledger/audit", fin, nil).JSON(t); r["ok"] != false {
		t.Fatalf("%v", r)
	}
	e.exec(`UPDATE players SET coins = coins - 7 WHERE id=$1`, p.ID)

	// operational alerts fire once per hour per condition
	var alerts []string
	e.d.Ops.Alert = func(ctx context.Context, text string) { alerts = append(alerts, text) }
	if n, _ := e.d.Ops.Check(ctx); n != 0 {
		t.Fatalf("all quiet: %d %v", n, alerts)
	}
	r := e.wd(p, 20000, walletAddr(9))
	id := int(r.JSON(t)["withdrawal"].(map[string]any)["id"].(float64))
	e.exec(`UPDATE withdrawals SET needs_review=TRUE, status='PROCESSING' WHERE id=$1`, id)
	if n, _ := e.d.Ops.Check(ctx); n != 1 || !strings.Contains(alerts[0], "need a human decision") {
		t.Fatalf("review alert: %d %v", n, alerts)
	}
	if n, _ := e.d.Ops.Check(ctx); n != 0 {
		t.Fatal("not again within the hour")
	}
	e.now = e.now.Add(61 * time.Minute)
	if n, _ := e.d.Ops.Check(ctx); n != 1 {
		t.Fatal("again in the next hour while it is still unresolved")
	}
	// a late withdrawal
	e.exec(`UPDATE withdrawals SET needs_review=FALSE, status='PENDING', created_at = $2 WHERE id=$1`, id, e.now.Add(-49*time.Hour))
	if n, _ := e.d.Ops.Check(ctx); n != 1 || !strings.Contains(alerts[len(alerts)-1], "longer than 48 hours") {
		t.Fatalf("late alert: %v", alerts)
	}
}
