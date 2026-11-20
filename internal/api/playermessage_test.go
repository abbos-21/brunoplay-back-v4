package api_test

import (
	"fmt"
	"strings"
	"testing"

	"brunoplay/internal/admin"
	"brunoplay/internal/telegram"
)

func TestAdminCanMessageOnePlayerThroughTheBot(t *testing.T) {
	e := newEnv(t)
	fake := telegram.NewFake()
	e.d.TG = fake
	super := e.adminLogin("owner@example.com", admin.RoleSuper)
	p := e.login(860, "en", "")
	var id int64
	if err := e.d.Pool.QueryRow(t.Context(), `SELECT id FROM players WHERE telegram_id=860`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	url := fmt.Sprintf("/api/admin/players/%d/message", id)

	if r := e.do("POST", url, p.Token, map[string]any{"text": "hi"}); r.Status == 200 {
		t.Fatal("a player cannot send")
	}
	if r := e.do("POST", url, super, map[string]any{"text": "   "}); r.Status == 200 {
		t.Fatal("an empty message is refused")
	}
	r := e.do("POST", url, super, map[string]any{"text": "Hello <b>there</b> & welcome"})
	m := fake.MessagesTo(860)
	if r.Status != 200 || len(m) != 1 || m[0].Text != "Hello &lt;b&gt;there&lt;/b&gt; &amp; welcome" {
		t.Fatalf("sent as typed: %d %s %v", r.Status, r.Body, m)
	}

	// the player blocked the bot: the admin is told, nothing else happens
	fake.FailSend[860] = telegram.ErrBlocked
	r = e.do("POST", url, super, map[string]any{"text": "again"})
	if r.Status != 409 || !strings.Contains(string(r.Body), "blocked the bot") {
		t.Fatalf("blocked: %d %s", r.Status, r.Body)
	}
	var blocked bool
	_ = e.d.Pool.QueryRow(t.Context(), `SELECT bot_blocked FROM players WHERE id=$1`, id).Scan(&blocked)
	if !blocked {
		t.Fatal("the player is marked as having blocked the bot")
	}
	// never started the bot
	fake.FailSend[860] = telegram.ErrNotFound
	if r = e.do("POST", url, super, map[string]any{"text": "x"}); r.Status != 409 || !strings.Contains(string(r.Body), "not started the bot") {
		t.Fatalf("not started: %d %s", r.Status, r.Body)
	}
}
