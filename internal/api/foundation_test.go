package api_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"brunoplay/internal/admin"
	"brunoplay/internal/httpx"
	"brunoplay/internal/ratelimit"
	"brunoplay/internal/settings"
	"brunoplay/internal/tgauth"
)

func TestHealth(t *testing.T) {
	e := newEnv(t)
	if r := e.do("GET", "/healthz", "", nil); r.Status != 200 {
		t.Fatal(r.Status)
	}
	if r := e.do("GET", "/readyz", "", nil); r.Status != 200 {
		t.Fatal(r.Status)
	}
}

func TestSignInCreatesPlayerOnce(t *testing.T) {
	e := newEnv(t)
	r := e.do("POST", "/api/auth/telegram", "", map[string]any{"initData": e.initData(1001, "ru", "")})
	if r.Status != 200 {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	m := r.JSON(t)
	if m["isNew"] != true {
		t.Fatal("first sign-in must be new")
	}
	u := m["user"].(map[string]any)
	if u["language"] != "RU" || u["coins"].(float64) != 0 || u["starsBalance"].(float64) != 0 || u["level"].(float64) != 1 {
		t.Fatalf("new player must start with nothing, Russian language from Telegram: %v", u)
	}
	r2 := e.do("POST", "/api/auth/telegram", "", map[string]any{"initData": e.initData(1001, "en", "")})
	m2 := r2.JSON(t)
	if m2["isNew"] != false {
		t.Fatal("second sign-in must not be new")
	}
	if m2["user"].(map[string]any)["language"] != "RU" {
		t.Fatal("the saved language must not be overwritten by Telegram's")
	}
	var n int
	_ = e.d.Pool.QueryRow(context.Background(), `SELECT count(*) FROM players`).Scan(&n)
	if n != 1 {
		t.Fatalf("players %d", n)
	}
}

func TestSignInRejectsBadData(t *testing.T) {
	e := newEnv(t)
	bad := tgauth.Sign("999:WRONG", map[string]string{"user": `{"id":5}`, "auth_date": "1"})
	for name, body := range map[string]any{
		"tampered": map[string]any{"initData": bad},
		"empty":    map[string]any{"initData": ""},
		"no body":  nil,
		"unknown":  map[string]any{"initData": "x", "evil": 1},
	} {
		r := e.do("POST", "/api/auth/telegram", "", body)
		if r.Status < 400 || r.Status >= 500 {
			t.Errorf("%s: want a 4xx, got %d %s", name, r.Status, r.Body)
		}
	}
	old := tgauth.Sign(botToken, map[string]string{"user": `{"id":5,"first_name":"x"}`, "auth_date": "1000000000"})
	r := e.do("POST", "/api/auth/telegram", "", map[string]any{"initData": old})
	if r.Code(t) != "AUTH_EXPIRED" {
		t.Fatalf("old initData: %s", r.Body)
	}
}

func TestMeNeedsTokenAndExpires(t *testing.T) {
	e := newEnv(t)
	if r := e.do("GET", "/api/me", "", nil); r.Status != 401 {
		t.Fatal(r.Status)
	}
	if r := e.do("GET", "/api/me", "garbage", nil); r.Status != 401 {
		t.Fatal(r.Status)
	}
	p := e.login(2001, "en", "")
	if r := e.do("GET", "/api/me", p.Token, nil); r.Status != 200 {
		t.Fatal(r.Status)
	}
	e.d.PlayerTokenTTL = 12 * time.Hour
	p = e.login(2001, "en", "")
	e.now = e.now.Add(13 * time.Hour)
	if r := e.do("GET", "/api/me", p.Token, nil); r.Code(t) != "AUTH_EXPIRED" {
		t.Fatalf("token must expire after 12h: %s", r.Body)
	}
}

func TestReferralAttributionAndSnapshot(t *testing.T) {
	e := newEnv(t)
	inviter := e.login(3001, "en", "")
	e.setSetting(settings.KeyRefJoinBonus, 50)

	f1 := e.login(3002, "en", "ref_"+itoa(inviter.ID))
	var ref *int64
	var bonus int64
	_ = e.d.Pool.QueryRow(context.Background(), `SELECT referred_by, friend_bonus_coins FROM players WHERE id=$1`, f1.ID).Scan(&ref, &bonus)
	if ref == nil || *ref != inviter.ID || bonus != 50 {
		t.Fatalf("friend must be attributed with the default bonus snapshot, got %v %d", ref, bonus)
	}

	// per-inviter override applies to later friends only
	e.exec(`UPDATE players SET invite_bonus_coins=100 WHERE id=$1`, inviter.ID)
	e.setSetting(settings.KeyRefJoinBonus, 5)
	f2 := e.login(3003, "en", "ref_"+itoa(inviter.ID))
	_ = e.d.Pool.QueryRow(context.Background(), `SELECT friend_bonus_coins FROM players WHERE id=$1`, f2.ID).Scan(&bonus)
	if bonus != 100 {
		t.Fatalf("override bonus expected 100, got %d", bonus)
	}
	_ = e.d.Pool.QueryRow(context.Background(), `SELECT friend_bonus_coins FROM players WHERE id=$1`, f1.ID).Scan(&bonus)
	if bonus != 50 {
		t.Fatalf("earlier friend keeps the snapshot (50), got %d", bonus)
	}

	// an existing player can never be attributed later, nor yourself, nor a missing inviter
	existing := e.login(3004, "en", "")
	e.login(3004, "en", "ref_"+itoa(inviter.ID))
	_ = e.d.Pool.QueryRow(context.Background(), `SELECT referred_by IS NOT NULL FROM players WHERE id=$1`, existing.ID).Scan(new(bool))
	var has bool
	_ = e.d.Pool.QueryRow(context.Background(), `SELECT referred_by IS NOT NULL FROM players WHERE id=$1`, existing.ID).Scan(&has)
	if has {
		t.Fatal("an existing player must not be attributed on a later sign-in")
	}
	self := e.login(3005, "en", "ref_999999")
	_ = e.d.Pool.QueryRow(context.Background(), `SELECT referred_by IS NOT NULL FROM players WHERE id=$1`, self.ID).Scan(&has)
	if has {
		t.Fatal("an unknown inviter must be ignored")
	}
}

func TestConcurrentFirstSignInMakesOnePlayer(t *testing.T) {
	e := newEnv(t)
	var wg sync.WaitGroup
	var ok int64
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := e.do("POST", "/api/auth/telegram", "", map[string]any{"initData": e.initData(4001, "en", "")})
			if r.Status == 200 {
				atomic.AddInt64(&ok, 1)
			}
		}()
	}
	wg.Wait()
	var n int
	_ = e.d.Pool.QueryRow(context.Background(), `SELECT count(*) FROM players WHERE telegram_id=4001`).Scan(&n)
	if n != 1 || ok != 12 {
		t.Fatalf("players=%d ok=%d, want 1 player and 12 successful sign-ins", n, ok)
	}
}

func TestBlockedPlayerSeesBlockedScreenOnly(t *testing.T) {
	e := newEnv(t)
	p := e.login(5001, "ru", "")
	adm := e.adminLogin("support@example.com", admin.RoleSupport)
	r := e.do("POST", "/api/admin/players/"+itoa(p.ID)+"/block", adm, map[string]any{"reason": "cheating"})
	if r.Status != 200 {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	me := e.do("GET", "/api/me", p.Token, nil)
	if me.Status != 200 || me.JSON(t)["user"].(map[string]any)["isBlocked"] != true {
		t.Fatalf("blocked player still reads /me: %d %s", me.Status, me.Body)
	}
	lang := e.do("PATCH", "/api/me/language", p.Token, map[string]any{"language": "EN"})
	if lang.Status != 403 || lang.Code(t) != "ACCOUNT_BLOCKED" {
		t.Fatalf("game actions must be refused: %d %s", lang.Status, lang.Body)
	}
	if m := lang.JSON(t)["error"].(map[string]any)["message"]; m != "Ваш аккаунт заблокирован" {
		t.Fatalf("message must be in the player's language, got %v", m)
	}
	if r := e.do("POST", "/api/admin/players/"+itoa(p.ID)+"/unblock", adm, nil); r.Status != 200 {
		t.Fatal(r.Status)
	}
	if r := e.do("PATCH", "/api/me/language", p.Token, map[string]any{"language": "EN"}); r.Status != 200 {
		t.Fatalf("unblocked player works again: %d %s", r.Status, r.Body)
	}
}

func TestMaintenanceAndVersionGates(t *testing.T) {
	e := newEnv(t)
	p := e.login(6001, "ru", "")

	e.setSetting(settings.KeyMaintenanceOn, true)
	e.setSetting(settings.KeyMaintenanceRU, "Скоро вернёмся")
	r := e.do("GET", "/api/me", p.Token, nil, "Accept-Language", "ru")
	if r.Status != 503 || r.Code(t) != "MAINTENANCE" {
		t.Fatalf("maintenance: %d %s", r.Status, r.Body)
	}
	if params := r.JSON(t)["error"].(map[string]any)["params"].(map[string]any); params["text"] != "Скоро вернёмся" {
		t.Fatalf("maintenance text: %v", params)
	}
	// app-info stays reachable so the app can show the screen, and admin keeps working
	if r := e.do("GET", "/api/app-info", "", nil); r.Status != 200 || r.JSON(t)["maintenance"].(map[string]any)["enabled"] != true {
		t.Fatalf("app-info during maintenance: %d %s", r.Status, r.Body)
	}
	if r := e.do("GET", "/api/admin/settings", e.adminLogin("root@example.com", admin.RoleSuper), nil); r.Status != 200 {
		t.Fatalf("admin must work during maintenance: %d", r.Status)
	}
	e.setSetting(settings.KeyMaintenanceOn, false)

	e.setSetting(settings.KeyMinVersion, "1.4.0")
	if r := e.do("GET", "/api/me", p.Token, nil, "X-App-Version", "1.3.9"); r.Status != 426 || r.Code(t) != "UPGRADE_REQUIRED" {
		t.Fatalf("old version: %d %s", r.Status, r.Body)
	}
	if r := e.do("GET", "/api/me", p.Token, nil, "X-App-Version", "1.10.0"); r.Status != 200 {
		t.Fatalf("1.10.0 >= 1.4.0: %d %s", r.Status, r.Body)
	}
	if r := e.do("GET", "/api/me", p.Token, nil); r.Status != 200 {
		t.Fatalf("no version header must pass: %d", r.Status)
	}
}

func TestRateLimits(t *testing.T) {
	e := newEnv(t)
	e.d.MW.IPLimiter = ratelimit.NewLocal(0.0001, 3)
	first := 0
	for i := 0; i < 6; i++ {
		if r := e.do("GET", "/api/app-info", "", nil); r.Status == 200 {
			first++
		} else if r.Code(t) != "RATE_LIMITED" || r.Header.Get("Retry-After") == "" {
			t.Fatalf("limited response wrong: %d %s", r.Status, r.Body)
		}
	}
	if first != 3 {
		t.Fatalf("burst of 3 expected, got %d", first)
	}
}

func TestAdminPermissionsAndSettings(t *testing.T) {
	e := newEnv(t)
	finance := e.adminLogin("fin@example.com", admin.RoleFinance)
	support := e.adminLogin("sup@example.com", admin.RoleSupport)
	super := e.adminLogin("root@example.com", admin.RoleSuper)

	if r := e.do("GET", "/api/admin/settings", "", nil); r.Status != 401 {
		t.Fatal(r.Status)
	}
	// finance may change withdrawal settings, not game settings
	if r := e.do("PUT", "/api/admin/settings/"+settings.KeyWdMin, finance, map[string]any{"value": 10000}); r.Status != 200 {
		t.Fatalf("finance sets withdrawal.min: %d %s", r.Status, r.Body)
	}
	if r := e.do("PUT", "/api/admin/settings/"+settings.KeyChestPicks, finance, map[string]any{"value": 4}); r.Status != 403 {
		t.Fatalf("finance must not set games.*: %d", r.Status)
	}
	if r := e.do("PUT", "/api/admin/settings/"+settings.KeyWdMin, support, map[string]any{"value": 1}); r.Status != 403 {
		t.Fatalf("support must not write settings: %d", r.Status)
	}
	if r := e.do("PUT", "/api/admin/settings/"+settings.KeyWdMin, super, map[string]any{"value": -1}); r.Status != 422 {
		t.Fatalf("invalid value must be 422: %d %s", r.Status, r.Body)
	}
	if v, _ := e.d.Settings.Int(context.Background(), settings.KeyWdMin); v != 10000 {
		t.Fatalf("value %d", v)
	}
	// every change is in the audit log
	r := e.do("GET", "/api/admin/audit", super, nil)
	rows := r.JSON(t)["audit"].([]any)
	found := false
	for _, x := range rows {
		if x.(map[string]any)["action"] == "settings.set" {
			found = true
		}
	}
	if !found {
		t.Fatalf("audit must contain settings.set: %s", r.Body)
	}
	// only a super admin manages admins
	if r := e.do("GET", "/api/admin/admins", finance, nil); r.Status != 403 {
		t.Fatal(r.Status)
	}
	if r := e.do("GET", "/api/admin/admins", super, nil); r.Status != 200 {
		t.Fatal(r.Status)
	}
	// "what is still empty" view
	m := e.do("GET", "/api/admin/settings/missing", super, nil).JSON(t)["missing"].([]any)
	has := func(k string) bool {
		for _, x := range m {
			if x == k {
				return true
			}
		}
		return false
	}
	if has(settings.KeyWdMin) || !has(settings.KeyWdMax) {
		t.Fatalf("missing list wrong: %v", m)
	}
}

func TestAdminLoginLockoutOverHTTP(t *testing.T) {
	e := newEnv(t)
	_, _ = e.d.Admin.Create(context.Background(), admin.CreateInput{Email: "a@example.com", Password: adminPW, Role: admin.RoleSuper})
	for i := 0; i < 5; i++ {
		if r := e.do("POST", "/api/admin/auth/login", "", map[string]any{"email": "a@example.com", "password": "nope"}); r.Status != 401 {
			t.Fatal(r.Status)
		}
	}
	if r := e.do("POST", "/api/admin/auth/login", "", map[string]any{"email": "a@example.com", "password": adminPW}); r.Status != 401 {
		t.Fatalf("locked: %d", r.Status)
	}
}

func TestAdminPlayerSearchAndInviteBonus(t *testing.T) {
	e := newEnv(t)
	a := e.login(7001, "en", "")
	e.login(7002, "en", "")
	super := e.adminLogin("root@example.com", admin.RoleSuper)
	r := e.do("GET", "/api/admin/players?q=user7001", super, nil)
	m := r.JSON(t)
	if m["total"].(float64) != 1 || len(m["players"].([]any)) != 1 {
		t.Fatalf("search: %s", r.Body)
	}
	if r := e.do("PUT", "/api/admin/players/"+itoa(a.ID)+"/invite-bonus", super, map[string]any{"coins": 100}); r.Status != 200 {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	var b *int64
	_ = e.d.Pool.QueryRow(context.Background(), `SELECT invite_bonus_coins FROM players WHERE id=$1`, a.ID).Scan(&b)
	if b == nil || *b != 100 {
		t.Fatalf("invite bonus %v", b)
	}
	if r := e.do("PUT", "/api/admin/players/"+itoa(a.ID)+"/invite-bonus", super, map[string]any{"coins": nil}); r.Status != 200 {
		t.Fatal(r.Status)
	}
	_ = e.d.Pool.QueryRow(context.Background(), `SELECT invite_bonus_coins FROM players WHERE id=$1`, a.ID).Scan(&b)
	if b != nil {
		t.Fatal("null must reset to the default")
	}
}

func TestIdempotencyMiddlewareReplays(t *testing.T) {
	e := newEnv(t)
	p := e.login(8001, "en", "")
	var calls int64
	mux := http.NewServeMux()
	mux.Handle("/pay", e.d.MW.PlayerAuth(false)(e.d.MW.Idempotency(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt64(&calls, 1)
		time.Sleep(50 * time.Millisecond)
		httpx.JSON(w, 200, map[string]int64{"call": n})
	}))))
	srv := httptest.NewServer(e.d.MW.Base(mux))
	defer srv.Close()
	post := func(key string) (int, string, string) {
		req, _ := http.NewRequest("POST", srv.URL+"/pay", nil)
		req.Header.Set("Authorization", "Bearer "+p.Token)
		if key != "" {
			req.Header.Set("Idempotency-Key", key)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(b), res.Header.Get("Idempotent-Replay")
	}
	s1, b1, _ := post("k1")
	s2, b2, replay := post("k1")
	if s1 != 200 || s2 != 200 || b1 != b2 || replay != "true" || calls != 1 {
		t.Fatalf("replay failed: %d %q / %d %q replay=%q calls=%d", s1, b1, s2, b2, replay, calls)
	}
	if _, b3, _ := post("k2"); b3 == b1 || calls != 2 {
		t.Fatalf("a new key runs again: %q calls=%d", b3, calls)
	}
	// a request without a key is never deduplicated
	post("")
	post("")
	if calls != 4 {
		t.Fatalf("calls=%d want 4", calls)
	}
	// simultaneous identical requests: the handler runs once, the other gets IN_PROGRESS or the replay
	atomic.StoreInt64(&calls, 0)
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); post("race") }()
	}
	wg.Wait()
	if calls != 1 {
		t.Fatalf("the handler ran %d times for one key", calls)
	}
}

func itoa(i int64) string { return strconv.FormatInt(i, 10) }
