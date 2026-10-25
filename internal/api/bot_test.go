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
	"brunoplay/internal/telegram"
)

// botEnv wires the fake Telegram into every part and makes sending instant.
func (e *env) botEnv() *telegram.FakeAPI {
	fake := telegram.NewFake()
	e.d.TG = fake
	e.d.Topup.SetAPIs(fake, e.d.Chain)
	e.d.Tasks.SetTelegram(fake)
	e.d.Notify.SetAPI(fake)
	e.d.Notify.SetRate(100000)
	e.d.Notify.SetWebApp("https://app.example")
	e.d.Notify.Now = func() time.Time { return e.now }
	e.d.Bot.SetAPI(fake)
	e.d.Bot.SetWebApp("https://app.example")
	e.d.Cfg.BotWebhookSec = "whsecret"
	e.d.Cfg.WebAppURL = "https://app.example"
	return fake
}

// reach marks a player as someone the bot can write to.
func (e *env) reach(p player) {
	e.exec(`UPDATE players SET bot_started=TRUE, bot_blocked=FALSE WHERE id=$1`, p.ID)
}

func (e *env) sendDue() int {
	e.t.Helper()
	n, err := e.d.Notify.RunDue(context.Background(), 500)
	if err != nil {
		e.t.Fatal(err)
	}
	return n
}

func (e *env) notifStatus(p player, typ string) []string {
	rows, err := e.d.Pool.Query(context.Background(), `SELECT status || COALESCE(':' || NULLIF(reason,''), '') FROM notifications WHERE player_id=$1 AND type=$2 ORDER BY id`, p.ID, typ)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		_ = rows.Scan(&s)
		out = append(out, s)
	}
	return out
}

func (e *env) lastText(fake *telegram.FakeAPI, p player) string {
	m := fake.MessagesTo(p.TG)
	if len(m) == 0 {
		return ""
	}
	return m[len(m)-1].Text
}

func TestTankFullMessageIsScheduledAndSentOnTime(t *testing.T) {
	e := newEnv(t)
	e.gameConfig()
	fake := e.botEnv()
	p := e.login(600, "en", "")
	e.reach(p)
	e.do("POST", "/api/fishing/start", p.Token, map[string]any{"adViewId": e.watchAd(p, "FISHING_START")})
	if got := e.notifStatus(p, "tank_full"); len(got) != 1 || got[0] != "PENDING" {
		t.Fatalf("the message is queued for the moment the tank is full: %v", got)
	}
	e.now = e.now.Add(100 * time.Second)
	if e.sendDue() != 0 {
		t.Fatal("not full yet")
	}
	e.now = e.now.Add(101 * time.Second) // 5 coins at 0.025/s = 200 s
	if e.sendDue() != 1 {
		t.Fatalf("full now: %v", fake.Messages)
	}
	m := fake.MessagesTo(600)[0]
	if m.Text != "🎣 Your tank is full! Collect your coins." || len(m.Buttons) != 1 || m.Buttons[0][0].Text != "Collect" || m.Buttons[0][0].WebApp != "https://app.example/" {
		t.Fatalf("message: %+v", m)
	}
	if got := e.notifStatus(p, "tank_full"); got[0] != "SENT" {
		t.Fatalf("%v", got)
	}
}

func TestMessageIsCancelledWhenSituationChanges(t *testing.T) {
	e := newEnv(t)
	e.gameConfig()
	fake := e.botEnv()
	p := e.login(601, "en", "")
	e.reach(p)
	e.give(p, ledger.Coin, 10000)
	e.do("POST", "/api/fishing/start", p.Token, map[string]any{"adViewId": e.watchAd(p, "FISHING_START")})
	// the player collects early: "tank full" and "collect reminder" must disappear
	e.now = e.now.Add(120 * time.Second)
	if r := e.do("POST", "/api/fishing/collect", p.Token, map[string]any{"adViewId": e.watchAd(p, "COLLECT")}); r.Status != 200 {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	if got := e.notifStatus(p, "tank_full"); len(got) != 1 || !strings.HasPrefix(got[0], "CANCELLED") {
		t.Fatalf("tank_full must be cancelled by the collect: %v", got)
	}
	e.now = e.now.Add(10 * time.Hour)
	e.sendDue()
	for _, m := range fake.MessagesTo(601) {
		if strings.Contains(m.Text, "tank is full") || strings.Contains(m.Text, "coins are waiting") {
			t.Fatalf("a stale message was sent: %q", m.Text)
		}
	}
	// after the collect the idle reminder is queued (12 h)
	if got := e.notifStatus(p, "fishing_idle"); len(got) != 1 {
		t.Fatalf("idle reminder: %v", got)
	}
	// starting again cancels the idle reminder and the old session's messages
	e.do("POST", "/api/fishing/start", p.Token, map[string]any{"adViewId": e.watchAd(p, "FISHING_START")})
	if got := e.notifStatus(p, "fishing_idle"); !strings.HasPrefix(got[0], "CANCELLED") && !strings.HasPrefix(got[0], "SENT") {
		t.Fatalf("idle reminder after a new start: %v", got)
	}
	// an upgrade moves the full time: only the new time sends
	e.now = e.now.Add(time.Hour)
	e.do("POST", "/api/shop/cards/MINING_RATE/upgrade", p.Token, nil)
	pending := 0
	for _, s := range e.notifStatus(p, "tank_full") {
		if s == "PENDING" {
			pending++
		}
	}
	if pending != 1 {
		t.Fatalf("exactly one pending tank_full after the upgrade: %v", e.notifStatus(p, "tank_full"))
	}
}

func TestPreferencesMandatoryAndBlockedBot(t *testing.T) {
	e := newEnv(t)
	fake := e.botEnv()
	p := e.login(602, "ru", "")
	e.reach(p)
	ctx := context.Background()
	seq := 0
	send := func(typ string, params map[string]any) {
		seq++
		if err := e.d.Notify.Send(ctx, e.d.Pool, p.ID, typ, params, fmt.Sprintf("t:%s:%d", typ, seq)); err != nil {
			t.Fatal(err)
		}
	}
	// category switched off in the app
	if r := e.do("PATCH", "/api/me/notifications", p.Token, map[string]any{"off": []string{"fishing"}}); r.Status != 200 {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	if r := e.do("PATCH", "/api/me/notifications", p.Token, map[string]any{"off": []string{"payments"}}); r.Status != 422 {
		t.Fatalf("payments cannot be listed as off: %d", r.Status)
	}
	send("autocollect_done", map[string]any{"coins": 5})
	e.sendDue()
	if got := e.notifStatus(p, "autocollect_done"); got[0] != "SKIPPED:switched off by the player" {
		t.Fatalf("%v", got)
	}
	// everything off: payments still arrive, in Russian
	e.do("PATCH", "/api/me/notifications", p.Token, map[string]any{"allOff": true})
	send("task_new", nil)
	send("withdrawal_completed", map[string]any{"ton": "0.095", "tx": "abc<b>"})
	e.sendDue()
	if got := e.notifStatus(p, "task_new"); got[0] != "SKIPPED:switched off by the player" {
		t.Fatalf("%v", got)
	}
	txt := e.lastText(fake, p)
	if txt != "✅ Вывод 0.095 TON отправлен. Транзакция: abc&lt;b&gt;" {
		t.Fatalf("payment message in Russian with escaped text: %q", txt)
	}
	// the player blocked the bot: marked, and nothing more is attempted
	e.do("PATCH", "/api/me/notifications", p.Token, map[string]any{"allOff": false, "off": []string{}})
	fake.FailSend[602] = telegram.ErrBlocked
	send("task_new", nil)
	e.sendDue()
	var blocked bool
	_ = e.d.Pool.QueryRow(ctx, `SELECT bot_blocked FROM players WHERE id=$1`, p.ID).Scan(&blocked)
	if !blocked {
		t.Fatal("a blocked bot must be remembered")
	}
	fake.FailSend[602] = nil
	e.now = e.now.Add(time.Minute)
	send("withdrawal_failed", map[string]any{"coins": 1})
	e.sendDue()
	if got := e.notifStatus(p, "withdrawal_failed"); got[0] != "SKIPPED:bot not available" {
		t.Fatalf("%v", got)
	}
	// unblocking the bot (chat member update) makes the player reachable again
	e.tgUpdate(telegram.Update{MyChatMember: &telegram.ChatMemberUpdate{Chat: telegram.Chat{ID: 602, Type: "private"}, From: telegram.User{ID: 602}}})
	_ = e.d.Pool.QueryRow(ctx, `SELECT bot_blocked FROM players WHERE id=$1`, p.ID).Scan(&blocked)
	if blocked {
		t.Fatal("a chat member update with status member clears the block")
	}
}

func TestQuietHoursCooldownAndDailyCap(t *testing.T) {
	e := newEnv(t)
	fake := e.botEnv()
	p := e.login(603, "en", "")
	e.reach(p)
	ctx := context.Background()
	e.now = time.Date(2026, 10, 24, 23, 0, 0, 0, time.UTC)
	e.setSetting(settings.KeyNotifQuiet, map[string]int{"from": 22, "to": 7})
	_ = e.d.Notify.Send(ctx, e.d.Pool, p.ID, "task_new", nil, "q1")
	if e.sendDue() != 0 || len(fake.Messages) != 0 {
		t.Fatal("quiet hours: nothing is sent at night")
	}
	// payments ignore quiet hours
	_ = e.d.Notify.Send(ctx, e.d.Pool, p.ID, "withdrawal_cancelled", map[string]any{"coins": 5}, "q2")
	if e.sendDue() != 1 {
		t.Fatal("payments are sent at any hour")
	}
	e.now = time.Date(2026, 10, 25, 7, 0, 1, 0, time.UTC)
	if e.sendDue() != 1 {
		t.Fatal("the quiet message goes out when the night ends")
	}
	e.setSetting(settings.KeyNotifQuiet, map[string]int{"from": -1, "to": -1})

	// cooldown: the same type twice within its cooldown
	_ = e.d.Notify.Send(ctx, e.d.Pool, p.ID, "task_new", nil, "c1")
	e.sendDue()
	if got := e.notifStatus(p, "task_new"); got[len(got)-1] != "SKIPPED:cooldown" {
		t.Fatalf("%v", got)
	}
	// daily cap: two messages a day (payments excluded)
	e.setSetting(settings.KeyNotifDailyCap, 2)
	e.nextDay()
	for i, typ := range []string{"task_new", "tournament_start", "friend_joined"} {
		_ = e.d.Notify.Send(ctx, e.d.Pool, p.ID, typ, map[string]any{"name_en": "x", "name": "x"}, fmt.Sprintf("cap%d", i))
	}
	e.sendDue()
	var sent int
	_ = e.d.Pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE player_id=$1 AND status='SENT' AND sent_at >= $2`, p.ID, time.Date(e.now.Year(), e.now.Month(), e.now.Day(), 0, 0, 0, 0, time.UTC)).Scan(&sent)
	if sent != 2 {
		t.Fatalf("daily cap of 2: %d sent", sent)
	}
	_ = e.d.Notify.Send(ctx, e.d.Pool, p.ID, "withdrawal_cancelled", map[string]any{"coins": 1}, "cap-pay")
	if e.sendDue() != 1 {
		t.Fatal("payment messages are not counted by the cap")
	}
}

func TestSenderRetriesAndTelegramRateLimit(t *testing.T) {
	e := newEnv(t)
	fake := e.botEnv()
	p := e.login(604, "en", "")
	e.reach(p)
	ctx := context.Background()
	fake.FailSend[604] = telegram.RateLimited{RetryAfter: 30 * time.Second}
	_ = e.d.Notify.Send(ctx, e.d.Pool, p.ID, "withdrawal_cancelled", map[string]any{"coins": 1}, "r1")
	e.sendDue()
	var at time.Time
	var attempts int
	_ = e.d.Pool.QueryRow(ctx, `SELECT notify_at, attempts FROM notifications WHERE dedupe_key='r1'`).Scan(&at, &attempts)
	if at.Sub(e.now) < 30*time.Second || attempts != 0 {
		t.Fatalf("rate limit: retry after Telegram's wait, no attempt used: %v %d", at.Sub(e.now), attempts)
	}
	fake.FailSend[604] = fmt.Errorf("network down")
	for i := 0; i < 6; i++ {
		e.now = e.now.Add(time.Hour)
		e.sendDue()
	}
	if got := e.notifStatus(p, "withdrawal_cancelled"); got[0] != "FAILED:network down" {
		t.Fatalf("five failed attempts end as FAILED: %v", got)
	}
	// a recovered network sends a fresh message normally
	fake.FailSend[604] = nil
	_ = e.d.Notify.Send(ctx, e.d.Pool, p.ID, "withdrawal_cancelled", map[string]any{"coins": 2}, "r2")
	if e.sendDue() != 1 {
		t.Fatal("recovered")
	}
}

func TestValidatorsDropMessagesThatAreNotTrueAnymore(t *testing.T) {
	e := newEnv(t)
	e.gameConfig()
	e.setSetting(settings.KeyStreakRewards, []int{10, 20, 30, 40, 50, 60, 70, 80, 90, 100})
	e.setSetting(settings.KeyComboPerCorrect, 5)
	fake := e.botEnv()
	p := e.login(605, "en", "")
	e.reach(p)
	ctx := context.Background()
	// idle reminder while fishing is running is dropped
	e.do("POST", "/api/fishing/start", p.Token, map[string]any{"adViewId": e.watchAd(p, "FISHING_START")})
	_ = e.d.Notify.Send(ctx, e.d.Pool, p.ID, "fishing_idle", nil, "v1")
	// "daily reward ready" after the reward was claimed is dropped
	e.do("POST", "/api/daily/streak/claim", p.Token, map[string]any{})
	_ = e.d.Notify.Send(ctx, e.d.Pool, p.ID, "streak_ready", nil, "v2")
	_ = e.d.Notify.Send(ctx, e.d.Pool, p.ID, "streak_at_risk", map[string]any{"streak": 3}, "v3")
	// the combo is still open, so that one is true
	_ = e.d.Notify.Send(ctx, e.d.Pool, p.ID, "combo_ready", nil, "v4")
	// no open chest round
	_ = e.d.Notify.Send(ctx, e.d.Pool, p.ID, "chest_round_open", nil, "v5")
	e.sendDue()
	for typ, want := range map[string]string{"fishing_idle": "SKIPPED:no longer relevant", "streak_ready": "SKIPPED:no longer relevant", "streak_at_risk": "SKIPPED:no longer relevant",
		"combo_ready": "SENT", "chest_round_open": "SKIPPED:no longer relevant"} {
		if got := e.notifStatus(p, typ); len(got) == 0 || got[0] != want {
			t.Errorf("%s: %v want %s", typ, got, want)
		}
	}
	if len(fake.MessagesTo(605)) != 1 {
		t.Fatalf("only the true message went out: %v", fake.Messages)
	}
}

func TestBoosterMessagesFollowTheBooster(t *testing.T) {
	e := newEnv(t)
	e.gameConfig()
	fake := e.botEnv()
	p := e.login(606, "ru", "")
	e.reach(p)
	e.give(p, ledger.Star, 2000)
	e.do("POST", "/api/shop/boosters/AUTO_MINING/buy", p.Token, nil)
	if got := e.notifStatus(p, "booster_expiring"); len(got) != 2 {
		t.Fatalf("a message 3 days and 1 day before the end: %v", got)
	}
	e.now = e.now.Add(27*24*time.Hour + time.Hour) // 3 days before the end
	e.sendDue()
	if txt := e.lastText(fake, p); txt != "⏳ Бустер «Автосбор» закончится через 3 дн. Продлите его в магазине." {
		t.Fatalf("%q", txt)
	}
	e.now = e.now.Add(31 * 24 * time.Hour)
	e.sendDue()
	if txt := e.lastText(fake, p); txt != "⌛ Бустер «Автосбор» закончился. Продлите его в магазине." {
		t.Fatalf("%q", txt)
	}
}

func TestAutocollectAndMoneyMessages(t *testing.T) {
	e := newEnv(t)
	e.gameConfig()
	e.wdConfig()
	e.moneyConfig()
	fake := e.botEnv()
	p := e.login(607, "en", "")
	e.reach(p)
	e.give(p, ledger.Star, 1000)
	e.give(p, ledger.Coin, 100000)
	e.do("POST", "/api/fishing/start", p.Token, map[string]any{"adViewId": e.watchAd(p, "FISHING_START")})
	e.do("POST", "/api/shop/boosters/AUTO_MINING/buy", p.Token, nil)
	e.now = e.now.Add(time.Hour)
	e.d.Fishing.RunAutocollect(context.Background(), 10)
	e.sendDue()
	if txt := e.lastText(fake, p); txt != "⚙️ Autocollect gathered 5 coins for you and started a new run." {
		t.Fatalf("%q", txt)
	}
	// withdrawal created, then completed by the admin
	fin := e.adminLogin("fin@example.com", admin.RoleFinance)
	r := e.wd(p, 20000, walletAddr(9))
	id := int(r.JSON(t)["withdrawal"].(map[string]any)["id"].(float64))
	e.sendDue()
	if txt := e.lastText(fake, p); !strings.HasPrefix(txt, "📝 Withdrawal request of 20,000 coins (0.195 TON)") && !strings.Contains(txt, "0.195 TON") {
		t.Fatalf("created: %q", txt)
	}
	e.do("POST", fmt.Sprintf("/api/admin/withdrawals/%d/complete", id), fin, map[string]any{"txHash": "HASH1"})
	e.sendDue()
	if txt := e.lastText(fake, p); txt != "✅ Your withdrawal of 0.195 TON was sent. Transaction: HASH1" {
		t.Fatalf("completed: %q", txt)
	}
	// top-up credited
	o := e.do("POST", "/api/topup/telegram-invoice", p.Token, map[string]any{"packStars": 500}).JSON(t)
	e.tgUpdate(telegram.Update{Message: &telegram.Message{From: &telegram.User{ID: 607}, SuccessfulPayment: &telegram.SuccessfulPayment{Currency: "XTR", TotalAmount: 500, InvoicePayload: o["id"].(string), TelegramPaymentChargeID: "c1"}}})
	e.sendDue()
	if txt := e.lastText(fake, p); txt != "⭐ 500 stars were added to your balance." {
		t.Fatalf("top-up: %q", txt)
	}
}

func TestLevelAndFriendMessages(t *testing.T) {
	e := newEnv(t)
	e.gameConfig()
	e.setSetting(settings.KeyLevelRewards, []map[string]any{{"level": 2, "coins": 50, "stars": 1}})
	e.setSetting(settings.KeyRefLevelRewards, map[string]int{"2": 200})
	fake := e.botEnv()
	inviter := e.login(608, "en", "")
	e.reach(inviter)
	friend := e.login(609, "ru", fmt.Sprintf("ref_%d", inviter.ID))
	e.reach(friend)
	e.levelUp(friend, 2)
	e.sendDue()
	if txt := e.lastText(fake, friend); txt != "🏆 Уровень 2! Награда: 50 монет, 1 звёзд" {
		t.Fatalf("level up: %q", txt)
	}
	var texts []string
	for _, m := range fake.MessagesTo(608) {
		texts = append(texts, m.Text)
	}
	if len(texts) != 2 || texts[0] != "👥 @user609 reached level 2 and is now your friend!" || texts[1] != "🎁 Your friend @user609 reached level 2: +200 coins." {
		t.Fatalf("inviter messages: %v", texts)
	}
}

func TestDailyMessagesAtTheirOwnHourAndWarnings(t *testing.T) {
	e := newEnv(t)
	e.gameConfig()
	e.setSetting(settings.KeyStreakRewards, []int{10, 20, 30, 40, 50, 60, 70, 80, 90, 100})
	e.setSetting(settings.KeyComboPerCorrect, 5)
	fake := e.botEnv()
	ctx := context.Background()
	early, late, none := e.login(610, "en", ""), e.login(611, "ru", ""), e.login(612, "en", "")
	e.reach(early)
	e.reach(late)
	e.exec(`UPDATE players SET pref_hour=8 WHERE id=$1`, early.ID)
	e.exec(`UPDATE players SET pref_hour=20 WHERE id=$1`, late.ID)
	_ = none // never started the bot: gets nothing

	e.now = time.Date(2026, 10, 24, 7, 30, 0, 0, time.UTC)
	if n, _ := e.d.Notify.DailyReady(ctx); n != 0 {
		t.Fatalf("nobody's hour yet: %d", n)
	}
	e.now = time.Date(2026, 10, 24, 8, 5, 0, 0, time.UTC)
	if n, _ := e.d.Notify.DailyReady(ctx); n != 1 {
		t.Fatalf("08:00 is the early player's hour: %d", n)
	}
	if n, _ := e.d.Notify.DailyReady(ctx); n != 0 {
		t.Fatal("the batch of an hour runs once")
	}
	e.sendDue()
	if txt := e.lastText(fake, early); txt != "📅🧩 Your daily reward and the Daily Combo are ready!" {
		t.Fatalf("both are open: %q", txt)
	}
	// the other player at 20:00 in Russian; they already claimed the streak, so only the combo is mentioned
	e.now = time.Date(2026, 10, 24, 20, 0, 0, 0, time.UTC)
	e.do("POST", "/api/daily/streak/claim", late.Token, map[string]any{})
	_, _ = e.d.Notify.DailyReady(ctx)
	e.sendDue()
	if txt := e.lastText(fake, late); txt != "🧩 Ежедневное комбо готово — найдите верный порядок!" {
		t.Fatalf("%q", txt)
	}

	// build a streak of 3 days for the warning
	e.d.PlayerTokenTTL = 100000 * time.Hour
	for i := 0; i < 3; i++ {
		e.now = time.Date(2026, 10, 25+i, 10, 0, 0, 0, time.UTC)
		e.do("POST", "/api/daily/streak/claim", early.Token, map[string]any{})
	}
	e.now = time.Date(2026, 10, 28, 18, 5, 0, 0, time.UTC) // 6 h before the reset, streak alive but not claimed today
	if n, err := e.d.Notify.DailyWarnings(ctx); err != nil || n == 0 {
		t.Fatalf("the 6 h warning: %d %v", n, err)
	}
	e.sendDue()
	var got []string
	for _, m := range fake.MessagesTo(610) {
		got = append(got, m.Text)
	}
	joined := strings.Join(got, "|")
	if !strings.Contains(joined, "Don't lose your streak of 3 days") {
		t.Fatalf("streak warning: %v", got)
	}
	// the player claims: the 1 h warning is not sent
	e.do("POST", "/api/daily/streak/claim", early.Token, map[string]any{})
	e.now = time.Date(2026, 10, 28, 23, 5, 0, 0, time.UTC)
	before := len(fake.MessagesTo(610))
	_, _ = e.d.Notify.DailyWarnings(ctx)
	e.sendDue()
	for _, m := range fake.MessagesTo(610)[before:] {
		if strings.Contains(m.Text, "streak") {
			t.Fatalf("no streak warning after claiming: %q", m.Text)
		}
	}
	// streak lost: the next-next day
	e.exec(`UPDATE daily_streaks SET last_claim_day = $1, current = 4 WHERE player_id=$2`, time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC), early.ID)
	e.now = time.Date(2026, 10, 28, 0, 30, 0, 0, time.UTC)
	if n, _ := e.d.Notify.StreakLost(ctx); n != 1 {
		t.Fatalf("streak lost: %d", n)
	}
	if n, _ := e.d.Notify.StreakLost(ctx); n != 0 {
		t.Fatal("once per day")
	}
}

// ---------------------------------------------------------------- bot commands

func (e *env) say(from telegram.User, text string) {
	e.t.Helper()
	r := e.tgUpdate(telegram.Update{Message: &telegram.Message{From: &from, Chat: telegram.Chat{ID: from.ID, Type: "private"}, Text: text}})
	if r.Status != 200 {
		e.t.Fatalf("update: %d %s", r.Status, r.Body)
	}
}

func TestBotStartHelpLanguageAndMenus(t *testing.T) {
	e := newEnv(t)
	fake := e.botEnv()
	ru := telegram.User{ID: 620, FirstName: "Ivan", LanguageCode: "ru"}
	en := telegram.User{ID: 621, FirstName: "Joe", LanguageCode: "en"}

	e.say(ru, "/start")
	e.say(en, "/start@ImTomsBot")
	if m := fake.MessagesTo(620); len(m) != 1 || !strings.Contains(m[0].Text, "Добро пожаловать") || m[0].Buttons[0][0].Text != "Открыть игру" {
		t.Fatalf("Russian welcome: %v", m)
	}
	if m := fake.MessagesTo(621); len(m) != 1 || !strings.Contains(m[0].Text, "Welcome to BrunoPlay") || m[0].Buttons[0][0].Text != "Open game" || m[0].Buttons[0][0].WebApp != "https://app.example/" {
		t.Fatalf("English welcome: %v", m)
	}
	// the person who pressed /start is reachable as soon as the app creates the account
	p := e.login(620, "ru", "")
	var started bool
	_ = e.d.Pool.QueryRow(context.Background(), `SELECT bot_started FROM players WHERE id=$1`, p.ID).Scan(&started)
	if !started {
		t.Fatal("a bot user who opens the app later is marked as reachable")
	}
	// help and free text
	fake.Reset()
	e.say(en, "hello?")
	if m := fake.MessagesTo(621); len(m) != 1 || !strings.Contains(m[0].Text, "BrunoPlay help") {
		t.Fatalf("free text gets the help: %v", m)
	}
	// language buttons
	e.tgUpdate(telegram.Update{CallbackQuery: &telegram.CallbackQuery{ID: "1", From: ru, Data: "lang:EN", Message: &telegram.Message{MessageID: 1, Chat: telegram.Chat{ID: 620}}}})
	var lang string
	_ = e.d.Pool.QueryRow(context.Background(), `SELECT language FROM players WHERE id=$1`, p.ID).Scan(&lang)
	if lang != "EN" {
		t.Fatalf("language %s", lang)
	}
	if got := e.do("GET", "/api/me", p.Token, nil).JSON(t)["user"].(map[string]any)["language"]; got != "EN" {
		t.Fatalf("the app sees the same language: %v", got)
	}
	// notifications menu: toggle a category, mute everything
	fake.Reset()
	e.say(ru, "/notifications")
	m := fake.MessagesTo(620)
	if len(m) != 1 || len(m[0].Buttons) != 7 || !strings.Contains(m[0].Buttons[0][0].Text, "✅") {
		t.Fatalf("menu: %+v", m)
	}
	cb := func(data string) {
		e.tgUpdate(telegram.Update{CallbackQuery: &telegram.CallbackQuery{ID: "x", From: ru, Data: data, Message: &telegram.Message{MessageID: 7, Chat: telegram.Chat{ID: 620}}}})
	}
	cb("cat:fishing")
	var off string
	_ = e.d.Pool.QueryRow(context.Background(), `SELECT notif_off::text FROM players WHERE id=$1`, p.ID).Scan(&off)
	if off != `["fishing"]` || len(fake.Edits) != 1 || !strings.Contains(fake.Edits[0].Buttons[0][0].Text, "🚫") {
		t.Fatalf("toggle: %s edits=%d", off, len(fake.Edits))
	}
	cb("cat:fishing")
	cb("cat:nonsense")
	_ = e.d.Pool.QueryRow(context.Background(), `SELECT notif_off::text FROM players WHERE id=$1`, p.ID).Scan(&off)
	if off != `[]` {
		t.Fatalf("toggle back: %s", off)
	}
	e.say(ru, "/stop")
	var all bool
	_ = e.d.Pool.QueryRow(context.Background(), `SELECT notif_all_off FROM players WHERE id=$1`, p.ID).Scan(&all)
	if !all {
		t.Fatal("/stop mutes")
	}
	e.say(ru, "/resume")
	_ = e.d.Pool.QueryRow(context.Background(), `SELECT notif_all_off FROM players WHERE id=$1`, p.ID).Scan(&all)
	if all {
		t.Fatal("/resume unmutes")
	}
	// the bot owner cannot be tricked: the webhook needs the secret
	if r := e.do("POST", "/api/webhooks/telegram", "", map[string]any{}); r.Status != 401 {
		t.Fatalf("%d", r.Status)
	}
}

func TestBotInviteLinkBeforeTheApp(t *testing.T) {
	e := newEnv(t)
	e.botEnv()
	inviter := e.login(630, "en", "")
	e.exec(`UPDATE players SET invite_bonus_coins=40 WHERE id=$1`, inviter.ID)
	e.say(telegram.User{ID: 631, FirstName: "New", LanguageCode: "en"}, fmt.Sprintf("/start ref_%d", inviter.ID))
	friend := e.login(631, "en", "") // opens the app without a parameter
	var by *int64
	var bonus int64
	_ = e.d.Pool.QueryRow(context.Background(), `SELECT referred_by, friend_bonus_coins FROM players WHERE id=$1`, friend.ID).Scan(&by, &bonus)
	if by == nil || *by != inviter.ID || bonus != 40 {
		t.Fatalf("attribution through the bot link: %v %d", by, bonus)
	}
}

// ---------------------------------------------------------------- templates and broadcasts

func TestAdminTemplatesOverrideDefaults(t *testing.T) {
	e := newEnv(t)
	fake := e.botEnv()
	content := e.adminLogin("content@example.com", admin.RoleContent)
	p := e.login(640, "en", "")
	e.reach(p)
	if r := e.do("PUT", "/api/admin/notifications/templates/task_new/EN", content, map[string]any{"text": "New tasks, {name}!", "button": "Go"}); r.Status != 200 {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	if r := e.do("PUT", "/api/admin/notifications/templates/nope/EN", content, map[string]any{"text": "x"}); r.Status != 404 {
		t.Fatalf("%d", r.Status)
	}
	_ = e.d.Notify.Send(context.Background(), e.d.Pool, p.ID, "task_new", map[string]any{"name": "<Bruno>"}, "tpl1")
	e.sendDue()
	m := fake.MessagesTo(640)
	if len(m) != 1 || m[0].Text != "New tasks, &lt;Bruno&gt;!" || m[0].Buttons[0][0].Text != "Go" {
		t.Fatalf("edited template: %+v", m)
	}
	list := e.do("GET", "/api/admin/notifications/templates", content, nil).JSON(t)["templates"].([]any)
	found := false
	for _, x := range list {
		tp := x.(map[string]any)
		if tp["type"] == "task_new" && tp["lang"] == "EN" && tp["edited"] == true && tp["default"] == "📋 New tasks are waiting for you!" {
			found = true
		}
	}
	if !found {
		t.Fatalf("templates list: %v", list[:2])
	}
	e.do("DELETE", "/api/admin/notifications/templates/task_new/EN", content, nil)
	e.now = e.now.Add(48 * time.Hour)
	_ = e.d.Notify.Send(context.Background(), e.d.Pool, p.ID, "task_new", nil, "tpl2")
	e.sendDue()
	if txt := e.lastText(fake, p); txt != "📋 New tasks are waiting for you!" {
		t.Fatalf("reset to default: %q", txt)
	}
}

func TestBroadcastNeedsTestSegmentsAndLanguage(t *testing.T) {
	e := newEnv(t)
	fake := e.botEnv()
	content := e.adminLogin("content@example.com", admin.RoleContent)
	var adminID int64
	_ = e.d.Pool.QueryRow(context.Background(), `SELECT id FROM admins WHERE email='content@example.com'`).Scan(&adminID)
	en1, ru1, ru2, blockedBot := e.login(650, "en", ""), e.login(651, "ru", ""), e.login(652, "ru", ""), e.login(653, "ru", "")
	for _, p := range []player{en1, ru1, ru2, blockedBot} {
		e.reach(p)
	}
	e.exec(`UPDATE players SET bot_blocked=TRUE WHERE id=$1`, blockedBot.ID)
	e.exec(`UPDATE players SET level=5 WHERE id=$1`, ru2.ID)

	body := map[string]any{"title": "Event", "textEn": "Big event!", "textRu": "Большое событие!", "buttonEn": "Join", "buttonRu": "Участвовать", "buttonPath": "/games",
		"segment": map[string]any{"language": "RU"}}
	r := e.do("POST", "/api/admin/broadcasts", content, body)
	if r.Status != 201 {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	id := int(r.JSON(t)["id"].(float64))
	base := fmt.Sprintf("/api/admin/broadcasts/%d", id)
	if r := e.do("POST", base+"/start", content, nil); r.Status != 400 {
		t.Fatalf("a broadcast must be tested first: %d", r.Status)
	}
	if r := e.do("POST", base+"/test", content, nil); r.Status != 422 {
		t.Fatalf("the admin needs a Telegram id to test: %d", r.Status)
	}
	e.do("PATCH", fmt.Sprintf("/api/admin/admins/%d", adminID), e.adminLogin("root@example.com", admin.RoleSuper), map[string]any{"telegramId": 9001})
	if r := e.do("POST", base+"/test", content, nil); r.Status != 200 {
		t.Fatalf("test: %d %s", r.Status, r.Body)
	}
	if m := fake.MessagesTo(9001); len(m) != 2 || !strings.HasPrefix(m[0].Text, "[EN]") || !strings.HasPrefix(m[1].Text, "[RU]") {
		t.Fatalf("both languages are tested: %v", m)
	}
	if r := e.do("POST", base+"/start", content, map[string]any{}); r.Status != 200 {
		t.Fatalf("start: %d %s", r.Status, r.Body)
	}
	if err := e.d.Notify.RunBroadcasts(context.Background()); err != nil {
		t.Fatal(err)
	}
	e.sendDue()
	if len(fake.MessagesTo(650)) != 0 || len(fake.MessagesTo(653)) != 0 {
		t.Fatal("the RU segment excludes English players and players who blocked the bot")
	}
	m1, m2 := fake.MessagesTo(651), fake.MessagesTo(652)
	if len(m1) != 1 || m1[0].Text != "Большое событие!" || m1[0].Buttons[0][0].Text != "Участвовать" || m1[0].Buttons[0][0].WebApp != "https://app.example/games" || len(m2) != 1 {
		t.Fatalf("%v %v", m1, m2)
	}
	_ = e.d.Notify.RunBroadcasts(context.Background()) // the next maintenance pass closes a finished broadcast
	d := e.do("GET", base, content, nil).JSON(t)
	if d["broadcast"].(map[string]any)["status"] != "DONE" || d["stats"].(map[string]any)["SENT"].(float64) != 2 {
		t.Fatalf("stats: %v", d)
	}
	// a level segment and a future start
	b2 := e.do("POST", "/api/admin/broadcasts", content, map[string]any{"title": "Lvl", "textEn": "a", "textRu": "б", "segment": map[string]any{"minLevel": 5}}).JSON(t)
	id2 := int(b2["id"].(float64))
	e.do("POST", fmt.Sprintf("/api/admin/broadcasts/%d/test", id2), content, nil)
	when := e.now.Add(time.Hour).Format(time.RFC3339)
	e.do("POST", fmt.Sprintf("/api/admin/broadcasts/%d/start", id2), content, map[string]any{"at": when})
	e.d.Notify.RunBroadcasts(context.Background())
	if e.countRows("notifications WHERE broadcast_id IS NOT NULL AND status='PENDING'") != 0 {
		t.Fatal("a scheduled broadcast waits for its time")
	}
	e.now = e.now.Add(2 * time.Hour)
	e.d.Notify.RunBroadcasts(context.Background())
	e.sendDue()
	if len(fake.MessagesTo(652)) != 2 || len(fake.MessagesTo(651)) != 1 {
		t.Fatalf("only the level-5 player gets the second broadcast: %d %d", len(fake.MessagesTo(652)), len(fake.MessagesTo(651)))
	}
	// support has no access to broadcasts
	sup := e.adminLogin("sup@example.com", admin.RoleSupport)
	if r := e.do("GET", "/api/admin/broadcasts", sup, nil); r.Status != 403 {
		t.Fatalf("%d", r.Status)
	}
}

func TestTaskNewIsAnnouncedToEveryone(t *testing.T) {
	e := newEnv(t)
	fake := e.botEnv()
	content := e.adminLogin("content@example.com", admin.RoleContent)
	a, b := e.login(660, "en", ""), e.login(661, "ru", "")
	e.reach(a)
	e.reach(b)
	e.task(content, taskBody("OPEN_LINK", map[string]any{"url": "https://example.com"}))
	e.sendDue()
	if len(fake.MessagesTo(660)) != 1 || fake.MessagesTo(661)[0].Text != "📋 Вас ждут новые задания!" {
		t.Fatalf("%v", fake.Messages)
	}
}
