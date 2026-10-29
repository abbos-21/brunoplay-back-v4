package api_test

import (
	"testing"
	"time"

	"brunoplay/internal/admin"
	"brunoplay/internal/settings"
)

func providerIDs(t *testing.T, view map[string]any) []string {
	t.Helper()
	list, _ := view["providers"].([]any)
	out := []string{}
	for _, x := range list {
		out = append(out, x.(map[string]any)["id"].(string))
	}
	return out
}

func eq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestAdProvidersPriorityAndSwitches(t *testing.T) {
	e := newEnv(t)
	e.gameConfig()
	boss := e.adminLogin("boss@example.com", admin.RoleSuper)
	help := e.adminLogin("help@example.com", admin.RoleSupport)
	p := e.login(7001, "en", "")
	save := func(token string, body map[string]any) resp { return e.do("PUT", "/api/admin/ads/providers", token, body) }
	pr := func(id string, on bool) map[string]any { return map[string]any{"id": id, "enabled": on} }
	// the page always sends every provider: Monetag is added (off) where a case does not care about it
	list := func(l ...map[string]any) []map[string]any {
		for _, x := range l {
			if x["id"] == "monetag" {
				return l
			}
		}
		return append(l, pr("monetag", false))
	}

	// by default only Adsgram serves ads, TADS is off and not set up
	view := e.do("POST", "/api/ads", p.Token, map[string]any{"placement": "FISHING_START"}).JSON(t)
	if ids := providerIDs(t, view); !eq(ids, []string{"adsgram"}) || view["blockId"] != "blk-start" {
		t.Fatalf("default chain: %v", view)
	}

	// only the super admin edits it, and the lists are validated
	if r := save(help, map[string]any{"providers": list(pr("tads", true), pr("adsgram", true))}); r.Status != 403 {
		t.Fatalf("support: %d %s", r.Status, r.Body)
	}
	for name, body := range map[string]map[string]any{
		"unknown":   {"providers": list(pr("adsgram", true), pr("tads", true), pr("nope", true))},
		"duplicate": {"providers": list(pr("adsgram", true), pr("adsgram", true), pr("tads", true))},
		"missing":   {"providers": list(pr("adsgram", true))},
		"widget":    {"providers": list(pr("adsgram", true), pr("tads", true)), "fields": map[string]any{settings.KeyAdsTadsWidget: "abc"}},
		"field":     {"providers": list(pr("adsgram", true), pr("tads", true)), "fields": map[string]any{"ads.required": false}},
	} {
		if r := save(boss, body); r.Code(t) != "ADMIN_ACTION_REFUSED" {
			t.Fatalf("%s: %d %s", name, r.Status, r.Body)
		}
	}

	// TADS on but without a widget id: still not offered
	if r := save(boss, map[string]any{"providers": list(pr("tads", true), pr("adsgram", true))}); r.Status != 200 {
		t.Fatalf("save: %d %s", r.Status, r.Body)
	}
	view = e.do("POST", "/api/ads", p.Token, map[string]any{"placement": "FISHING_START"}).JSON(t)
	if ids := providerIDs(t, view); !eq(ids, []string{"adsgram"}) {
		t.Fatalf("tads without widget: %v", ids)
	}

	// TADS first, with a widget: the app asks TADS, then Adsgram
	r := save(boss, map[string]any{"providers": list(pr("tads", true), pr("adsgram", true)), "fields": map[string]any{settings.KeyAdsTadsWidget: "12941", settings.KeyAdsTadsWait: 0}})
	if r.Status != 200 {
		t.Fatalf("save: %d %s", r.Status, r.Body)
	}
	view = e.do("POST", "/api/ads", p.Token, map[string]any{"placement": "FISHING_START"}).JSON(t)
	if ids := providerIDs(t, view); !eq(ids, []string{"tads", "adsgram"}) {
		t.Fatalf("priority: %v", view)
	}
	if first := view["providers"].([]any)[0].(map[string]any); first["widgetId"] != "12941" {
		t.Fatalf("widget: %v", first)
	}

	// the TADS callback needs the secret, the right widget, and completes the proof; fishing then starts
	url := "/api/webhooks/tads"
	if r := e.do("POST", url+"?secret=wrong", "", map[string]any{"telegram_id": "7001", "widget_id": "12941"}); r.Status != 401 {
		t.Fatalf("wrong secret: %d %s", r.Status, r.Body)
	}
	if r := e.do("POST", url+"?secret=adsecret", "", map[string]any{"telegram_id": "7001", "widget_id": "999"}); r.JSON(t)["ok"] != false {
		t.Fatalf("other widget: %s", r.Body)
	}
	if r := e.do("POST", "/api/fishing/start", p.Token, map[string]any{"adViewId": view["adViewId"]}); r.Code(t) != "AD_NOT_COMPLETED" {
		t.Fatalf("before the callback: %d %s", r.Status, r.Body)
	}
	if r := e.do("POST", url+"?secret=adsecret", "", map[string]any{"telegram_id": "7001", "widget_id": "12941"}); r.JSON(t)["ok"] != true {
		t.Fatalf("tads callback: %s", r.Body)
	}
	if r := e.do("POST", "/api/fishing/start", p.Token, map[string]any{"adViewId": view["adViewId"]}); r.Status != 200 {
		t.Fatalf("start after tads: %d %s", r.Status, r.Body)
	}

	// a TADS callback cannot complete a proof that did not offer TADS, and Adsgram's cannot complete one that offered only TADS
	save(boss, map[string]any{"providers": list(pr("adsgram", true), pr("tads", false))})
	v2 := e.do("POST", "/api/ads", p.Token, map[string]any{"placement": "COLLECT"}).JSON(t)
	if ids := providerIDs(t, v2); !eq(ids, []string{"adsgram"}) {
		t.Fatalf("tads off: %v", ids)
	}
	if r := e.do("POST", url+"?secret=adsecret", "", map[string]any{"telegram_id": "7001"}); r.JSON(t)["ok"] != false {
		t.Fatalf("tads must not complete an adsgram-only proof: %s", r.Body)
	}
	save(boss, map[string]any{"providers": list(pr("adsgram", false), pr("tads", true))})
	v3 := e.do("POST", "/api/ads", p.Token, map[string]any{"placement": "COLLECT"}).JSON(t)
	if ids := providerIDs(t, v3); !eq(ids, []string{"tads"}) || v3["blockId"] != "" {
		t.Fatalf("adsgram off: %v", v3)
	}
	q := "/api/webhooks/adsgram?userid=7001&blockid=blk-collect&secret=adsecret"
	if r := e.do("GET", q, "", nil); r.JSON(t)["ok"] != false {
		t.Fatalf("adsgram must not complete a tads-only proof: %s", r.Body)
	}

	// with Adsgram off its interstitial and sponsored task go too
	e.setSetting(settings.KeyAdsInterEnabled, true)
	e.setSetting(settings.KeyAdsInterBlock, "int-1")
	e.setSetting(settings.KeyAdsTaskEnabled, true)
	e.setSetting(settings.KeyAdsTaskBlock, "task-1")
	cfg := e.do("GET", "/api/ads/config", p.Token, nil).JSON(t)
	if cfg["interstitial"].(map[string]any)["enabled"] != false || cfg["task"].(map[string]any)["enabled"] != false {
		t.Fatalf("adsgram off: %v", cfg)
	}
	save(boss, map[string]any{"providers": list(pr("adsgram", true), pr("tads", true))})
	cfg = e.do("GET", "/api/ads/config", p.Token, nil).JSON(t)
	if cfg["interstitial"].(map[string]any)["enabled"] != true || cfg["task"].(map[string]any)["enabled"] != true {
		t.Fatalf("adsgram on: %v", cfg)
	}

	// every provider off while ads are required: CONFIG_MISSING instead of a silent free pass
	save(boss, map[string]any{"providers": list(pr("adsgram", false), pr("tads", false))})
	if r := e.do("POST", "/api/ads", p.Token, map[string]any{"placement": "COLLECT"}); r.Code(t) != "CONFIG_MISSING" {
		t.Fatalf("all off: %d %s", r.Status, r.Body)
	}
	// ...and with ads not required the action simply needs no proof
	e.setSetting(settings.KeyAdsRequired, false)
	if v := e.do("POST", "/api/ads", p.Token, map[string]any{"placement": "COLLECT"}).JSON(t); v["required"] != false {
		t.Fatalf("not required: %v", v)
	}

	// the admin page shows the providers in order, with what is missing
	pv := e.do("GET", "/api/admin/ads/providers", boss, nil).JSON(t)
	if ids := providerIDs(t, pv); !eq(ids, []string{"adsgram", "tads", "monetag"}) {
		t.Fatalf("admin view: %v", pv)
	}
}

// TADS reports an ad when it STARTS. The proof it completes can be spent only after the wait set in the admin panel.
func TestTadsProofWaitsForTheAdToEnd(t *testing.T) {
	e := newEnv(t)
	e.gameConfig()
	boss := e.adminLogin("boss@example.com", admin.RoleSuper)
	p := e.login(7002, "en", "")
	body := map[string]any{
		"providers": []map[string]any{{"id": "tads", "enabled": true}, {"id": "adsgram", "enabled": false}, {"id": "monetag", "enabled": false}},
		"fields":    map[string]any{settings.KeyAdsTadsWidget: "12941", settings.KeyAdsTadsWait: 30},
	}
	if r := e.do("PUT", "/api/admin/ads/providers", boss, body); r.Status != 200 {
		t.Fatalf("save: %d %s", r.Status, r.Body)
	}
	// a wait that is not a whole number, or out of range, is refused
	for _, bad := range []any{1.5, "x", -1, 500} {
		body["fields"] = map[string]any{settings.KeyAdsTadsWait: bad}
		if r := e.do("PUT", "/api/admin/ads/providers", boss, body); r.Status == 200 {
			t.Fatalf("wait %v must be refused: %s", bad, r.Body)
		}
	}

	view := e.do("POST", "/api/ads", p.Token, map[string]any{"placement": "FISHING_START"}).JSON(t)
	if ref := view["providers"].([]any)[0].(map[string]any); ref["waitSeconds"] != float64(30) {
		t.Fatalf("the app must be told the wait: %v", ref)
	}
	hook := "/api/webhooks/tads?secret=adsecret"
	if r := e.do("POST", hook, "", map[string]any{"telegram_id": "7002", "widget_id": "12941"}); r.JSON(t)["ok"] != true {
		t.Fatalf("callback: %s", r.Body)
	}
	start := func() resp { return e.do("POST", "/api/fishing/start", p.Token, map[string]any{"adViewId": view["adViewId"]}) }

	// reported, but the ad is still running: not usable yet
	e.now = e.now.Add(10 * time.Second)
	if r := start(); r.Code(t) != "AD_NOT_COMPLETED" {
		t.Fatalf("10s after the report: %d %s", r.Status, r.Body)
	}
	e.now = e.now.Add(21 * time.Second)
	if r := start(); r.Status != 200 {
		t.Fatalf("31s after the report: %d %s", r.Status, r.Body)
	}
}

// The TADS test card (TGB widget) on the Tasks screen: only when its widget id is set and TADS is on; it never needs a proof.
func TestTadsTestCardFollowsTheAdminSettings(t *testing.T) {
	e := newEnv(t)
	e.gameConfig()
	boss := e.adminLogin("boss@example.com", admin.RoleSuper)
	p := e.login(7003, "en", "")
	save := func(on bool, tgb any) resp {
		return e.do("PUT", "/api/admin/ads/providers", boss, map[string]any{
			"providers": []map[string]any{{"id": "adsgram", "enabled": true}, {"id": "tads", "enabled": on}, {"id": "monetag", "enabled": false}},
			"fields":    map[string]any{settings.KeyAdsTadsTGB: tgb},
		})
	}
	banner := func() map[string]any {
		return e.do("GET", "/api/ads/config", p.Token, nil).JSON(t)["banner"].(map[string]any)
	}
	if b := banner(); b["enabled"] != false {
		t.Fatalf("default: %v", b)
	}
	if r := save(true, "abc"); r.Code(t) != "ADMIN_ACTION_REFUSED" {
		t.Fatalf("a widget id is a number: %d %s", r.Status, r.Body)
	}
	if r := save(true, "12944"); r.Status != 200 {
		t.Fatalf("save: %d %s", r.Status, r.Body)
	}
	if b := banner(); b["enabled"] != true || b["widgetId"] != "12944" || b["provider"] != "tads" {
		t.Fatalf("on: %v", b)
	}
	if r := save(false, "12944"); r.Status != 200 {
		t.Fatal(r.Body)
	}
	if b := banner(); b["enabled"] != false {
		t.Fatalf("tads off: %v", b)
	}
	save(true, "12944")
	e.exec(`UPDATE players SET ads_disabled=TRUE WHERE id=$1`, p.ID)
	if b := banner(); b["enabled"] != false {
		t.Fatalf("ads off for the player: %v", b)
	}
}

func TestMonetagPostbackCompletesTheExactProof(t *testing.T) {
	e := newEnv(t)
	e.gameConfig()
	boss := e.adminLogin("boss@example.com", admin.RoleSuper)
	p := e.login(7101, "en", "")
	other := e.login(7102, "en", "")
	save := func(body map[string]any) resp { return e.do("PUT", "/api/admin/ads/providers", boss, body) }
	pr := func(id string, on bool) map[string]any { return map[string]any{"id": id, "enabled": on} }
	all := func(adsgram, tads, monetag bool) []map[string]any {
		return []map[string]any{pr("monetag", monetag), pr("adsgram", adsgram), pr("tads", tads)}
	}

	// a zone id must be a number
	if r := save(map[string]any{"providers": all(true, false, true), "fields": map[string]any{settings.KeyAdsMonetagZone: "abc"}}); r.Code(t) != "ADMIN_ACTION_REFUSED" {
		t.Fatalf("zone: %d %s", r.Status, r.Body)
	}
	// on but without a zone: not offered
	if r := save(map[string]any{"providers": all(true, false, true)}); r.Status != 200 {
		t.Fatalf("save: %d %s", r.Status, r.Body)
	}
	view := e.do("POST", "/api/ads", p.Token, map[string]any{"placement": "FISHING_START"}).JSON(t)
	if ids := providerIDs(t, view); !eq(ids, []string{"adsgram"}) {
		t.Fatalf("no zone: %v", ids)
	}
	// Monetag first, with a zone and no wait
	if r := save(map[string]any{"providers": all(true, false, true), "fields": map[string]any{settings.KeyAdsMonetagZone: "12144526", settings.KeyAdsMonetagWait: 0}}); r.Status != 200 {
		t.Fatalf("save: %d %s", r.Status, r.Body)
	}
	view = e.do("POST", "/api/ads", p.Token, map[string]any{"placement": "FISHING_START"}).JSON(t)
	if ids := providerIDs(t, view); !eq(ids, []string{"monetag", "adsgram"}) {
		t.Fatalf("chain: %v", ids)
	}
	if first := view["providers"].([]any)[0].(map[string]any); first["zoneId"] != "12144526" {
		t.Fatalf("zone in the chain: %v", first)
	}
	id := view["adViewId"].(string)
	hook := func(q string) map[string]any { return e.do("GET", "/api/webhooks/monetag?"+q, "", nil).JSON(t) }
	base := "secret=adsecret&zone_id=12144526&event_type=impression&reward_event_type=valued&ymid=" + id

	if r := e.do("GET", "/api/webhooks/monetag?secret=wrong&ymid="+id+"&reward_event_type=valued", "", nil); r.Status != 401 {
		t.Fatalf("wrong secret: %d %s", r.Status, r.Body)
	}
	// unpaid, other zone, other player, unknown id, junk id: nothing is completed
	for name, q := range map[string]string{
		"non_valued": "secret=adsecret&event_type=impression&reward_event_type=non_valued&ymid=" + id,
		"other zone": "secret=adsecret&zone_id=999&reward_event_type=valued&ymid=" + id,
		"other user": base + "&telegram_id=7102",
		"unknown":    "secret=adsecret&reward_event_type=valued&ymid=0123456789abcdef01234567",
		"junk":       "secret=adsecret&reward_event_type=valued&ymid=x%27%3B%20drop",
	} {
		if r := hook(q); r["ok"] != false {
			t.Fatalf("%s: %v", name, r)
		}
	}
	if r := e.do("POST", "/api/fishing/start", p.Token, map[string]any{"adViewId": id}); r.Code(t) != "AD_NOT_COMPLETED" {
		t.Fatalf("before the postback: %d %s", r.Status, r.Body)
	}
	// the real one; a click or a retry of the same ad answers 200 without doing anything again
	if r := hook(base + "&telegram_id=7101"); r["ok"] != true {
		t.Fatalf("postback: %v", r)
	}
	if r := hook(base + "&event_type=click"); r["ok"] != false {
		t.Fatalf("repeat: %v", r)
	}
	if r := e.do("POST", "/api/fishing/start", p.Token, map[string]any{"adViewId": id}); r.Status != 200 {
		t.Fatalf("start after monetag: %d %s", r.Status, r.Body)
	}

	// with a wait the proof cannot be spent before the ad has ended
	save(map[string]any{"providers": all(true, false, true), "fields": map[string]any{settings.KeyAdsMonetagWait: 30}})
	v2 := e.do("POST", "/api/ads", other.Token, map[string]any{"placement": "FISHING_START"}).JSON(t)
	id2 := v2["adViewId"].(string)
	if r := hook("secret=adsecret&reward_event_type=valued&ymid=" + id2); r["ok"] != true {
		t.Fatalf("second postback: %v", r)
	}
	start := func() resp { return e.do("POST", "/api/fishing/start", other.Token, map[string]any{"adViewId": id2}) }
	e.now = e.now.Add(10 * time.Second)
	if r := start(); r.Code(t) != "AD_NOT_COMPLETED" {
		t.Fatalf("10s after the postback: %d %s", r.Status, r.Body)
	}
	e.now = e.now.Add(21 * time.Second)
	if r := start(); r.Status != 200 {
		t.Fatalf("31s after the postback: %d %s", r.Status, r.Body)
	}
}
