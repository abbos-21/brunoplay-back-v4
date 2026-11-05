package api_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"brunoplay/internal/admin"
	"brunoplay/internal/ledger"
	"brunoplay/internal/payoutfeed"
	"brunoplay/internal/settings"
	"brunoplay/internal/telegram"
)

const feedChannel = "@brunoplay_payouts"

// feedHash is a valid-looking transaction hash (64 hex characters)
var feedHash = strings.Repeat("ab12", 16)

func TestPayoutsChannelPostsEverySuccessfulPayout(t *testing.T) {
	e := newEnv(t)
	e.wdConfig()
	fake := telegram.NewFake()
	e.d.PayoutFeed.SetSender(fake)
	e.d.PayoutFeed.PlayURL = "https://t.me/test_bot/play"
	fin := e.adminLogin("fin@example.com", admin.RoleFinance)
	ctx := context.Background()
	run := func() {
		t.Helper()
		if err := e.d.PayoutFeed.Run(ctx); err != nil {
			t.Fatal(err)
		}
	}
	// withdraws 20,000 coins as player `tg` and completes it with the given hash
	paid := func(tg int64, hash string) {
		t.Helper()
		p := e.login(tg, "en", "")
		e.give(p, ledger.Coin, 100000)
		r := e.wd(p, 20000, walletAddr(9))
		if r.Status != 200 {
			t.Fatalf("withdraw: %d %s", r.Status, r.Body)
		}
		id := int(r.JSON(t)["withdrawal"].(map[string]any)["id"].(float64))
		if c := e.do("POST", fmt.Sprintf("/api/admin/withdrawals/%d/complete", id), fin, map[string]any{"txHash": hash}); c.Status != 200 {
			t.Fatalf("complete: %d %s", c.Status, c.Body)
		}
	}

	// no channel set: nothing is queued
	paid(600, feedHash)
	run()
	if e.countRows("payout_posts") != 0 || len(fake.ChatPosts) != 0 {
		t.Fatal("with no channel nothing is posted")
	}

	// a channel: the next paid withdrawal is posted, once, without the player's real name
	e.setSetting(settings.KeyPayoutsChannel, feedChannel)
	paid(601, feedHash)
	run()
	run() // asking again must not post again
	if len(fake.ChatPosts) != 1 {
		t.Fatalf("one post: %d", len(fake.ChatPosts))
	}
	post := fake.ChatPosts[0]
	if post.Chat != feedChannel || !strings.Contains(post.Text, "Payout sent") || !strings.Contains(post.Text, "0.195 TON") || !strings.Contains(post.Text, "20,000 coins") {
		t.Fatalf("the post: %+v", post)
	}
	if strings.Contains(post.Text, "user601") || !strings.Contains(post.Text, payoutfeed.MaskName("@user601")) {
		t.Fatalf("the name must be masked: %q", post.Text)
	}
	if !strings.Contains(post.Text, `href="https://tonviewer.com/transaction/`+feedHash+`"`) {
		t.Fatalf("the transaction link: %q", post.Text)
	}
	if len(post.Buttons) != 1 || !strings.HasPrefix(post.Buttons[0][0].URL, "https://t.me/") {
		t.Fatalf("the play button: %+v", post.Buttons)
	}

	// a hash that does not look like a hash gets no link (a wrong link in a public channel is worse than none)
	paid(602, "HASH1")
	run()
	if len(fake.ChatPosts) != 2 || strings.Contains(fake.ChatPosts[1].Text, "tonviewer") {
		t.Fatalf("no link for a strange hash: %+v", fake.ChatPosts)
	}

	// names can be hidden completely, and TON posts can be switched off
	e.setSetting(settings.KeyPayoutsNames, false)
	paid(603, feedHash)
	run()
	if len(fake.ChatPosts) != 3 || !strings.Contains(fake.ChatPosts[2].Text, "👤 Player") || strings.Contains(fake.ChatPosts[2].Text, "***") {
		t.Fatalf("names hidden: %q", fake.ChatPosts[2].Text)
	}
	e.setSetting(settings.KeyPayoutsTon, false)
	paid(604, feedHash)
	run()
	if len(fake.ChatPosts) != 3 {
		t.Fatal("TON posts are switched off")
	}
	e.setSetting(settings.KeyPayoutsTon, true)
	e.setSetting(settings.KeyPayoutsNames, true)

	// a failed send is kept and retried later, and goes out when Telegram works again
	fake.FailChat = errors.New("telegram: 403 bot is not a member of the channel")
	paid(605, feedHash)
	run()
	var status string
	var attempts int
	_ = e.d.Pool.QueryRow(ctx, `SELECT status, attempts FROM payout_posts ORDER BY id DESC LIMIT 1`).Scan(&status, &attempts)
	if status != "PENDING" || attempts != 1 || len(fake.ChatPosts) != 3 {
		t.Fatalf("a failed post waits: %s attempts=%d posts=%d", status, attempts, len(fake.ChatPosts))
	}
	fake.FailChat = nil
	run() // the retry is not due yet
	if len(fake.ChatPosts) != 3 {
		t.Fatal("the retry waits for its time")
	}
	e.now = e.now.Add(3 * time.Minute)
	run()
	if len(fake.ChatPosts) != 4 {
		t.Fatalf("the retry: %d posts", len(fake.ChatPosts))
	}

	// delivered star withdrawals are posted too
	e.setSetting(settings.KeyStarWdMinLevel, 1)
	e.setSetting(settings.KeyStarWdMinAgeDays, 0)
	sp := e.login(610, "en", "")
	e.give(sp, ledger.Star, 1000)
	if r := e.do("POST", "/api/star-withdrawals", sp.Token, map[string]any{"stars": 300}); r.Status != 200 {
		t.Fatalf("star withdrawal: %d %s", r.Status, r.Body)
	}
	q := e.do("GET", "/api/admin/star-withdrawals", fin, nil).JSON(t)["withdrawals"].([]any)
	id := int(q[0].(map[string]any)["id"].(float64))
	if r := e.do("POST", fmt.Sprintf("/api/admin/star-withdrawals/%d/deliver", id), fin, nil); r.Status != 200 {
		t.Fatalf("deliver: %d %s", r.Status, r.Body)
	}
	run()
	if last := fake.ChatPosts[len(fake.ChatPosts)-1]; len(fake.ChatPosts) != 5 || !strings.Contains(last.Text, "300 Stars") || strings.Contains(last.Text, "user610") {
		t.Fatalf("star post: %+v", fake.ChatPosts)
	}

	// the admin's test button
	if r := e.do("POST", "/api/admin/payout-feed/test", fin, nil); r.Status != 200 || !strings.Contains(fake.ChatPosts[len(fake.ChatPosts)-1].Text, "payouts channel works") {
		t.Fatalf("test post: %d %s", r.Status, r.Body)
	}
	fake.FailChat = errors.New("telegram: 400 chat not found")
	if r := e.do("POST", "/api/admin/payout-feed/test", fin, nil); r.Code(t) != "ADMIN_ACTION_REFUSED" || !strings.Contains(string(r.Body), "administrator") {
		t.Fatalf("a failed test post says why: %d %s", r.Status, r.Body)
	}
	if r := e.do("POST", "/api/admin/payout-feed/test", sp.Token, nil); r.Status < 400 {
		t.Fatal("a player cannot use the admin test")
	}
}

func TestMaskName(t *testing.T) {
	for in, want := range map[string]string{"@bruno": "b***o", "Bruno": "B***o", "Al": "A***", "A": "A***", "": "Player", "@": "Player", "Иван": "И***н"} {
		if got := payoutfeed.MaskName(in); got != want {
			t.Errorf("MaskName(%q) = %q, want %q", in, got, want)
		}
	}
}

// Payouts that were paid before the channel existed are posted afterwards, oldest first, with their date, spaced out, and never twice.
func TestPayoutsBackfillPostsPastPayoutsOnce(t *testing.T) {
	e := newEnv(t)
	e.wdConfig()
	fake := telegram.NewFake()
	e.d.PayoutFeed.SetSender(fake)
	e.d.PayoutFeed.PlayURL = "https://t.me/test_bot/play"
	fin := e.adminLogin("fin@example.com", admin.RoleFinance)
	ctx := context.Background()
	e.setSetting(settings.KeyStarWdMinLevel, 1)
	e.setSetting(settings.KeyStarWdMinAgeDays, 0)

	paidTon := func(tg int64) {
		t.Helper()
		p := e.login(tg, "en", "")
		e.give(p, ledger.Coin, 100000)
		r := e.wd(p, 20000, walletAddr(9))
		id := int(r.JSON(t)["withdrawal"].(map[string]any)["id"].(float64))
		if c := e.do("POST", fmt.Sprintf("/api/admin/withdrawals/%d/complete", id), fin, map[string]any{"txHash": feedHash}); c.Status != 200 {
			t.Fatalf("complete: %d %s", c.Status, c.Body)
		}
	}
	// three payouts, two hours apart, while no channel is set
	paidTon(700)
	e.now = e.now.Add(2 * time.Hour)
	fin = e.adminRelogin("fin@example.com") // the admin's session is short
	paidTon(701)
	e.now = e.now.Add(2 * time.Hour)
	fin = e.adminRelogin("fin@example.com")
	sp := e.login(702, "en", "")
	e.give(sp, ledger.Star, 1000)
	e.do("POST", "/api/star-withdrawals", sp.Token, map[string]any{"stars": 300})
	q := e.do("GET", "/api/admin/star-withdrawals", fin, nil).JSON(t)["withdrawals"].([]any)
	e.do("POST", fmt.Sprintf("/api/admin/star-withdrawals/%d/deliver", int(q[0].(map[string]any)["id"].(float64))), fin, nil)
	if e.countRows("payout_posts") != 0 {
		t.Fatal("nothing was queued without a channel")
	}

	if _, err := e.d.PayoutFeed.Backfill(ctx, true, 5*time.Second); err == nil {
		t.Fatal("a backfill needs the channel to be set")
	}
	e.setSetting(settings.KeyPayoutsChannel, feedChannel)

	// a dry run counts and writes nothing
	rep, err := e.d.PayoutFeed.Backfill(ctx, false, 5*time.Second)
	if err != nil || rep.Ton != 2 || rep.Stars != 1 || e.countRows("payout_posts") != 0 {
		t.Fatalf("dry run: %+v %v rows=%d", rep, err, e.countRows("payout_posts"))
	}
	// applying queues them, spaced 5 seconds apart: none is due at once, all three are due 15 seconds later
	if rep, err = e.d.PayoutFeed.Backfill(ctx, true, 5*time.Second); err != nil || !rep.Applied || e.countRows("payout_posts") != 3 {
		t.Fatalf("apply: %+v %v rows=%d", rep, err, e.countRows("payout_posts"))
	}
	if err := e.d.PayoutFeed.Run(ctx); err != nil || len(fake.ChatPosts) != 0 {
		t.Fatalf("nothing is due yet: %d posts %v", len(fake.ChatPosts), err)
	}
	e.now = e.now.Add(6 * time.Second)
	_ = e.d.PayoutFeed.Run(ctx)
	if len(fake.ChatPosts) != 1 {
		t.Fatalf("one post every 5 seconds: %d", len(fake.ChatPosts))
	}
	e.now = e.now.Add(20 * time.Second)
	_ = e.d.PayoutFeed.Run(ctx)
	if len(fake.ChatPosts) != 3 {
		t.Fatalf("all three: %d", len(fake.ChatPosts))
	}
	for i, p := range fake.ChatPosts {
		if !strings.Contains(p.Text, "📅") || !strings.Contains(p.Text, "Payout sent") {
			t.Fatalf("post %d has no date: %q", i, p.Text)
		}
	}
	if !strings.Contains(fake.ChatPosts[0].Text, "0.195 TON") || !strings.Contains(fake.ChatPosts[1].Text, "0.195 TON") || !strings.Contains(fake.ChatPosts[2].Text, "300 Stars") {
		t.Fatalf("oldest first (two TON, then the star payout): %+v", fake.ChatPosts)
	}
	// again: nothing is posted twice
	if rep, err = e.d.PayoutFeed.Backfill(ctx, true, 5*time.Second); err != nil || rep.Ton+rep.Stars != 0 {
		t.Fatalf("second run: %+v %v", rep, err)
	}
	if !strings.Contains(rep.Summary(5*time.Second), "Nothing to post") {
		t.Fatalf("summary: %s", rep.Summary(5*time.Second))
	}
}
