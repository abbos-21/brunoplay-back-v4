package api_test

import (
	"strings"
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
	zone := map[string]any{settings.KeyAdsMonetagZone: "12144526", settings.KeyAdsMonetagWait: 0}

	// by default only Adsgram serves ads, Monetag is off and not set up
	view := e.do("POST", "/api/ads", p.Token, map[string]any{"placement": "FISHING_START"}).JSON(t)
	if ids := providerIDs(t, view); !eq(ids, []string{"adsgram"}) || view["blockId"] != "blk-start" {
		t.Fatalf("default chain: %v", view)
	}

	// only the super admin edits it, and the lists are validated
	if r := save(help, map[string]any{"providers": []map[string]any{pr("monetag", true), pr("adsgram", true)}}); r.Status != 403 {
		t.Fatalf("support: %d %s", r.Status, r.Body)
	}
	for name, body := range map[string]map[string]any{
		"unknown":   {"providers": []map[string]any{pr("adsgram", true), pr("monetag", true), pr("nope", true)}},
		"duplicate": {"providers": []map[string]any{pr("adsgram", true), pr("adsgram", true), pr("monetag", true)}},
		"missing":   {"providers": []map[string]any{pr("adsgram", true)}},
		"zone":      {"providers": []map[string]any{pr("adsgram", true), pr("monetag", true)}, "fields": map[string]any{settings.KeyAdsMonetagZone: "abc"}},
		"field":     {"providers": []map[string]any{pr("adsgram", true), pr("monetag", true)}, "fields": map[string]any{"ads.required": false}},
		"retired":   {"providers": []map[string]any{pr("adsgram", true), pr("monetag", true), pr("t"+"ads", true)}}, // a provider that was removed
	} {
		if r := save(boss, body); r.Code(t) != "ADMIN_ACTION_REFUSED" {
			t.Fatalf("%s: %d %s", name, r.Status, r.Body)
		}
	}

	// Monetag on but without a zone id: still not offered
	if r := save(boss, map[string]any{"providers": []map[string]any{pr("monetag", true), pr("adsgram", true)}}); r.Status != 200 {
		t.Fatalf("save: %d %s", r.Status, r.Body)
	}
	view = e.do("POST", "/api/ads", p.Token, map[string]any{"placement": "FISHING_START"}).JSON(t)
	if ids := providerIDs(t, view); !eq(ids, []string{"adsgram"}) {
		t.Fatalf("monetag without a zone: %v", ids)
	}

	// Monetag first, with a zone: the app asks Monetag, then Adsgram
	if r := save(boss, map[string]any{"providers": []map[string]any{pr("monetag", true), pr("adsgram", true)}, "fields": zone}); r.Status != 200 {
		t.Fatalf("save: %d %s", r.Status, r.Body)
	}
	view = e.do("POST", "/api/ads", p.Token, map[string]any{"placement": "FISHING_START"}).JSON(t)
	if ids := providerIDs(t, view); !eq(ids, []string{"monetag", "adsgram"}) {
		t.Fatalf("priority: %v", view)
	}
	if first := view["providers"].([]any)[0].(map[string]any); first["zoneId"] != "12144526" {
		t.Fatalf("zone: %v", first)
	}

	// Adsgram's callback cannot complete a proof that offered only Monetag
	save(boss, map[string]any{"providers": []map[string]any{pr("adsgram", false), pr("monetag", true)}})
	v3 := e.do("POST", "/api/ads", p.Token, map[string]any{"placement": "COLLECT"}).JSON(t)
	if ids := providerIDs(t, v3); !eq(ids, []string{"monetag"}) || v3["blockId"] != "" {
		t.Fatalf("adsgram off: %v", v3)
	}
	q := "/api/webhooks/adsgram?userid=7001&blockid=blk-collect&secret=adsecret"
	if r := e.do("GET", q, "", nil); r.JSON(t)["ok"] != false {
		t.Fatalf("adsgram must not complete a monetag-only proof: %s", r.Body)
	}

	// with Adsgram off its interstitial and sponsored task go too
	e.setSetting(settings.KeyAdsInterEnabled, true)
	e.setSetting(settings.KeyAdsInterBlock, "int-1")
	e.setSetting(settings.KeyAdsTaskEnabled, true)
	e.setSetting(settings.KeyAdsTaskBlock, "task-1")
	cfg := e.do("GET", "/api/ads/config", p.Token, nil).JSON(t)
	if cfg["task"].(map[string]any)["enabled"] != false {
		t.Fatalf("adsgram off: %v", cfg)
	}
	save(boss, map[string]any{"providers": []map[string]any{pr("adsgram", true), pr("monetag", true)}})
	cfg = e.do("GET", "/api/ads/config", p.Token, nil).JSON(t)
	if cfg["interstitial"].(map[string]any)["enabled"] != true || cfg["task"].(map[string]any)["enabled"] != true {
		t.Fatalf("adsgram on: %v", cfg)
	}

	// every provider off while ads are required: CONFIG_MISSING instead of a silent free pass
	save(boss, map[string]any{"providers": []map[string]any{pr("adsgram", false), pr("monetag", false)}})
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
	if ids := providerIDs(t, pv); !eq(ids, []string{"adsgram", "monetag"}) {
		t.Fatalf("admin view: %v", pv)
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
	all := func(adsgram, monetag bool) []map[string]any {
		return []map[string]any{pr("monetag", monetag), pr("adsgram", adsgram)}
	}

	// a zone id must be a number
	if r := save(map[string]any{"providers": all(true, true), "fields": map[string]any{settings.KeyAdsMonetagZone: "abc"}}); r.Code(t) != "ADMIN_ACTION_REFUSED" {
		t.Fatalf("zone: %d %s", r.Status, r.Body)
	}
	// on but without a zone: not offered
	if r := save(map[string]any{"providers": all(true, true)}); r.Status != 200 {
		t.Fatalf("save: %d %s", r.Status, r.Body)
	}
	view := e.do("POST", "/api/ads", p.Token, map[string]any{"placement": "FISHING_START"}).JSON(t)
	if ids := providerIDs(t, view); !eq(ids, []string{"adsgram"}) {
		t.Fatalf("no zone: %v", ids)
	}
	// Monetag first, with a zone and no wait
	if r := save(map[string]any{"providers": all(true, true), "fields": map[string]any{settings.KeyAdsMonetagZone: "12144526", settings.KeyAdsMonetagWait: 0}}); r.Status != 200 {
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
	if r := hook(strings.Replace(base, "ymid="+id, "ymid="+id+"_2", 1) + "&telegram_id=7101"); r["ok"] != true {
		t.Fatalf("postback: %v", r)
	}
	if r := hook(base + "&event_type=click"); r["ok"] != false {
		t.Fatalf("repeat: %v", r)
	}
	if r := e.do("POST", "/api/fishing/start", p.Token, map[string]any{"adViewId": id}); r.Status != 200 {
		t.Fatalf("start after monetag: %d %s", r.Status, r.Body)
	}

	// with a wait the proof cannot be spent before the ad has ended
	save(map[string]any{"providers": all(true, true), "fields": map[string]any{settings.KeyAdsMonetagWait: 30}})
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

func TestInterstitialChainFollowsProvidersAndSwitches(t *testing.T) {
	e := newEnv(t)
	e.gameConfig()
	boss := e.adminLogin("boss@example.com", admin.RoleSuper)
	p := e.login(7201, "en", "")
	pr := func(id string, on bool) map[string]any { return map[string]any{"id": id, "enabled": on} }
	save := func(order []map[string]any, fields map[string]any) {
		t.Helper()
		if r := e.do("PUT", "/api/admin/ads/providers", boss, map[string]any{"providers": order, "fields": fields}); r.Status != 200 {
			t.Fatalf("save: %d %s", r.Status, r.Body)
		}
	}
	inter := func() map[string]any {
		return e.do("GET", "/api/ads/config", p.Token, nil).JSON(t)["interstitial"].(map[string]any)
	}
	ids := func(c map[string]any) []string {
		out := []string{}
		for _, x := range c["providers"].([]any) {
			out = append(out, x.(map[string]any)["id"].(string))
		}
		return out
	}
	e.setSetting(settings.KeyAdsInterEnabled, true)
	e.setSetting(settings.KeyAdsInterBlock, "int-1")
	zone := map[string]any{settings.KeyAdsMonetagZone: "12144526"}

	// Adsgram alone (Monetag has no zone yet): as before
	save([]map[string]any{pr("adsgram", true), pr("monetag", true)}, nil)
	if c := inter(); c["enabled"] != true || !eq(ids(c), []string{"adsgram"}) || c["blockId"] != "int-1" {
		t.Fatalf("adsgram only: %v", c)
	}
	// both: in the priority order, Monetag as the backup
	save([]map[string]any{pr("adsgram", true), pr("monetag", true)}, zone)
	if c := inter(); !eq(ids(c), []string{"adsgram", "monetag"}) {
		t.Fatalf("both: %v", c)
	}
	save([]map[string]any{pr("monetag", true), pr("adsgram", true)}, nil)
	if c := inter(); !eq(ids(c), []string{"monetag", "adsgram"}) {
		t.Fatalf("monetag first: %v", c)
	}
	// Adsgram off: Monetag alone still serves interstitials (the old app sees no block and shows none)
	save([]map[string]any{pr("adsgram", false), pr("monetag", true)}, nil)
	if c := inter(); c["enabled"] != true || !eq(ids(c), []string{"monetag"}) || c["blockId"] != "int-1" {
		t.Fatalf("monetag only: %v", c)
	}
	// Monetag off too, or the interstitial switch off: nothing
	save([]map[string]any{pr("adsgram", false), pr("monetag", false)}, nil)
	if c := inter(); c["enabled"] != false || len(ids(c)) != 0 {
		t.Fatalf("all off: %v", c)
	}
	save([]map[string]any{pr("adsgram", true), pr("monetag", true)}, nil)
	e.setSetting(settings.KeyAdsInterEnabled, false)
	if c := inter(); c["enabled"] != false {
		t.Fatalf("switch off: %v", c)
	}
}
