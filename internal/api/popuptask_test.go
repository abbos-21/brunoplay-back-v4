package api_test

import (
	"testing"
	"time"

	"brunoplay/internal/admin"
	"brunoplay/internal/settings"
)

// Monetag's Rewarded Popup as a task card: the game holds a proof before the tap, Monetag's postback completes it, and only then
// (after the wait) the coins can be claimed, once per interval.
func TestMonetagPopupTask(t *testing.T) {
	e := newEnv(t)
	e.gameConfig()
	boss := e.adminLogin("boss@example.com", admin.RoleSuper)
	p := e.login(7301, "en", "")
	other := e.login(7302, "en", "")
	pr := func(id string, on bool) map[string]any { return map[string]any{"id": id, "enabled": on} }
	save := func(monetag bool, fields map[string]any) {
		t.Helper()
		body := map[string]any{"providers": []map[string]any{pr("adsgram", true), pr("tads", false), pr("monetag", monetag)}, "fields": fields}
		if r := e.do("PUT", "/api/admin/ads/providers", boss, body); r.Status != 200 {
			t.Fatalf("save: %d %s", r.Status, r.Body)
		}
	}
	status := func() map[string]any {
		return e.do("GET", "/api/ads/config", p.Token, nil).JSON(t)["popupTask"].(map[string]any)
	}
	prepare := func(token string) resp { return e.do("POST", "/api/ads/popup-task/prepare", token, map[string]any{}) }
	claim := func(token, id string) resp {
		return e.do("POST", "/api/ads/popup-task/claim", token, map[string]any{"adViewId": id})
	}
	postback := func(id string) map[string]any {
		return e.do("GET", "/api/webhooks/monetag?secret=adsecret&zone_id=12144526&event_type=impression&reward_event_type=valued&ymid="+id, "", nil).JSON(t)
	}

	// off by default
	save(true, map[string]any{settings.KeyAdsMonetagZone: "12144526", settings.KeyAdsMonetagWait: 20})
	if st := status(); st["enabled"] != false {
		t.Fatalf("default: %v", st)
	}
	if r := prepare(p.Token); r.Code(t) != "AD_PLACEMENT_DISABLED" {
		t.Fatalf("prepare while off: %d %s", r.Status, r.Body)
	}

	// on: the admin's coins and interval reach the game
	save(true, map[string]any{settings.KeyAdsMonetagTask: true, settings.KeyAdsMonetagTaskCoins: 500, settings.KeyAdsMonetagTaskInterval: 60})
	st := status()
	if st["enabled"] != true || st["available"] != true || st["coins"] != float64(500) || st["zoneId"] != "12144526" || st["intervalMinutes"] != float64(60) {
		t.Fatalf("on: %v", st)
	}

	// the proof is held before the tap; asking again gives the same one
	first := prepare(p.Token).JSON(t)
	id := first["adViewId"].(string)
	if id == "" || first["completed"] != false {
		t.Fatalf("prepare: %v", first)
	}
	if again := prepare(p.Token).JSON(t); again["adViewId"] != id {
		t.Fatalf("prepare twice: %v", again)
	}
	// nothing is paid before Monetag's postback
	if r := claim(p.Token, id); r.Code(t) != "AD_NOT_COMPLETED" {
		t.Fatalf("claim before the postback: %d %s", r.Status, r.Body)
	}
	// a postback of an unknown proof, and the postback of another player's tap, change nothing for this player
	if r := e.do("GET", "/api/webhooks/monetag?secret=adsecret&reward_event_type=valued&ymid=0123456789abcdef01234567", "", nil).JSON(t); r["ok"] != false {
		t.Fatalf("unknown proof: %v", r)
	}
	if r := postback(id); r["ok"] != true {
		t.Fatalf("postback: %v", r)
	}
	// confirmed, but the wait (the ad's length, set in the admin page) is not over
	e.now = e.now.Add(5 * time.Second)
	if r := claim(p.Token, id); r.Code(t) != "AD_NOT_COMPLETED" {
		t.Fatalf("claim inside the wait: %d %s", r.Status, r.Body)
	}
	// another player cannot claim this proof
	if r := claim(other.Token, id); r.Status == 200 {
		t.Fatalf("someone else's proof was paid: %s", r.Body)
	}
	e.now = e.now.Add(20 * time.Second)
	r := claim(p.Token, id)
	if r.Status != 200 || r.JSON(t)["coins"] != float64(500) {
		t.Fatalf("claim: %d %s", r.Status, r.Body)
	}
	// once: the same proof again, and the next task before the interval
	if r := claim(p.Token, id); r.Status == 200 {
		t.Fatalf("paid twice: %s", r.Body)
	}
	if r := prepare(p.Token); r.Code(t) != "AD_TASK_NOT_READY" {
		t.Fatalf("prepare inside the interval: %d %s", r.Status, r.Body)
	}
	if st := status(); st["available"] != false || st["nextInSeconds"].(float64) <= 0 {
		t.Fatalf("after the claim: %v", st)
	}
	// the interval over: a new proof
	e.now = e.now.Add(61 * time.Minute)
	if next := prepare(p.Token).JSON(t); next["adViewId"] == id || next["adViewId"] == "" {
		t.Fatalf("next proof: %v", next)
	}

	// Monetag switched off: no card, no new proof (the admin session of the test clock has expired by now)
	boss = e.adminLogin("boss2@example.com", admin.RoleSuper)
	save(false, nil)
	if st := status(); st["enabled"] != false {
		t.Fatalf("monetag off: %v", st)
	}
	if r := prepare(p.Token); r.Code(t) != "AD_PLACEMENT_DISABLED" {
		t.Fatalf("prepare with monetag off: %d %s", r.Status, r.Body)
	}
}
