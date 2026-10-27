package api_test

import (
	"net/url"
	"testing"
	"time"

	"brunoplay/internal/settings"
)

func (e *env) adTaskConfig() {
	e.setSetting(settings.KeyAdsTaskEnabled, true)
	e.setSetting(settings.KeyAdsTaskBlock, "task-777")
	e.setSetting(settings.KeyAdsTaskCoins, 2000)
	e.setSetting(settings.KeyAdsTaskPerDay, 2)
}

// taskCallback is Adsgram's server-to-server call of the Task block's Reward URL.
func (e *env) taskCallback(tg string, extra url.Values) resp {
	q := url.Values{"userid": {tg}, "secret": {"adsecret"}}
	for k, v := range extra {
		q[k] = v
	}
	return e.do("GET", "/api/webhooks/adsgram-task?"+q.Encode(), "", nil)
}

func TestSponsoredTaskPaysOnlyAfterAdsgramCallsBack(t *testing.T) {
	e := newEnv(t)
	e.gameConfig()
	e.adTaskConfig()
	p := e.login(5101, "en", "")

	// the game says "done" but Adsgram never called: nothing is paid
	if r := e.do("POST", "/api/ads/task/claim", p.Token, map[string]any{}); r.Code(t) != "AD_NOT_COMPLETED" {
		t.Fatalf("claim without callback: %d %s", r.Status, r.Body)
	}
	if e.coins(p) != 0 {
		t.Fatal("coins paid without a callback")
	}

	// bad secret / unknown player
	if r := e.do("GET", "/api/webhooks/adsgram-task?userid=5101&secret=wrong", "", nil); r.Status != 401 {
		t.Fatalf("bad secret: %d", r.Status)
	}
	if r := e.taskCallback("999999", nil); r.Status != 200 || r.JSON(t)["ok"] != false {
		t.Fatalf("unknown user: %d %s", r.Status, r.Body)
	}
	// a callback for another block is ignored; "777" and "task-777" are the same block
	if r := e.taskCallback("5101", url.Values{"blockid": {"1234"}}); r.JSON(t)["ok"] != false {
		t.Fatalf("other block must be ignored: %s", r.Body)
	}
	if r := e.taskCallback("5101", url.Values{"blockid": {"777"}}); r.Status != 200 || r.JSON(t)["ok"] != true {
		t.Fatalf("callback: %d %s", r.Status, r.Body)
	}
	// a repeated callback right away counts once
	if r := e.taskCallback("5101", nil); r.JSON(t)["ok"] != false {
		t.Fatalf("repeat within seconds must count once: %s", r.Body)
	}

	r := e.do("POST", "/api/ads/task/claim", p.Token, map[string]any{})
	if r.Status != 200 {
		t.Fatalf("claim: %d %s", r.Status, r.Body)
	}
	if got := r.JSON(t)["coins"]; got != float64(2000) {
		t.Fatalf("coins: %v", got)
	}
	if e.coins(p) != 2000 {
		t.Fatalf("balance: %d", e.coins(p))
	}
	// one completion pays once
	if r := e.do("POST", "/api/ads/task/claim", p.Token, map[string]any{}); r.Code(t) != "AD_NOT_COMPLETED" {
		t.Fatalf("second claim: %s", r.Body)
	}
	if e.coins(p) != 2000 {
		t.Fatalf("paid twice: %d", e.coins(p))
	}
}

func TestSponsoredTaskDailyLimitAndSwitch(t *testing.T) {
	e := newEnv(t)
	e.gameConfig()
	e.adTaskConfig()
	p := e.login(5102, "en", "")

	for i := 0; i < 2; i++ {
		e.now = e.now.Add(time.Minute)
		if r := e.taskCallback("5102", nil); r.JSON(t)["ok"] != true {
			t.Fatalf("callback %d: %s", i, r.Body)
		}
		if r := e.do("POST", "/api/ads/task/claim", p.Token, map[string]any{}); r.Status != 200 {
			t.Fatalf("claim %d: %d %s", i, r.Status, r.Body)
		}
	}
	// the third completion of the day is recorded but not paid, and the day counter is not spent by the refusal
	e.now = e.now.Add(time.Minute)
	e.taskCallback("5102", nil)
	if r := e.do("POST", "/api/ads/task/claim", p.Token, map[string]any{}); r.Code(t) != "AD_LIMIT_REACHED" {
		t.Fatalf("limit: %d %s", r.Status, r.Body)
	}
	if e.coins(p) != 4000 {
		t.Fatalf("balance: %d", e.coins(p))
	}
	cfg := e.do("GET", "/api/ads/config", p.Token, nil).JSON(t)
	task := cfg["task"].(map[string]any)
	if task["left"] != float64(0) || task["enabled"] != true || task["blockId"] != "task-777" {
		t.Fatalf("config: %v", task)
	}

	// the unpaid completion expires after a few hours; on the next game day a new one pays again
	e.now = e.now.Add(24 * time.Hour)
	if r := e.do("POST", "/api/ads/task/claim", p.Token, map[string]any{}); r.Code(t) != "AD_NOT_COMPLETED" {
		t.Fatalf("expired completion: %d %s", r.Status, r.Body)
	}
	if r := e.taskCallback("5102", nil); r.JSON(t)["ok"] != true {
		t.Fatalf("next day callback: %s", r.Body)
	}
	if r := e.do("POST", "/api/ads/task/claim", p.Token, map[string]any{}); r.Status != 200 {
		t.Fatalf("next day claim: %d %s", r.Status, r.Body)
	}

	// switched off: callbacks are ignored and claims refused
	e.setSetting(settings.KeyAdsTaskEnabled, false)
	if r := e.taskCallback("5102", nil); r.JSON(t)["ok"] != false {
		t.Fatalf("callback while off: %s", r.Body)
	}
	if r := e.do("POST", "/api/ads/task/claim", p.Token, map[string]any{}); r.Code(t) != "AD_PLACEMENT_DISABLED" {
		t.Fatalf("claim while off: %d %s", r.Status, r.Body)
	}
}

func TestInterstitialConfigFollowsTheSettings(t *testing.T) {
	e := newEnv(t)
	e.gameConfig()
	p := e.login(5103, "en", "")
	get := func() map[string]any {
		return e.do("GET", "/api/ads/config", p.Token, nil).JSON(t)["interstitial"].(map[string]any)
	}
	// off by default, and a block id alone does not switch it on
	if c := get(); c["enabled"] != false {
		t.Fatalf("default: %v", c)
	}
	e.setSetting(settings.KeyAdsInterBlock, "12345")
	if c := get(); c["enabled"] != false {
		t.Fatalf("block id alone: %v", c)
	}
	e.setSetting(settings.KeyAdsInterEnabled, true)
	e.setSetting(settings.KeyAdsInterGap, 240)
	e.setSetting(settings.KeyAdsInterFirst, 60)
	c := get()
	if c["enabled"] != true || c["blockId"] != "12345" || c["intervalSeconds"] != float64(240) || c["firstAfterSeconds"] != float64(60) {
		t.Fatalf("configured: %v", c)
	}
	// enabled without a block id stays off
	e.setSetting(settings.KeyAdsInterBlock, "")
	if c := get(); c["enabled"] != false {
		t.Fatalf("no block id: %v", c)
	}
}
