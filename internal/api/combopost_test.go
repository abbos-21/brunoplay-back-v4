package api_test

import (
	"bytes"
	"context"
	"errors"
	"image/png"
	"strings"
	"testing"
	"time"

	"brunoplay/internal/admin"
	"brunoplay/internal/combopost"
	"brunoplay/internal/settings"
	"brunoplay/internal/telegram"
)

const ambassadors = "@bruno_ambassadors"

// comboFake gives the combo poster a fake Telegram and returns it
func (e *env) comboFake() *telegram.FakeAPI {
	e.t.Helper()
	f := telegram.NewFake()
	e.d.ComboPost = combopost.New(e.d.Pool, e.d.Settings, f, e.d.Daily.TodayPattern, "https://t.me/test_bot/play")
	e.d.ComboPost.Now = e.d.Now
	return f
}

func TestComboPostGoesToTheAmbassadorsChannelOncePerDay(t *testing.T) {
	e := newEnv(t)
	fake := e.comboFake()
	ctx := context.Background()
	run := func() error { return e.d.ComboPost.Run(ctx) }

	// switched off, then switched on without a channel: nothing is posted
	if err := run(); err != nil || len(fake.Photos) != 0 {
		t.Fatalf("off: %v %d", err, len(fake.Photos))
	}
	e.setSetting(settings.KeyAmbassadorsOn, true)
	if err := run(); err != nil || len(fake.Photos) != 0 || e.countRows("combo_posts") != 0 {
		t.Fatalf("no channel: %v %d", err, len(fake.Photos))
	}

	// a channel: today's combo is posted, once
	e.setSetting(settings.KeyAmbassadorsChat, ambassadors)
	if err := run(); err != nil {
		t.Fatal(err)
	}
	if err := run(); err != nil { // asking again must not post again
		t.Fatal(err)
	}
	if len(fake.Photos) != 1 {
		t.Fatalf("one post: %d", len(fake.Photos))
	}
	post := fake.Photos[0]
	if post.Chat != ambassadors {
		t.Fatalf("channel: %s", post.Chat)
	}
	img, err := png.Decode(bytes.NewReader(post.PNG))
	if err != nil || img.Bounds().Dx() < 400 {
		t.Fatalf("the picture is a PNG: %v", err)
	}
	// the caption names the real combo of the day, in order, in both languages
	var items []int16
	if err := e.d.Pool.QueryRow(ctx, `SELECT items FROM combo_patterns WHERE day=$1`, e.now.UTC().Truncate(24*time.Hour)).Scan(&items); err != nil || len(items) != 4 {
		t.Fatalf("the day's pattern exists: %v %v", err, items)
	}
	pos := 0
	for _, id := range items {
		var nameEN string
		if err := e.d.Pool.QueryRow(ctx, `SELECT name_en FROM combo_images WHERE id=$1`, id).Scan(&nameEN); err != nil {
			t.Fatal(err)
		}
		at := strings.Index(post.Caption[pos:], nameEN)
		if at < 0 {
			t.Fatalf("%q must come in order in %q", nameEN, post.Caption)
		}
		pos += at
	}
	if !strings.Contains(post.Caption, "Ежедневное комбо") || !strings.Contains(post.Caption, "Daily combo") {
		t.Fatalf("both languages: %s", post.Caption)
	}
	if len(post.Buttons) != 1 || !strings.HasPrefix(post.Buttons[0][0].URL, "https://t.me/") {
		t.Fatalf("a link button to the game: %+v", post.Buttons)
	}
	if len(fake.Pins) != 1 || len(fake.Unpins) != 0 {
		t.Fatalf("pinned: %v %v", fake.Pins, fake.Unpins)
	}

	// the next game day: a new post, the old one is unpinned
	e.now = e.now.Add(24 * time.Hour)
	if err := run(); err != nil {
		t.Fatal(err)
	}
	if len(fake.Photos) != 2 || len(fake.Pins) != 2 || len(fake.Unpins) != 1 {
		t.Fatalf("day two: %d photos, pins %v, unpins %v", len(fake.Photos), fake.Pins, fake.Unpins)
	}
}

func TestComboPostIsTriedAgainAfterAFailure(t *testing.T) {
	e := newEnv(t)
	fake := e.comboFake()
	ctx := context.Background()
	e.setSetting(settings.KeyAmbassadorsOn, true)
	e.setSetting(settings.KeyAmbassadorsChat, ambassadors)

	fake.FailChat = errors.New("telegram sendPhoto: 400 chat not found")
	if err := e.d.ComboPost.Run(ctx); err == nil {
		t.Fatal("the failure is reported")
	}
	if len(fake.Photos) != 0 {
		t.Fatal("nothing was posted")
	}
	// the day stays claimed for a few minutes (no hammering), then it is tried again
	fake.FailChat = nil
	if err := e.d.ComboPost.Run(ctx); err != nil || len(fake.Photos) != 0 {
		t.Fatalf("not at once: %v %d", err, len(fake.Photos))
	}
	e.exec(`UPDATE combo_posts SET claimed_at = now() - interval '10 minutes'`)
	if err := e.d.ComboPost.Run(ctx); err != nil || len(fake.Photos) != 1 {
		t.Fatalf("the retry posts: %v %d", err, len(fake.Photos))
	}
	if e.countRows("combo_posts") != 1 {
		t.Fatal("one row per day")
	}
}

func TestComboPostNowFromTheAdminPage(t *testing.T) {
	e := newEnv(t)
	fake := e.comboFake()
	super := e.adminLogin("owner@example.com", admin.RoleSuper)
	p := e.login(800, "en", "")

	if r := e.do("POST", "/api/admin/combo/post-now", p.Token, nil); r.Status == 200 {
		t.Fatal("a player cannot post")
	}
	if r := e.do("POST", "/api/admin/combo/post-now", super, nil); r.Status != 503 {
		t.Fatalf("no channel yet: %d %s", r.Status, r.Body)
	}
	e.setSetting(settings.KeyAmbassadorsChat, ambassadors)
	// works while the daily switch is off, and again (a repeat unpins the earlier post)
	for i := 0; i < 2; i++ {
		r := e.do("POST", "/api/admin/combo/post-now", super, nil)
		if r.Status != 200 || r.JSON(t)["pinned"] != true {
			t.Fatalf("post now: %d %s", r.Status, r.Body)
		}
	}
	if len(fake.Photos) != 2 || len(fake.Unpins) != 1 || e.countRows("combo_posts") != 1 {
		t.Fatalf("photos=%d unpins=%v rows=%d", len(fake.Photos), fake.Unpins, e.countRows("combo_posts"))
	}
	// a bot that may not pin still posts; the admin is told
	fake.FailPin = errors.New("telegram pinChatMessage: 400 not enough rights")
	r := e.do("POST", "/api/admin/combo/post-now", super, nil)
	if r.Status != 200 || r.JSON(t)["pinned"] == true || r.JSON(t)["pinError"] == nil {
		t.Fatalf("pin failure is reported: %d %s", r.Status, r.Body)
	}
}
