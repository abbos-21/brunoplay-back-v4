package api_test

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"brunoplay/internal/admin"
	"brunoplay/internal/ledger"
	"brunoplay/internal/settings"
	"brunoplay/internal/telegram"
)

func (e *env) nextDay() {
	e.now = time.Date(e.now.Year(), e.now.Month(), e.now.Day(), 12, 0, 0, 0, time.UTC).Add(24 * time.Hour)
}

// ---------------------------------------------------------------- streak

func TestStreakLadderAndBreak(t *testing.T) {
	e := newEnv(t)
	e.gameConfig()
	p := e.login(400, "en", "")
	if r := e.do("GET", "/api/daily/streak", p.Token, nil); r.Code(t) != "CONFIG_MISSING" {
		t.Fatalf("%s", r.Body)
	}
	e.setSetting(settings.KeyStreakRewards, []int{10, 20, 30, 40, 50, 60, 70, 80, 90, 100})

	st := e.do("GET", "/api/daily/streak", p.Token, nil).JSON(t)
	cells := st["streakRewards"].([]any)
	if st["canClaim"] != true || st["todayReward"].(float64) != 10 || st["currentStreak"].(float64) != 0 || len(cells) != 10 ||
		cells[0].(map[string]any)["isToday"] != true || cells[0].(map[string]any)["claimed"] != false {
		t.Fatalf("day 1: %v", st)
	}
	claim := func() map[string]any {
		r := e.do("POST", "/api/daily/streak/claim", p.Token, map[string]any{})
		if r.Status != 200 {
			t.Fatalf("claim: %d %s", r.Status, r.Body)
		}
		return r.JSON(t)
	}
	c := claim()
	if c["reward"].(float64) != 10 || c["newStreak"].(float64) != 1 || e.coins(p) != 10 {
		t.Fatalf("%v", c)
	}
	if r := e.do("POST", "/api/daily/streak/claim", p.Token, map[string]any{}); r.Code(t) != "STREAK_ALREADY_CLAIMED" {
		t.Fatalf("second claim today: %s", r.Body)
	}
	st = e.do("GET", "/api/daily/streak", p.Token, nil).JSON(t)
	if st["canClaim"] != false || st["currentStreak"].(float64) != 1 || st["streakRewards"].([]any)[0].(map[string]any)["claimed"] != true {
		t.Fatalf("after claim: %v", st)
	}

	// consecutive days climb the ladder
	for want := int64(20); want <= 100; want += 10 {
		e.nextDay()
		if got := int64(claim()["reward"].(float64)); got != want {
			t.Fatalf("ladder reward %d, want %d", got, want)
		}
	}
	// day 11 loops back to the day 1 reward while the streak keeps counting
	e.nextDay()
	st = e.do("GET", "/api/daily/streak", p.Token, nil).JSON(t)
	for _, c := range st["streakRewards"].([]any) {
		if c.(map[string]any)["claimed"] != false {
			t.Fatalf("a new round starts with an empty ladder: %v", st["streakRewards"])
		}
	}
	c = claim()
	if c["reward"].(float64) != 10 || c["newStreak"].(float64) != 11 || c["ladderDay"].(float64) != 1 {
		t.Fatalf("loop: %v", c)
	}
	// missing a whole day breaks the streak
	e.nextDay()
	e.nextDay()
	st = e.do("GET", "/api/daily/streak", p.Token, nil).JSON(t)
	if st["currentStreak"].(float64) != 0 || st["todayReward"].(float64) != 10 {
		t.Fatalf("broken streak: %v", st)
	}
	c = claim()
	if c["newStreak"].(float64) != 1 {
		t.Fatalf("restart at 1: %v", c)
	}
	if best := st["bestStreak"].(float64); best != 11 {
		t.Fatalf("best streak is kept: %v", best)
	}
}

func TestStreakDayBoundaryIsUTC(t *testing.T) {
	e := newEnv(t)
	e.gameConfig()
	e.setSetting(settings.KeyStreakRewards, []int{10, 20, 30, 40, 50, 60, 70, 80, 90, 100})
	p := e.login(401, "en", "")
	e.now = time.Date(2026, 10, 24, 23, 59, 58, 0, time.UTC)
	if r := e.do("POST", "/api/daily/streak/claim", p.Token, map[string]any{}); r.Status != 200 {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	e.now = time.Date(2026, 10, 24, 23, 59, 59, 0, time.UTC)
	if r := e.do("POST", "/api/daily/streak/claim", p.Token, map[string]any{}); r.Code(t) != "STREAK_ALREADY_CLAIMED" {
		t.Fatalf("same game day: %s", r.Body)
	}
	e.now = time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC) // one second later: a new day
	r := e.do("POST", "/api/daily/streak/claim", p.Token, map[string]any{})
	if r.Status != 200 || r.JSON(t)["newStreak"].(float64) != 2 {
		t.Fatalf("the next UTC day continues the streak: %d %s", r.Status, r.Body)
	}
	if st := e.do("GET", "/api/daily/streak", p.Token, nil).JSON(t); !strings.HasPrefix(st["resetsAt"].(string), "2026-10-26T00:00:00") {
		t.Fatalf("resetsAt: %v", st["resetsAt"])
	}
}

func TestStreakDoubleWithAd(t *testing.T) {
	e := newEnv(t)
	e.gameConfig()
	e.setSetting(settings.KeyStreakRewards, []int{10, 20, 30, 40, 50, 60, 70, 80, 90, 100})
	p := e.login(402, "en", "")
	if r := e.do("POST", "/api/daily/streak/claim", p.Token, map[string]any{"double": true}); r.Code(t) != "AD_NOT_COMPLETED" {
		t.Fatalf("x2 needs an ad: %s", r.Body)
	}
	if e.coins(p) != 0 {
		t.Fatal("a refused x2 must not pay")
	}
	ad := e.watchAd(p, "DOUBLE_STREAK")
	r := e.do("POST", "/api/daily/streak/claim", p.Token, map[string]any{"double": true, "adViewId": ad})
	if r.Status != 200 || r.JSON(t)["reward"].(float64) != 20 || e.coins(p) != 20 {
		t.Fatalf("x2: %d %s coins=%d", r.Status, r.Body, e.coins(p))
	}
	// an ad cannot be requested for something already claimed
	if r := e.do("POST", "/api/ads", p.Token, map[string]any{"placement": "DOUBLE_STREAK"}); r.Code(t) != "STREAK_ALREADY_CLAIMED" {
		t.Fatalf("%s", r.Body)
	}
}

func TestConcurrentStreakClaimPaysOnce(t *testing.T) {
	e := newEnv(t)
	e.gameConfig()
	e.setSetting(settings.KeyStreakRewards, []int{10, 20, 30, 40, 50, 60, 70, 80, 90, 100})
	p := e.login(403, "en", "")
	var ok int64
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if e.do("POST", "/api/daily/streak/claim", p.Token, map[string]any{}).Status == 200 {
				atomic.AddInt64(&ok, 1)
			}
		}()
	}
	wg.Wait()
	if ok != 1 || e.coins(p) != 10 {
		t.Fatalf("ok=%d coins=%d", ok, e.coins(p))
	}
}

// ---------------------------------------------------------------- combo

func (e *env) setPattern(items ...int) {
	e.t.Helper()
	e.exec(`INSERT INTO combo_patterns (day, items, source) VALUES ($1,$2,'admin') ON CONFLICT (day) DO UPDATE SET items=EXCLUDED.items`,
		time.Date(e.now.Year(), e.now.Month(), e.now.Day(), 0, 0, 0, 0, time.UTC), items)
}

func (e *env) combo(p player, items []int, ad string) resp {
	body := map[string]any{"items": items}
	if ad != "" {
		body["adViewId"] = ad
	}
	return e.do("POST", "/api/daily/combo/submit", p.Token, body)
}

func TestComboScoringExampleAndNoLeak(t *testing.T) {
	e := newEnv(t)
	e.gameConfig()
	e.setSetting(settings.KeyComboPerCorrect, 5)
	p := e.login(410, "ru", "")
	e.setPattern(1, 2, 3, 4) // hat, fish, net, boots

	st := e.do("GET", "/api/daily/combo", p.Token, nil)
	m := st.JSON(t)
	if len(m["items"].([]any)) != 8 || m["slots"].(float64) != 4 || m["alreadyAttempted"] != false {
		t.Fatalf("status: %s", st.Body)
	}
	if m["items"].([]any)[0].(map[string]any)["name"] != "Шляпа" {
		t.Fatalf("names follow the player's language: %v", m["items"].([]any)[0])
	}
	for _, bad := range [][]int{{1, 2, 3}, {1, 1, 2, 3}, {1, 2, 3, 99}, {1, 2, 3, 4, 5}, {}} {
		if r := e.combo(p, bad, ""); r.Status != 422 && r.Status != 400 {
			t.Errorf("%v must be refused: %d %s", bad, r.Status, r.Body)
		}
	}
	// the example of the owner: pineapple, banana, apple, orange -> only banana and orange are right
	r := e.combo(p, []int{3, 2, 1, 4}, "")
	if r.Status != 200 {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	res := r.JSON(t)
	got := res["correct"].([]any)
	if got[0] != false || got[1] != true || got[2] != false || got[3] != true || res["correctCount"].(float64) != 2 || res["reward"].(float64) != 10 {
		t.Fatalf("scoring: %v", res)
	}
	if e.coins(p) != 0 {
		t.Fatal("nothing is paid before the claim")
	}
	// the answer is never in any response
	for _, body := range []string{string(st.Body), string(r.Body), string(e.do("GET", "/api/daily/combo", p.Token, nil).Body)} {
		for _, key := range []string{"pattern", "answer", "correctAnswer", "solution"} {
			if strings.Contains(strings.ToLower(body), key) {
				t.Fatalf("response leaks %q: %s", key, body)
			}
		}
	}
	after := e.do("GET", "/api/daily/combo", p.Token, nil).JSON(t)
	if after["alreadyAttempted"] != true || after["lastResult"].(float64) != 2 || after["canClaim"] != true {
		t.Fatalf("%v", after)
	}
	// only the free attempt: the second needs an ad and an admin limit
	if r := e.combo(p, []int{1, 2, 3, 4}, ""); r.Code(t) != "COMBO_ALREADY_ATTEMPTED" {
		t.Fatalf("%s", r.Body)
	}
}

func TestComboExtraAttemptsBestCountsClaimAndDouble(t *testing.T) {
	e := newEnv(t)
	e.gameConfig()
	e.setSetting(settings.KeyComboPerCorrect, 5)
	e.setSetting(settings.KeyAdsComboExtra, 2)
	p := e.login(411, "en", "")
	e.setPattern(1, 2, 3, 4)

	// no ad can be requested before the free attempt is used
	if r := e.do("POST", "/api/ads", p.Token, map[string]any{"placement": "COMBO_EXTRA"}); r.Code(t) != "EXTRA_LIMIT_REACHED" {
		t.Fatalf("%s", r.Body)
	}
	if r := e.combo(p, []int{1, 5, 6, 7}, ""); r.Status != 200 { // 1 correct
		t.Fatalf("free attempt: %d %s", r.Status, r.Body)
	}
	// an extra attempt without a watched ad is refused
	if r := e.combo(p, []int{1, 2, 3, 4}, ""); r.Code(t) != "AD_NOT_COMPLETED" {
		t.Fatalf("%s", r.Body)
	}
	r := e.combo(p, []int{1, 2, 3, 4}, e.watchAd(p, "COMBO_EXTRA"))
	if r.Status != 200 || r.JSON(t)["bestCorrect"].(float64) != 4 || r.JSON(t)["extraAttemptsLeft"].(float64) != 1 {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	// a worse attempt does not lower the best
	r = e.combo(p, []int{5, 6, 7, 8}, e.watchAd(p, "COMBO_EXTRA"))
	res := r.JSON(t)
	if res["correctCount"].(float64) != 0 || res["bestCorrect"].(float64) != 4 || res["reward"].(float64) != 20 || res["extraAttemptsLeft"].(float64) != 0 {
		t.Fatalf("%v", res)
	}
	// the daily limit of extra attempts is reached
	if r := e.do("POST", "/api/ads", p.Token, map[string]any{"placement": "COMBO_EXTRA"}); r.Code(t) != "EXTRA_LIMIT_REACHED" {
		t.Fatalf("%s", r.Body)
	}
	// claim x2 needs the ad, then pays the best attempt doubled, once
	if r := e.do("POST", "/api/daily/combo/claim", p.Token, map[string]any{"double": true}); r.Code(t) != "AD_NOT_COMPLETED" {
		t.Fatalf("%s", r.Body)
	}
	ad := e.watchAd(p, "DOUBLE_COMBO")
	c := e.do("POST", "/api/daily/combo/claim", p.Token, map[string]any{"double": true, "adViewId": ad})
	if c.Status != 200 || c.JSON(t)["reward"].(float64) != 40 || e.coins(p) != 40 {
		t.Fatalf("claim x2: %d %s coins=%d", c.Status, c.Body, e.coins(p))
	}
	if r := e.do("POST", "/api/daily/combo/claim", p.Token, map[string]any{}); r.Code(t) != "COMBO_ALREADY_CLAIMED" {
		t.Fatalf("%s", r.Body)
	}
	if r := e.combo(p, []int{1, 2, 3, 4}, "any"); r.Code(t) != "COMBO_ALREADY_CLAIMED" {
		t.Fatalf("claiming locks the day: %s", r.Body)
	}
	if e.coins(p) != 40 {
		t.Fatalf("coins %d", e.coins(p))
	}
}

func TestComboClaimRulesAndLostReward(t *testing.T) {
	e := newEnv(t)
	e.gameConfig()
	e.setSetting(settings.KeyComboPerCorrect, 5)
	p := e.login(412, "en", "")
	e.setPattern(1, 2, 3, 4)
	if r := e.do("POST", "/api/daily/combo/claim", p.Token, map[string]any{}); r.Code(t) != "COMBO_NOTHING_TO_CLAIM" {
		t.Fatalf("no attempt yet: %s", r.Body)
	}
	e.combo(p, []int{5, 6, 7, 8}, "") // nothing right
	if r := e.do("POST", "/api/daily/combo/claim", p.Token, map[string]any{}); r.Code(t) != "COMBO_NOTHING_TO_CLAIM" {
		t.Fatalf("nothing won: %s", r.Body)
	}
	// a plain claim pays the best attempt; an unclaimed reward is lost at the reset
	e2 := e.login(413, "en", "")
	e.combo(e2, []int{1, 2, 7, 8}, "")
	e.nextDay()
	e.d.PlayerTokenTTL = 100000 * time.Hour
	if r := e.do("POST", "/api/daily/combo/claim", e2.Token, map[string]any{}); r.Code(t) != "COMBO_NOTHING_TO_CLAIM" {
		t.Fatalf("yesterday's reward is gone: %s", r.Body)
	}
	if e.coins(e2) != 0 {
		t.Fatal("an unclaimed combo reward is lost")
	}
	// the new day has a fresh free attempt
	if r := e.combo(e2, []int{1, 2, 3, 4}, ""); r.Status != 200 {
		t.Fatalf("new day: %d %s", r.Status, r.Body)
	}
}

func TestComboAutoPatternSharedAndAdminSchedule(t *testing.T) {
	e := newEnv(t)
	e.gameConfig()
	e.setSetting(settings.KeyComboPerCorrect, 5)
	a, b := e.login(414, "en", ""), e.login(415, "en", "")

	// no pattern scheduled: the first attempt creates a random one, the same for everybody
	e.combo(a, []int{1, 2, 3, 4}, "")
	var src string
	var items []int32
	if err := e.d.Pool.QueryRow(context.Background(), `SELECT source, items FROM combo_patterns`).Scan(&src, &items); err != nil || src != "auto" || len(items) != 4 {
		t.Fatalf("auto pattern: %v %s %v", err, src, items)
	}
	seen := map[int32]bool{}
	for _, x := range items {
		seen[x] = true
	}
	if len(seen) != 4 {
		t.Fatalf("a pattern has four different pictures: %v", items)
	}
	rb := e.combo(b, []int{int(items[0]), int(items[1]), int(items[2]), int(items[3])}, "")
	if rb.JSON(t)["correctCount"].(float64) != 4 {
		t.Fatalf("both players face the same pattern: %s", rb.Body)
	}

	// admin schedules a future day; today is read-only; duplicates are refused
	content := e.adminLogin("content@example.com", admin.RoleContent)
	tomorrow := e.now.AddDate(0, 0, 1).Format("2006-01-02")
	today := e.now.Format("2006-01-02")
	if r := e.do("PUT", "/api/admin/combo/patterns/"+tomorrow, content, map[string]any{"items": []int{4, 3, 2, 1}}); r.Status != 200 {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	if r := e.do("PUT", "/api/admin/combo/patterns/"+today, content, map[string]any{"items": []int{4, 3, 2, 1}}); r.Status != 400 {
		t.Fatalf("today is read-only: %d", r.Status)
	}
	if r := e.do("PUT", "/api/admin/combo/patterns/"+tomorrow, content, map[string]any{"items": []int{4, 4, 2, 1}}); r.Status != 422 {
		t.Fatalf("duplicates: %d", r.Status)
	}
	if r := e.do("PUT", "/api/admin/combo/patterns/"+tomorrow, content, map[string]any{"items": []int{4, 3, 2, 99}}); r.Status != 422 {
		t.Fatalf("unknown picture: %d", r.Status)
	}
	sup := e.adminLogin("sup@example.com", admin.RoleSupport)
	if r := e.do("PUT", "/api/admin/combo/patterns/"+tomorrow, sup, map[string]any{"items": []int{1, 2, 3, 4}}); r.Status != 403 {
		t.Fatalf("support cannot edit the combo: %d", r.Status)
	}
	l := e.do("GET", "/api/admin/combo/patterns", content, nil).JSON(t)["patterns"].([]any)
	if len(l) != 2 || l[0].(map[string]any)["source"] != "auto" || l[0].(map[string]any)["attempters"].(float64) != 2 || l[1].(map[string]any)["source"] != "admin" {
		t.Fatalf("calendar: %v", l)
	}
	// the scheduled pattern is the one used the next day
	e.nextDay()
	e.d.PlayerTokenTTL = 100000 * time.Hour
	content = e.adminRelogin("content@example.com")
	if r := e.combo(a, []int{4, 3, 2, 1}, ""); r.JSON(t)["correctCount"].(float64) != 4 {
		t.Fatalf("scheduled pattern: %s", r.Body)
	}
	// cannot deactivate so many pictures that fewer than four remain
	if r := e.do("PATCH", "/api/admin/combo/images/8", content, map[string]any{"active": false}); r.Status != 200 {
		t.Fatalf("%d", r.Status)
	}
	for id := 7; id >= 5; id-- {
		e.do("PATCH", fmt.Sprintf("/api/admin/combo/images/%d", id), content, map[string]any{"active": false})
	}
	if r := e.do("PATCH", "/api/admin/combo/images/4", content, map[string]any{"active": false}); r.Status != 422 {
		t.Fatalf("the pool must keep four pictures: %d", r.Status)
	}
	e.exec(`UPDATE combo_images SET active=TRUE`)
}

// ---------------------------------------------------------------- tasks

func (e *env) task(content string, body map[string]any) int {
	e.t.Helper()
	r := e.do("POST", "/api/admin/tasks", content, body)
	if r.Status != 201 {
		e.t.Fatalf("create task: %d %s", r.Status, r.Body)
	}
	return int(r.JSON(e.t)["id"].(float64))
}

func taskBody(typ string, extra map[string]any) map[string]any {
	b := map[string]any{"category": "MAIN", "type": typ, "titleEn": "Do it", "titleRu": "Сделай", "descEn": "d", "descRu": "о", "required": 1, "rewardCoins": 50}
	for k, v := range extra {
		b[k] = v
	}
	return b
}

func TestTasksChannelInviteLinkAds(t *testing.T) {
	e := newEnv(t)
	e.gameConfig()
	fake, _ := e.moneyConfig()
	e.d.Tasks.SetTelegram(fake)
	content := e.adminLogin("content@example.com", admin.RoleContent)
	p := e.login(420, "ru", "")

	if r := e.do("POST", "/api/admin/tasks", content, taskBody("CHANNEL_JOIN", nil)); r.Status != 422 {
		t.Fatalf("a channel task needs a channel: %d", r.Status)
	}
	chID := e.task(content, taskBody("CHANNEL_JOIN", map[string]any{"channelUsername": "@bruno"}))
	invID := e.task(content, taskBody("INVITE_FRIENDS", map[string]any{"required": 2, "rewardCoins": 100, "category": "OTHER"}))
	linkID := e.task(content, taskBody("OPEN_LINK", map[string]any{"url": "https://example.com", "rewardCoins": 7, "rewardStars": 2}))
	adsID := e.task(content, taskBody("WATCH_ADS", map[string]any{"required": 2, "rewardCoins": 30}))

	l := e.do("GET", "/api/tasks", p.Token, nil).JSON(t)
	if len(l["tasks"].([]any)) != 4 || l["newCount"].(float64) != 4 {
		t.Fatalf("list: %v", l)
	}
	if l["tasks"].([]any)[0].(map[string]any)["title"] != "Сделай" {
		t.Fatalf("titles follow the language: %v", l["tasks"].([]any)[0])
	}
	e.do("POST", "/api/tasks/seen", p.Token, nil)
	if l := e.do("GET", "/api/tasks", p.Token, nil).JSON(t); l["newCount"].(float64) != 0 {
		t.Fatalf("seen clears the badge: %v", l)
	}
	e.now = e.now.Add(time.Minute)
	later := e.task(content, taskBody("OPEN_LINK", map[string]any{"url": "https://example.com/2"}))
	if l := e.do("GET", "/api/tasks", p.Token, nil).JSON(t); l["newCount"].(float64) != 1 {
		t.Fatalf("a task added after the last visit is new: %v", l)
	}
	_ = later

	check := func(id int) map[string]any {
		r := e.do("POST", fmt.Sprintf("/api/tasks/%d/check", id), p.Token, nil)
		if r.Status != 200 {
			t.Fatalf("check %d: %d %s", id, r.Status, r.Body)
		}
		return r.JSON(t)
	}
	// channel: not a member, then member
	if c := check(chID); c["completed"] != false || c["error"] != "TASK_NOT_COMPLETED" {
		t.Fatalf("%v", c)
	}
	fake.Members["@bruno:420"] = "member"
	if c := check(chID); c["completed"] != true || c["reward"].(float64) != 50 || e.coins(p) != 50 {
		t.Fatalf("%v coins=%d", c, e.coins(p))
	}
	if r := e.do("POST", fmt.Sprintf("/api/tasks/%d/check", chID), p.Token, nil); r.Code(t) != "TASK_ALREADY_COMPLETED" {
		t.Fatalf("%s", r.Body)
	}
	// invite: needs two counted friends
	f1, f2 := e.login(421, "en", fmt.Sprintf("ref_%d", p.ID)), e.login(422, "en", fmt.Sprintf("ref_%d", p.ID))
	e.exec(`UPDATE players SET referral_counted=TRUE, referral_counted_at=now() WHERE id=$1`, f1.ID)
	if c := check(invID); c["completed"] != false || c["progress"].(float64) != 1 {
		t.Fatalf("1 of 2: %v", c)
	}
	e.exec(`UPDATE players SET referral_counted=TRUE, referral_counted_at=now() WHERE id=$1`, f2.ID)
	if c := check(invID); c["completed"] != true || e.coins(p) != 150 {
		t.Fatalf("%v coins=%d", c, e.coins(p))
	}
	// link: must be opened and wait the minimum time
	if c := check(linkID); c["completed"] != false {
		t.Fatalf("never opened: %v", c)
	}
	e.do("POST", fmt.Sprintf("/api/tasks/%d/open", linkID), p.Token, nil)
	if c := check(linkID); c["completed"] != false {
		t.Fatalf("too fast: %v", c)
	}
	e.now = e.now.Add(6 * time.Second)
	if c := check(linkID); c["completed"] != true || e.coins(p) != 157 || e.stars(p) != 2 {
		t.Fatalf("%v coins=%d stars=%d", c, e.coins(p), e.stars(p))
	}
	// ads: two watched ads
	adp := func() resp {
		return e.do("POST", fmt.Sprintf("/api/tasks/%d/ad-progress", adsID), p.Token, map[string]any{"adViewId": e.watchAd(p, "TASK_ADS")})
	}
	if r := adp(); r.JSON(t)["progress"].(float64) != 1 || r.JSON(t)["completed"] != false {
		t.Fatalf("%s", r.Body)
	}
	used := e.watchAd(p, "TASK_ADS")
	if r := e.do("POST", fmt.Sprintf("/api/tasks/%d/ad-progress", adsID), p.Token, map[string]any{"adViewId": used}); r.JSON(t)["completed"] != true || e.coins(p) != 187 {
		t.Fatalf("%s coins=%d", r.Body, e.coins(p))
	}
	if r := e.do("POST", fmt.Sprintf("/api/tasks/%d/ad-progress", adsID), p.Token, map[string]any{"adViewId": used}); r.Status != 409 {
		t.Fatalf("a finished task takes no more ads: %d", r.Status)
	}
	if r := e.do("POST", fmt.Sprintf("/api/tasks/%d/ad-progress", chID), p.Token, map[string]any{"adViewId": "x"}); r.Status != 400 {
		t.Fatalf("ads count only for ad tasks: %d", r.Status)
	}
}

func TestTaskRepeatWindowsAndConcurrency(t *testing.T) {
	e := newEnv(t)
	e.gameConfig()
	fake, _ := e.moneyConfig()
	e.d.Tasks.SetTelegram(fake)
	content := e.adminLogin("content@example.com", admin.RoleContent)
	p := e.login(430, "en", "")
	fake.Members["@daily:430"] = "member"
	daily := e.task(content, taskBody("CHANNEL_JOIN", map[string]any{"channelUsername": "@daily", "repeat": "DAILY"}))
	weekly := e.task(content, taskBody("CHANNEL_JOIN", map[string]any{"channelUsername": "@daily", "repeat": "WEEKLY", "rewardCoins": 70}))
	once := e.task(content, taskBody("CHANNEL_JOIN", map[string]any{"channelUsername": "@daily", "rewardCoins": 5}))

	var paid int64
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if e.do("POST", fmt.Sprintf("/api/tasks/%d/check", daily), p.Token, nil).Status == 200 {
				atomic.AddInt64(&paid, 1)
			}
		}()
	}
	wg.Wait()
	if paid != 1 || e.coins(p) != 50 {
		t.Fatalf("concurrent checks: paid=%d coins=%d", paid, e.coins(p))
	}
	e.do("POST", fmt.Sprintf("/api/tasks/%d/check", weekly), p.Token, nil)
	e.do("POST", fmt.Sprintf("/api/tasks/%d/check", once), p.Token, nil)
	if e.coins(p) != 125 {
		t.Fatalf("coins %d", e.coins(p))
	}
	// next day: the daily task is available again, the weekly (same ISO week) and the one-time are not
	e.nextDay()
	e.d.PlayerTokenTTL = 100000 * time.Hour
	l := e.do("GET", "/api/tasks", p.Token, nil).JSON(t)["tasks"].([]any)
	done := map[int]bool{}
	for _, x := range l {
		m := x.(map[string]any)
		done[int(m["id"].(float64))] = m["isCompleted"].(bool)
	}
	weekDay := e.now.Weekday()
	if done[daily] || !done[once] || (weekDay != time.Monday && !done[weekly]) {
		t.Fatalf("after a day: %v (weekday %v)", done, weekDay)
	}
	if r := e.do("POST", fmt.Sprintf("/api/tasks/%d/check", daily), p.Token, nil); r.Status != 200 || e.coins(p) != 175 {
		t.Fatalf("daily again: %d %s coins=%d", r.Status, r.Body, e.coins(p))
	}
	// a week later the weekly one comes back too
	e.now = e.now.AddDate(0, 0, 8)
	if r := e.do("POST", fmt.Sprintf("/api/tasks/%d/check", weekly), p.Token, nil); r.Status != 200 {
		t.Fatalf("weekly after a week: %d %s", r.Status, r.Body)
	}
	// time window and switch-off
	e.exec(`UPDATE tasks SET ends_at = $2 WHERE id=$1`, once, e.now.Add(-time.Hour))
	e.exec(`UPDATE tasks SET active=FALSE WHERE id=$1`, daily)
	for _, id := range []int{once, daily} {
		if r := e.do("POST", fmt.Sprintf("/api/tasks/%d/check", id), p.Token, nil); r.Status != 404 {
			t.Fatalf("task %d is closed: %d", id, r.Status)
		}
	}
}

func TestChannelCheckErrorIsNotACompletion(t *testing.T) {
	e := newEnv(t)
	e.gameConfig()
	fake := telegram.NewFake()
	e.d.Tasks.SetTelegram(&failingMembers{FakeAPI: fake})
	content := e.adminLogin("content@example.com", admin.RoleContent)
	p := e.login(431, "en", "")
	id := e.task(content, taskBody("CHANNEL_JOIN", map[string]any{"channelUsername": "@bruno"}))
	r := e.do("POST", fmt.Sprintf("/api/tasks/%d/check", id), p.Token, nil)
	if r.Status != 503 || r.Code(t) != "TASK_CANNOT_VERIFY" || e.coins(p) != 0 {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
}

type failingMembers struct{ *telegram.FakeAPI }

func (f *failingMembers) GetChatMemberStatus(ctx context.Context, chat string, userID int64) (string, error) {
	return "", fmt.Errorf("chat not found")
}

func TestCardRequirementTaskDone(t *testing.T) {
	e := newEnv(t)
	e.gameConfig()
	fake, _ := e.moneyConfig()
	e.d.Tasks.SetTelegram(fake)
	content := e.adminLogin("content@example.com", admin.RoleContent)
	p := e.login(432, "en", "")
	fake.Members["@bruno:432"] = "member"
	id := e.task(content, taskBody("CHANNEL_JOIN", map[string]any{"channelUsername": "@bruno", "rewardCoins": 1}))
	e.setSetting(settings.KeyCardsMining, []map[string]any{
		{"level": 1, "value": 0.025, "cost": 0, "currency": "COIN"},
		{"level": 2, "value": 0.03, "cost": 10, "currency": "COIN", "requirements": []map[string]any{{"type": "task_done", "value": id}}}})
	e.give(p, ledger.Coin, 100)
	if r := e.do("POST", "/api/shop/cards/MINING_RATE/upgrade", p.Token, nil); r.Code(t) != "REQUIREMENT_NOT_MET" {
		t.Fatalf("%s", r.Body)
	}
	e.do("POST", fmt.Sprintf("/api/tasks/%d/check", id), p.Token, nil)
	if r := e.do("POST", "/api/shop/cards/MINING_RATE/upgrade", p.Token, nil); r.Status != 200 {
		t.Fatalf("task done unlocks the card: %d %s", r.Status, r.Body)
	}
}

// ---------------------------------------------------------------- bonus ad

func TestBonusAd(t *testing.T) {
	e := newEnv(t)
	e.gameConfig()
	p := e.login(440, "en", "")
	if r := e.do("GET", "/api/ads/bonus", p.Token, nil); r.Code(t) != "CONFIG_MISSING" {
		t.Fatalf("%s", r.Body)
	}
	e.setSetting(settings.KeyAdsBonusCoins, 50)
	e.setSetting(settings.KeyAdsBonusPerDay, 2)
	if st := e.do("GET", "/api/ads/bonus", p.Token, nil).JSON(t); st["left"].(float64) != 2 || st["coins"].(float64) != 50 {
		t.Fatalf("%v", st)
	}
	if r := e.do("POST", "/api/ads/bonus/claim", p.Token, map[string]any{"adViewId": "nope"}); r.Code(t) != "AD_NOT_COMPLETED" {
		t.Fatalf("no free coins without an ad: %s", r.Body)
	}
	ad := e.watchAd(p, "BONUS_COINS")
	if r := e.do("POST", "/api/ads/bonus/claim", p.Token, map[string]any{"adViewId": ad}); r.Status != 200 || e.coins(p) != 50 {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	if r := e.do("POST", "/api/ads/bonus/claim", p.Token, map[string]any{"adViewId": ad}); r.Status != 409 || e.coins(p) != 50 {
		t.Fatalf("an ad pays once: %d", r.Status)
	}
	if r := e.do("POST", "/api/ads/bonus/claim", p.Token, map[string]any{"adViewId": e.watchAd(p, "BONUS_COINS")}); r.Status != 200 || e.coins(p) != 100 {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	if r := e.do("POST", "/api/ads", p.Token, map[string]any{"placement": "BONUS_COINS"}); r.Code(t) != "AD_LIMIT_REACHED" {
		t.Fatalf("the daily limit: %s", r.Body)
	}
	e.nextDay()
	e.d.PlayerTokenTTL = 100000 * time.Hour
	if st := e.do("GET", "/api/ads/bonus", p.Token, nil).JSON(t); st["left"].(float64) != 2 {
		t.Fatalf("a new day: %v", st)
	}
}

// ---------------------------------------------------------------- friends

// levelUp lifts a player to the given level through the real upgrade endpoints.
func (e *env) levelUp(p player, level int) {
	e.t.Helper()
	e.give(p, ledger.Coin, 100000)
	for l := 2; l <= level; l++ {
		for _, card := range []string{"MINING_RATE", "COIN_CAPACITY"} {
			if r := e.do("POST", "/api/shop/cards/"+card+"/upgrade", p.Token, nil); r.Status != 200 {
				e.t.Fatalf("upgrade %s to %d: %d %s", card, l, r.Status, r.Body)
			}
		}
	}
}

func TestReferralCountsAtLevelTwoAndPays(t *testing.T) {
	e := newEnv(t)
	e.gameConfig()
	e.setSetting(settings.KeyRefLevelRewards, map[string]int{"2": 200, "3": 400})
	e.setSetting(settings.KeyRefJoinBonus, 0)
	inviter := e.login(450, "en", "")
	e.exec(`UPDATE players SET invite_bonus_coins=100 WHERE id=$1`, inviter.ID) // a special partner link
	friend := e.login(451, "en", fmt.Sprintf("ref_%d", inviter.ID))

	st := e.do("GET", "/api/friends/stats", inviter.Token, nil).JSON(t)
	if st["totalReferrals"].(float64) != 0 || st["pendingReferrals"].(float64) != 1 {
		t.Fatalf("not counted before level 2: %v", st)
	}
	// level 2: both cards upgraded once
	e.give(friend, ledger.Coin, 100000)
	e.do("POST", "/api/shop/cards/MINING_RATE/upgrade", friend.Token, nil)
	if st := e.do("GET", "/api/friends/stats", inviter.Token, nil).JSON(t); st["totalReferrals"].(float64) != 0 {
		t.Fatalf("one card is not level 2: %v", st)
	}
	before := e.coins(friend)
	e.do("POST", "/api/shop/cards/COIN_CAPACITY/upgrade", friend.Token, nil)
	if e.coins(inviter) != 200 {
		t.Fatalf("inviter reward for level 2: %d", e.coins(inviter))
	}
	if e.coins(friend) != before-100+100 { // upgrade cost 100, join bonus +100
		t.Fatalf("friend join bonus: coins %d (before %d)", e.coins(friend), before)
	}
	st = e.do("GET", "/api/friends/stats", inviter.Token, nil).JSON(t)
	if st["totalReferrals"].(float64) != 1 || st["referralEarnings"].(float64) != 200 || st["pendingReferrals"].(float64) != 0 {
		t.Fatalf("%v", st)
	}
	// the next level pays its own reward, once
	e.do("POST", "/api/shop/cards/MINING_RATE/upgrade", friend.Token, nil)
	e.do("POST", "/api/shop/cards/COIN_CAPACITY/upgrade", friend.Token, nil)
	if e.coins(inviter) != 600 {
		t.Fatalf("level 3 reward: %d", e.coins(inviter))
	}
	// the join bonus is paid once only
	var bonusRows int
	_ = e.d.Pool.QueryRow(context.Background(), `SELECT count(*) FROM ledger_entries WHERE player_id=$1 AND reason='friend_join_bonus'`, friend.ID).Scan(&bonusRows)
	if bonusRows != 1 {
		t.Fatalf("bonus rows %d", bonusRows)
	}
	// the friends list and reward table
	f := e.do("GET", "/api/friends", inviter.Token, nil).JSON(t)
	list := f["friends"].([]any)
	if len(list) != 1 || list[0].(map[string]any)["level"].(float64) != 3 || list[0].(map[string]any)["counted"] != true || len(f["rewards"].([]any)) != 2 {
		t.Fatalf("%v", f)
	}
	links := f["links"].(map[string]any)
	if links["inviteLink"] != fmt.Sprintf("https://t.me/ImTomsBot/app?startapp=ref_%d", inviter.ID) || links["botLink"] != fmt.Sprintf("https://t.me/ImTomsBot?start=ref_%d", inviter.ID) {
		t.Fatalf("links: %v", links)
	}
	// a blocked inviter earns nothing more
	e.exec(`UPDATE players SET is_blocked=TRUE WHERE id=$1`, inviter.ID)
	e.do("POST", "/api/shop/cards/MINING_RATE/upgrade", friend.Token, nil)
	e.do("POST", "/api/shop/cards/COIN_CAPACITY/upgrade", friend.Token, nil)
	if e.coins(inviter) != 600 {
		t.Fatalf("blocked inviter: %d", e.coins(inviter))
	}
}

func TestReferralMiningShareIsCapped(t *testing.T) {
	e := newEnv(t)
	e.gameConfig()
	e.setSetting(settings.KeyRefMiningPct, 10)
	e.setSetting(settings.KeyRefMiningCap, 12)
	inviter := e.login(452, "en", "")
	friend := e.login(453, "en", fmt.Sprintf("ref_%d", inviter.ID))
	collect := func() int64 {
		e.do("POST", "/api/fishing/start", friend.Token, map[string]any{"adViewId": e.watchAd(friend, "FISHING_START")})
		e.now = e.now.Add(10 * time.Hour)
		r := e.do("POST", "/api/fishing/collect", friend.Token, map[string]any{"adViewId": e.watchAd(friend, "COLLECT")})
		if r.Status != 200 {
			t.Fatalf("%d %s", r.Status, r.Body)
		}
		return int64(r.JSON(t)["coinsCollected"].(float64))
	}
	// not counted yet (level 1): no share
	collect()
	if e.coins(inviter) != 0 {
		t.Fatalf("an uncounted friend pays nothing: %d", e.coins(inviter))
	}
	e.exec(`UPDATE players SET referral_counted=TRUE, referral_counted_at=now() WHERE id=$1`, friend.ID)
	e.exec(`UPDATE players SET capacity_level=10, mining_level=10 WHERE id=$1`, friend.ID) // 50-coin tank, full in 10 h at 0.25/s
	collect()                                                                              // 50 coins -> share 5
	if e.coins(inviter) != 5 {
		t.Fatalf("10%% of 50: %d", e.coins(inviter))
	}
	collect() // another 5 -> 10
	collect() // 5 more would be 15 but the cap is 12 -> only 2
	if e.coins(inviter) != 12 {
		t.Fatalf("the cap per friend is 12: %d", e.coins(inviter))
	}
	collect()
	if e.coins(inviter) != 12 {
		t.Fatalf("nothing after the cap: %d", e.coins(inviter))
	}
	// the friend's own coins are not reduced
	if e.coins(friend) <= 0 {
		t.Fatal("the friend keeps everything")
	}
	// a held inviter earns nothing
	e.exec(`UPDATE referral_payments SET coins=0`)
	e.exec(`UPDATE players SET hold_rewards=TRUE WHERE id=$1`, inviter.ID)
	collect()
	if e.coins(inviter) != 12 {
		t.Fatalf("held inviter: %d", e.coins(inviter))
	}
}

func TestReferralThroughBotLinkFirst(t *testing.T) {
	e := newEnv(t)
	inviter := e.login(454, "en", "")
	e.exec(`UPDATE players SET invite_bonus_coins=25 WHERE id=$1`, inviter.ID)
	if err := e.d.Referrals.Remember(context.Background(), 455, inviter.ID); err != nil {
		t.Fatal(err)
	}
	friend := e.login(455, "en", "") // opens the app without a start parameter
	var by *int64
	var bonus int64
	_ = e.d.Pool.QueryRow(context.Background(), `SELECT referred_by, friend_bonus_coins FROM players WHERE id=$1`, friend.ID).Scan(&by, &bonus)
	if by == nil || *by != inviter.ID || bonus != 25 {
		t.Fatalf("remembered invite: %v %d", by, bonus)
	}
	// an existing player is never attributed afterwards
	_ = e.d.Referrals.Remember(context.Background(), 455, inviter.ID)
	var n int
	_ = e.d.Pool.QueryRow(context.Background(), `SELECT count(*) FROM pending_referrals`).Scan(&n)
	if n != 0 {
		t.Fatalf("pending rows %d", n)
	}
}
