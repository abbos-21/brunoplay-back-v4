package api_test

import (
	"testing"

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
	list := func(l ...map[string]any) []map[string]any { return l }
	pr := func(id string, on bool) map[string]any { return map[string]any{"id": id, "enabled": on} }

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
	r := save(boss, map[string]any{"providers": list(pr("tads", true), pr("adsgram", true)), "fields": map[string]any{settings.KeyAdsTadsWidget: "12941"}})
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
	if ids := providerIDs(t, pv); !eq(ids, []string{"adsgram", "tads"}) {
		t.Fatalf("admin view: %v", pv)
	}
}
