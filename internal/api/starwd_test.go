package api_test

import (
	"fmt"
	"testing"

	"brunoplay/internal/admin"
	"brunoplay/internal/ledger"
	"brunoplay/internal/settings"
)

// A star withdrawal takes the stars at once; an admin delivers it, or cancels it and the stars come back.
func TestStarWithdrawalFlow(t *testing.T) {
	e := newEnv(t)
	p := e.login(540, "en", "")
	fin := e.adminLogin("root@example.com", admin.RoleSuper)
	e.give(p, ledger.Star, 1000)

	// the rules: the minimum and the player level come from settings
	info := e.do("GET", "/api/star-withdrawals/info", p.Token, nil).JSON(t)
	if info["minStars"].(float64) != 100 || info["canWithdraw"] != false || info["blockedReason"] != "WITHDRAWAL_LEVEL_TOO_LOW" || info["notice"] == "" {
		t.Fatalf("a new player is held back by the level and age rules: %v", info)
	}
	if r := e.do("POST", "/api/star-withdrawals", p.Token, map[string]any{"stars": 300}); r.Code(t) != "WITHDRAWAL_LEVEL_TOO_LOW" {
		t.Fatalf("%s", r.Body)
	}
	e.setSetting(settings.KeyStarWdMinLevel, 1)
	e.setSetting(settings.KeyStarWdMinAgeDays, 0)
	if r := e.do("POST", "/api/star-withdrawals", p.Token, map[string]any{"stars": 50}); r.Code(t) != "STAR_WITHDRAWAL_BELOW_MIN" {
		t.Fatalf("below the minimum: %s", r.Body)
	}
	if r := e.do("POST", "/api/star-withdrawals", p.Token, map[string]any{"stars": 5000}); r.Code(t) != "WITHDRAWAL_LIMIT" {
		t.Fatalf("above the daily limit: %s", r.Body)
	}
	e.setSetting(settings.KeyStarWdDaily, 100000)
	e.setSetting(settings.KeyStarWdTotal, 100000)
	if r := e.do("POST", "/api/star-withdrawals", p.Token, map[string]any{"stars": 5000}); r.Code(t) != "INSUFFICIENT_STARS" {
		t.Fatalf("more than the balance: %s", r.Body)
	}
	if e.countRows("star_withdrawals") != 0 || e.stars(p) != 1000 {
		t.Fatal("a refused request leaves nothing behind")
	}

	// a request: the stars leave the balance at once, one open request at a time
	r := e.do("POST", "/api/star-withdrawals", p.Token, map[string]any{"stars": 300})
	if r.Status != 200 || e.stars(p) != 700 || e.ledgerSum(p, "STAR") != 700 {
		t.Fatalf("%d %s stars=%d", r.Status, r.Body, e.stars(p))
	}
	if r := e.do("POST", "/api/star-withdrawals", p.Token, map[string]any{"stars": 200}); r.Code(t) != "WITHDRAWAL_ALREADY_OPEN" {
		t.Fatalf("%s", r.Body)
	}
	mine := e.do("GET", "/api/star-withdrawals", p.Token, nil).JSON(t)["data"].([]any)
	if len(mine) != 1 || mine[0].(map[string]any)["status"] != "PENDING" || mine[0].(map[string]any)["stars"].(float64) != 300 {
		t.Fatalf("history: %v", mine)
	}

	// the admin sees it and delivers it
	q := e.do("GET", "/api/admin/star-withdrawals", fin, nil).JSON(t)["withdrawals"].([]any)
	if len(q) != 1 || q[0].(map[string]any)["username"] != "user540" {
		t.Fatalf("queue: %v", q)
	}
	id := int(q[0].(map[string]any)["id"].(float64))
	if r := e.do("POST", fmt.Sprintf("/api/admin/star-withdrawals/%d/deliver", id), p.Token, nil); r.Status < 400 {
		t.Fatalf("a player cannot deliver: %d", r.Status)
	}
	if r := e.do("POST", fmt.Sprintf("/api/admin/star-withdrawals/%d/deliver", id), fin, nil); r.Status != 200 {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	if r := e.do("POST", fmt.Sprintf("/api/admin/star-withdrawals/%d/deliver", id), fin, nil); r.Status < 400 {
		t.Fatal("delivered once only")
	}
	if r := e.do("POST", fmt.Sprintf("/api/admin/star-withdrawals/%d/cancel", id), fin, map[string]any{"reason": "late"}); r.Status < 400 {
		t.Fatal("a delivered request cannot be cancelled")
	}
	if e.stars(p) != 700 || e.countRows("notifications WHERE type='star_withdrawal_delivered'") != 1 {
		t.Fatalf("delivered: stars %d", e.stars(p))
	}

	// the next one is cancelled: the stars return and the player is told why
	e.do("POST", "/api/star-withdrawals", p.Token, map[string]any{"stars": 200})
	if e.stars(p) != 500 {
		t.Fatalf("200 taken: %d", e.stars(p))
	}
	q = e.do("GET", "/api/admin/star-withdrawals", fin, nil).JSON(t)["withdrawals"].([]any)
	id = int(q[0].(map[string]any)["id"].(float64))
	if r := e.do("POST", fmt.Sprintf("/api/admin/star-withdrawals/%d/cancel", id), fin, map[string]any{}); r.Status < 400 {
		t.Fatal("a reason is required")
	}
	if r := e.do("POST", fmt.Sprintf("/api/admin/star-withdrawals/%d/cancel", id), fin, map[string]any{"reason": "username is hidden"}); r.Status != 200 {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	if e.stars(p) != 700 || e.ledgerSum(p, "STAR") != 700 || e.countRows("notifications WHERE type='star_withdrawal_cancelled'") != 1 {
		t.Fatalf("refunded: stars %d", e.stars(p))
	}
	if r := e.do("POST", fmt.Sprintf("/api/admin/star-withdrawals/%d/cancel", id), fin, map[string]any{"reason": "again"}); r.Status < 400 || e.stars(p) != 700 {
		t.Fatal("refunded once only")
	}
	hist := e.do("GET", "/api/star-withdrawals", p.Token, nil).JSON(t)["data"].([]any)
	if len(hist) != 2 || hist[0].(map[string]any)["status"] != "CANCELLED" || hist[0].(map[string]any)["rejectReason"] != "username is hidden" {
		t.Fatalf("history: %v", hist)
	}

	// cancelled stars do not count against the limits; delivered ones do
	e.setSetting(settings.KeyStarWdDaily, 400)
	if r := e.do("POST", "/api/star-withdrawals", p.Token, map[string]any{"stars": 150}); r.Code(t) != "WITHDRAWAL_LIMIT" {
		t.Fatalf("300 delivered + 150 is over a daily limit of 400: %s", r.Body)
	}
	if r := e.do("POST", "/api/star-withdrawals", p.Token, map[string]any{"stars": 100}); r.Status != 200 {
		t.Fatalf("300 + 100 fits: %d %s", r.Status, r.Body)
	}

	// the switch
	e.setSetting(settings.KeyStarWdEnabled, false)
	if info := e.do("GET", "/api/star-withdrawals/info", p.Token, nil).JSON(t); info["canWithdraw"] != false {
		t.Fatalf("switched off: %v", info)
	}
}
