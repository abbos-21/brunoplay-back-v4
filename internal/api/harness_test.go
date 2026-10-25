package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"brunoplay/internal/admin"
	"brunoplay/internal/api"
	"brunoplay/internal/config"
	"brunoplay/internal/ratelimit"
	"brunoplay/internal/telegram"
	"brunoplay/internal/testutil"
	"brunoplay/internal/tgauth"
)

const (
	botToken = "123456:TEST-BOT-TOKEN"
	adminPW  = "Str0ng-Passw0rd!"
)

// env is a complete app on a clean test database with a controllable clock.
type env struct {
	t    *testing.T
	d    *api.Deps
	srv  *httptest.Server
	now  time.Time
	http *http.Client
}

func init() { slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil))) }

func newEnv(t *testing.T) *env {
	t.Helper()
	pool := testutil.DB(t)
	cfg := config.Config{
		Env: "test", JWTSecret: "player-secret-player-secret-player-secret", AdminJWTSecr: "admin-secret-admin-secret-admin-secret-0",
		BotToken: botToken, BotUsername: "ImTomsBot", InitDataMaxAge: 24 * time.Hour, TrustedProxy: true, DevAuth: true,
	}
	// the ledger stamps rows with the database clock, so the fake clock must be on the same UTC day
	e := &env{t: t, now: time.Now().UTC().Truncate(24 * time.Hour).Add(12 * time.Hour), http: &http.Client{Timeout: 20 * time.Second}}
	e.d = api.NewDeps(cfg, pool)
	e.d.Now = func() time.Time { return e.now }
	e.d.Settings.SetTTL(0)
	e.d.PlayerTokenTTL = 100000 * time.Hour
	e.d.Admin.SetClock(func() time.Time { return e.now })
	e.d.MW.Now = func() time.Time { return e.now }
	// tests make many calls: do not let the limiters interfere unless a test sets its own
	e.d.MW.IPLimiter = ratelimit.NewLocal(100000, 100000)
	e.d.MW.PlayerLimit = ratelimit.NewLocal(100000, 100000)
	e.d.LoginLimit = ratelimit.NewLocal(100000, 100000)
	e.d.MoneyLimit = ratelimit.NewLocal(100000, 100000)
	e.exec(`UPDATE combo_images SET active=TRUE`) // the pictures are not wiped between tests
	e.srv = httptest.NewServer(e.d.Router())
	t.Cleanup(e.srv.Close)
	return e
}

type resp struct {
	Status int
	Body   []byte
	Header http.Header
}

func (r resp) JSON(t testing.TB) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(r.Body, &m); err != nil {
		t.Fatalf("bad json %q: %v", r.Body, err)
	}
	return m
}
func (r resp) Code(t testing.TB) string {
	t.Helper()
	m := r.JSON(t)
	e, _ := m["error"].(map[string]any)
	c, _ := e["code"].(string)
	return c
}

func (e *env) do(method, path, token string, body any, hdr ...string) resp {
	e.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, e.srv.URL+path, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	res, err := e.http.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return resp{Status: res.StatusCode, Body: b, Header: res.Header}
}

// initData builds a correctly signed Telegram initData for a user.
func (e *env) initData(tgID int64, lang, startParam string) string {
	f := map[string]string{
		"user":      `{"id":` + strconv.FormatInt(tgID, 10) + `,"first_name":"P` + strconv.FormatInt(tgID, 10) + `","username":"user` + strconv.FormatInt(tgID, 10) + `","language_code":"` + lang + `"}`,
		"auth_date": strconv.FormatInt(e.now.Unix(), 10),
	}
	if startParam != "" {
		f["start_param"] = startParam
	}
	return tgauth.Sign(botToken, f)
}

type player struct {
	ID    int64
	Token string
	TG    int64
}

// login signs a player in through the real endpoint and returns the token.
func (e *env) login(tgID int64, lang, startParam string) player {
	e.t.Helper()
	r := e.do("POST", "/api/auth/telegram", "", map[string]any{"initData": e.initData(tgID, lang, startParam)})
	if r.Status != 200 {
		e.t.Fatalf("login failed: %d %s", r.Status, r.Body)
	}
	m := r.JSON(e.t)
	u := m["user"].(map[string]any)
	return player{ID: int64(u["id"].(float64)), Token: m["token"].(string), TG: tgID}
}

// adminLogin creates an admin with the role and returns its access token.
func (e *env) adminLogin(email, role string) string {
	e.t.Helper()
	ctx := context.Background()
	if _, err := e.d.Admin.Create(ctx, admin.CreateInput{Email: email, Password: adminPW, Role: role}); err != nil {
		e.t.Fatal(err)
	}
	r := e.do("POST", "/api/admin/auth/login", "", map[string]any{"email": email, "password": adminPW})
	if r.Status != 200 {
		e.t.Fatalf("admin login: %d %s", r.Status, r.Body)
	}
	return r.JSON(e.t)["accessToken"].(string)
}

// give credits a balance directly (tests only) through the ledger.
func (e *env) exec(sql string, args ...any) {
	e.t.Helper()
	if _, err := e.d.Pool.Exec(context.Background(), sql, args...); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) setSetting(key string, v any) {
	e.t.Helper()
	if err := e.d.Settings.Set(context.Background(), key, v, 0); err != nil {
		e.t.Fatalf("set %s: %v", key, err)
	}
}

// adminRelogin signs an existing admin in again (after a clock jump made the old token expire).
func (e *env) adminRelogin(email string) string {
	e.t.Helper()
	r := e.do("POST", "/api/admin/auth/login", "", map[string]any{"email": email, "password": adminPW})
	if r.Status != 200 {
		e.t.Fatalf("admin relogin: %d %s", r.Status, r.Body)
	}
	return r.JSON(e.t)["accessToken"].(string)
}

// telegramPaid builds the update Telegram sends after a successful Stars payment.
func telegramPaid(userID int64, orderID string, amount int64) telegram.Update {
	return telegram.Update{Message: &telegram.Message{From: &telegram.User{ID: userID}, SuccessfulPayment: &telegram.SuccessfulPayment{
		Currency: "XTR", TotalAmount: amount, InvoicePayload: orderID, TelegramPaymentChargeID: "chg-" + orderID}}}
}
