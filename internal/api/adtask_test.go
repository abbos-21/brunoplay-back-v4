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
	e.setSetting(settings.KeyAdsTaskInterval, 60)
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
	if r := e.do("POST", "/api/ads/task/claim", p.Token, map[string]any{}); r.Code(t) != "AD_TASK_NOT_READY" {
		t.Fatalf("second claim: %s", r.Body)
	}
	if e.coins(p) != 2000 {
		t.Fatalf("paid twice: %d", e.coins(p))
	}
}

func TestSponsoredTaskIntervalAndSwitch(t *testing.T) {
	e := newEnv(t)
	e.gameConfig()
	e.adTaskConfig() // one paid task per 60 minutes
	p := e.login(5102, "en", "")
	task := func() map[string]any {
		return e.do("GET", "/api/ads/config", p.Token, nil).JSON(t)["task"].(map[string]any)
	}

	if c := task(); c["available"] != true || c["nextInSeconds"] != float64(0) || c["intervalMinutes"] != float64(60) {
		t.Fatalf("before the first task: %v", c)
	}
	if r := e.taskCallback("5102", nil); r.JSON(t)["ok"] != true {
		t.Fatalf("callback: %s", r.Body)
	}
	if r := e.do("POST", "/api/ads/task/claim", p.Token, map[string]any{}); r.Status != 200 {
		t.Fatalf("claim: %d %s", r.Status, r.Body)
	}

	// during the interval: nothing to do, a callback is not kept, a claim is refused, and the status says when it is back
	e.now = e.now.Add(20 * time.Minute)
	if c := task(); c["available"] != false || c["nextInSeconds"] != float64(40*60) {
		t.Fatalf("during the wait: %v", c)
	}
	if r := e.taskCallback("5102", nil); r.JSON(t)["ok"] != false {
		t.Fatalf("callback during the wait must be ignored: %s", r.Body)
	}
	if r := e.do("POST", "/api/ads/task/claim", p.Token, map[string]any{}); r.Code(t) != "AD_TASK_NOT_READY" {
		t.Fatalf("claim during the wait: %d %s", r.Status, r.Body)
	}
	if e.coins(p) != 2000 {
		t.Fatalf("balance: %d", e.coins(p))
	}

	// when the interval is over the next task pays again
	e.now = e.now.Add(41 * time.Minute)
	if c := task(); c["available"] != true || c["nextInSeconds"] != float64(0) {
		t.Fatalf("after the wait: %v", c)
	}
	if r := e.taskCallback("5102", nil); r.JSON(t)["ok"] != true {
		t.Fatalf("next callback: %s", r.Body)
	}
	if r := e.do("POST", "/api/ads/task/claim", p.Token, map[string]any{}); r.Status != 200 {
		t.Fatalf("next claim: %d %s", r.Status, r.Body)
	}
	if e.coins(p) != 4000 {
		t.Fatalf("balance: %d", e.coins(p))
	}

	// switched off: callbacks are ignored and claims refused
	e.now = e.now.Add(2 * time.Hour)
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
