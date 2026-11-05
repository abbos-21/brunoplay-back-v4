package api_test

import (
	"errors"
	"fmt"
	"testing"

	"brunoplay/internal/admin"
)

// A boost task works for any channel the owner names: only an active boost of THAT channel by THAT player completes it.
func TestChannelBoostTaskWorksForAnyChannel(t *testing.T) {
	e := newEnv(t)
	e.gameConfig()
	fake, _ := e.moneyConfig()
	e.d.Tasks.SetTelegram(fake)
	content := e.adminLogin("content@example.com", admin.RoleContent)
	p, q := e.login(800, "en", ""), e.login(801, "ru", "")

	if r := e.do("POST", "/api/admin/tasks", content, taskBody("CHANNEL_BOOST", nil)); r.Status != 422 {
		t.Fatalf("a boost task needs a channel: %d %s", r.Status, r.Body)
	}
	newsID := e.task(content, taskBody("CHANNEL_BOOST", map[string]any{"channelUsername": "brunoplay_news", "url": "https://t.me/boost/brunoplay_news", "rewardCoins": 300}))
	otherID := e.task(content, taskBody("CHANNEL_BOOST", map[string]any{"channelUsername": "@another_channel", "url": "https://t.me/boost/another_channel", "rewardCoins": 400, "rewardStars": 2}))

	check := func(who player, id int) map[string]any {
		t.Helper()
		r := e.do("POST", fmt.Sprintf("/api/tasks/%d/check", id), who.Token, nil)
		if r.Status != 200 {
			t.Fatalf("check %d: %d %s", id, r.Status, r.Body)
		}
		return r.JSON(t)
	}

	// no boost yet: not done, with a message that says what to do
	if c := check(p, newsID); c["completed"] != false || c["error"] != "TASK_NOT_BOOSTED" || c["message"] == "" {
		t.Fatalf("not boosted: %v", c)
	}
	// a boost of ANOTHER channel does not count; a boost by another player does not count
	fake.Boosts["@another_channel:800"] = 1
	fake.Boosts["@brunoplay_news:801"] = 1
	if c := check(p, newsID); c["completed"] != false {
		t.Fatalf("a boost of another channel or by another player: %v", c)
	}
	// the other channel's task is completed by its own boost, with its own reward (a channel name given with or without @)
	if c := check(p, otherID); c["completed"] != true || c["reward"].(float64) != 400 || e.coins(p) != 400 || e.stars(p) != 2 {
		t.Fatalf("other channel: %v coins=%d stars=%d", c, e.coins(p), e.stars(p))
	}
	// now the first channel
	fake.Boosts["@brunoplay_news:800"] = 2
	if c := check(p, newsID); c["completed"] != true || c["reward"].(float64) != 300 || e.coins(p) != 700 {
		t.Fatalf("news channel: %v coins=%d", c, e.coins(p))
	}
	// paid once only
	if r := e.do("POST", fmt.Sprintf("/api/tasks/%d/check", newsID), p.Token, nil); r.Code(t) != "TASK_ALREADY_COMPLETED" || e.coins(p) != 700 {
		t.Fatalf("paid twice: %s", r.Body)
	}
	// the second player has their own boost and gets paid separately
	if c := check(q, newsID); c["completed"] != true || e.coins(q) != 300 {
		t.Fatalf("second player: %v", c)
	}

	// Telegram cannot answer (the bot is not an admin of the channel, or Telegram is down): "cannot verify", nothing is paid
	third := e.login(802, "en", "")
	fake.FailBoosts = errors.New("telegram getUserChatBoosts: 400 CHAT_ADMIN_REQUIRED")
	if r := e.do("POST", fmt.Sprintf("/api/tasks/%d/check", newsID), third.Token, nil); r.Code(t) != "TASK_CANNOT_VERIFY" || e.coins(third) != 0 {
		t.Fatalf("telegram error: %d %s", r.Status, r.Body)
	}

	// the type is in the player's task list with the channel's own link
	fake.FailBoosts = nil
	list := e.do("GET", "/api/tasks", third.Token, nil).JSON(t)["tasks"].([]any)
	found := 0
	for _, x := range list {
		if m := x.(map[string]any); m["type"] == "CHANNEL_BOOST" && (m["url"] == "https://t.me/boost/brunoplay_news" || m["url"] == "https://t.me/boost/another_channel") {
			found++
		}
	}
	if found != 2 {
		t.Fatalf("both boost tasks are listed: %v", list)
	}
}
