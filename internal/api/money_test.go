package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"brunoplay/internal/admin"
	"brunoplay/internal/ledger"
	"brunoplay/internal/settings"
	"brunoplay/internal/telegram"
	"brunoplay/internal/ton"
	"brunoplay/internal/withdrawals"
)

func walletAddr(seed byte) string {
	var a ton.Address
	for i := range a.Hash {
		a.Hash[i] = seed + byte(i)
	}
	return a.Friendly(true)
}

// moneyConfig sets the top-up settings: 1 TON = 100 stars, TON bonus 10 %, Telegram 1:1 with no bonus.
func (e *env) moneyConfig() (*telegram.FakeAPI, *ton.FakeChain) {
	fake := telegram.NewFake()
	chain := &ton.FakeChain{}
	e.d.TG, e.d.Chain = fake, chain
	e.d.Topup.SetAPIs(fake, chain)
	e.d.Cfg.BotWebhookSec = "whsecret"
	e.setSetting(settings.KeyTopupPacks, []map[string]any{{"stars": 200}, {"stars": 500}, {"stars": 1000}})
	e.setSetting(settings.KeyTopupStarsPerTon, 100)
	e.setSetting(settings.KeyTopupTonBonus, 10)
	e.setSetting(settings.KeyTopupTonWallet, walletAddr(1))
	e.setSetting(settings.KeyTopupTonConf, 0)
	return fake, chain
}

func (e *env) tgUpdate(u telegram.Update) resp {
	b, _ := json.Marshal(u)
	req, _ := http.NewRequest("POST", e.srv.URL+"/api/webhooks/telegram", strings.NewReader(string(b)))
	req.Header.Set("X-Telegram-Bot-Api-Secret-Token", "whsecret")
	req.Header.Set("Content-Type", "application/json")
	res, err := e.http.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer res.Body.Close()
	var buf strings.Builder
	_, _ = fmt.Fprint(&buf, "")
	body := make([]byte, 4096)
	n, _ := res.Body.Read(body)
	return resp{Status: res.StatusCode, Body: body[:n]}
}

func TestTopupPacksNeedConfig(t *testing.T) {
	e := newEnv(t)
	p := e.login(200, "en", "")
	if r := e.do("GET", "/api/topup/packs", p.Token, nil); r.Code(t) != "CONFIG_MISSING" {
		t.Fatalf("%s", r.Body)
	}
	e.moneyConfig()
	m := e.do("GET", "/api/topup/packs", p.Token, nil).JSON(t)
	packs := m["packs"].([]any)
	if len(packs) != 3 || m["tonAvailable"] != true || m["tonBonusPercent"].(float64) != 10 {
		t.Fatalf("%v", m)
	}
	p0 := packs[0].(map[string]any)
	if p0["stars"].(float64) != 200 || p0["telegramStars"].(float64) != 200 || p0["tonNano"].(float64) != 2e9 || p0["ton"] != "2" || p0["tonBonusStars"].(float64) != 20 {
		t.Fatalf("pack: %v", p0)
	}
	// the admin changes the TON bonus: the panel and the real bonus follow at once
	e.setSetting(settings.KeyTopupTonBonus, 20)
	m = e.do("GET", "/api/topup/packs", p.Token, nil).JSON(t)
	if m["tonBonusPercent"].(float64) != 20 || m["packs"].([]any)[0].(map[string]any)["tonBonusStars"].(float64) != 40 {
		t.Fatalf("%v", m)
	}
	if info := e.do("GET", "/api/app-info", "", nil).JSON(t); info["topupTonBonusPercent"].(float64) != 20 {
		t.Fatalf("app-info bonus: %v", info)
	}
}

func TestTelegramStarsPayment(t *testing.T) {
	e := newEnv(t)
	fake, _ := e.moneyConfig()
	p := e.login(201, "ru", "")

	if r := e.do("POST", "/api/topup/telegram-invoice", p.Token, map[string]any{"packStars": 777}); r.Code(t) != "TOPUP_PACK_INVALID" {
		t.Fatalf("a pack that is not offered: %s", r.Body)
	}
	r := e.do("POST", "/api/topup/telegram-invoice", p.Token, map[string]any{"packStars": 500})
	if r.Status != 200 {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	o := r.JSON(t)
	orderID := o["id"].(string)
	if len(fake.Invoices) != 1 || fake.Invoices[0].Payload != orderID || fake.Invoices[0].Stars != 500 || !strings.HasPrefix(o["invoiceLink"].(string), "https://t.me/") {
		t.Fatalf("invoice: %+v", fake.Invoices)
	}
	if fake.Invoices[0].Title != "500 звёзд" {
		t.Fatalf("Russian title expected, got %q", fake.Invoices[0].Title)
	}

	// without the secret header Telegram's webhook is refused
	req, _ := http.NewRequest("POST", e.srv.URL+"/api/webhooks/telegram", strings.NewReader(`{}`))
	if res, _ := e.http.Do(req); res.StatusCode != 401 {
		t.Fatalf("webhook without secret: %d", res.StatusCode)
	}

	pre := func(from int64, amount int64, payload string) bool {
		id := fmt.Sprintf("q%d%d", from, amount)
		e.tgUpdate(telegram.Update{PreCheckoutQuery: &telegram.PreCheckoutQuery{ID: id, From: telegram.User{ID: from}, Currency: "XTR", TotalAmount: amount, InvoicePayload: payload}})
		return fake.PreCheck[id]
	}
	if pre(201, 499, orderID) || pre(999, 500, orderID) || pre(201, 500, "nope") {
		t.Fatal("a wrong amount, a wrong user or an unknown order must be refused at pre-checkout")
	}
	if !pre(201, 500, orderID) {
		t.Fatal("the right payment must pass")
	}

	pay := telegram.Update{Message: &telegram.Message{From: &telegram.User{ID: 201}, SuccessfulPayment: &telegram.SuccessfulPayment{
		Currency: "XTR", TotalAmount: 500, InvoicePayload: orderID, TelegramPaymentChargeID: "chg-1"}}}
	if r := e.tgUpdate(pay); r.Status != 200 {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	if e.stars(p) != 500 {
		t.Fatalf("stars %d", e.stars(p))
	}
	// the same update delivered again (Telegram retries) must not pay twice
	for i := 0; i < 3; i++ {
		e.tgUpdate(pay)
	}
	if e.stars(p) != 500 {
		t.Fatalf("stars after retries %d", e.stars(p))
	}
	orders := e.do("GET", "/api/topup/orders", p.Token, nil).JSON(t)["orders"].([]any)
	if len(orders) != 1 || orders[0].(map[string]any)["status"] != "PAID" {
		t.Fatalf("%v", orders)
	}

	// a payment for an order that does not exist is not credited and is reported
	var alerts []string
	e.d.Topup.Alert = func(ctx context.Context, text string) { alerts = append(alerts, text) }
	e.tgUpdate(telegram.Update{Message: &telegram.Message{From: &telegram.User{ID: 201}, SuccessfulPayment: &telegram.SuccessfulPayment{
		Currency: "XTR", TotalAmount: 100, InvoicePayload: "ghost", TelegramPaymentChargeID: "chg-ghost"}}})
	if e.stars(p) != 500 || len(alerts) != 1 {
		t.Fatalf("ghost payment: stars=%d alerts=%v", e.stars(p), alerts)
	}

	// Telegram refunds the payment: the stars are taken back (below zero if already spent) and the account is held
	e.exec(`UPDATE players SET stars = stars - 450 WHERE id=$1`, p.ID) // the player spent most of them
	e.tgUpdate(telegram.Update{Message: &telegram.Message{From: &telegram.User{ID: 201}, RefundedPayment: &telegram.RefundedPayment{
		Currency: "XTR", TotalAmount: 500, InvoicePayload: orderID, TelegramPaymentChargeID: "chg-1"}}})
	if e.stars(p) != 50-500 {
		t.Fatalf("refund must take 500 stars back even below zero: %d", e.stars(p))
	}
	var held bool
	var status string
	_ = e.d.Pool.QueryRow(context.Background(), `SELECT hold_withdrawals FROM players WHERE id=$1`, p.ID).Scan(&held)
	_ = e.d.Pool.QueryRow(context.Background(), `SELECT status FROM topup_orders WHERE id=$1`, orderID).Scan(&status)
	if !held || status != "REFUNDED" {
		t.Fatalf("held=%v status=%s", held, status)
	}
	e.tgUpdate(telegram.Update{Message: &telegram.Message{From: &telegram.User{ID: 201}, RefundedPayment: &telegram.RefundedPayment{Currency: "XTR", TelegramPaymentChargeID: "chg-1"}}})
	if e.stars(p) != -450 {
		t.Fatalf("a second refund notice must not deduct again: %d", e.stars(p))
	}
}

func (e *env) tonOrder(p player, packStars int64) map[string]any {
	e.t.Helper()
	r := e.do("POST", "/api/topup/ton-order", p.Token, map[string]any{"packStars": packStars})
	if r.Status != 200 {
		e.t.Fatalf("ton order: %d %s", r.Status, r.Body)
	}
	return r.JSON(e.t)
}

func TestTonPaymentMatching(t *testing.T) {
	e := newEnv(t)
	_, chain := e.moneyConfig()
	p := e.login(210, "en", "")
	o := e.tonOrder(p, 500) // 5 TON for 500 stars, +10% bonus
	comment, price := o["comment"].(string), int64(o["amountNano"].(float64))
	if price != 5_000_000_000 || o["address"] != walletAddr(1) || !strings.HasPrefix(comment, "bp") || o["payload"] == "" {
		t.Fatalf("order: %v", o)
	}
	ctx := context.Background()

	if n, err := e.d.Topup.PollTon(ctx); err != nil || n != 0 {
		t.Fatalf("nothing on the chain yet: %d %v", n, err)
	}
	created := e.now
	e.now = created.Add(time.Minute) // the player pays, a minute passes
	// noise: wrong comment, no comment, a transfer from before the order existed
	chain.Txs = []ton.Tx{
		{Hash: "h-noise1", Value: price, Comment: "bpffffffffffffffffffff", Time: created.Add(time.Second)},
		{Hash: "h-noise2", Value: price, Comment: "", Time: created.Add(time.Second)},
		{Hash: "h-old", Value: price, Comment: comment, Time: created.Add(-time.Hour)},
	}
	if n, _ := e.d.Topup.PollTon(ctx); n != 0 || e.stars(p) != 0 {
		t.Fatalf("noise must not credit: n=%d stars=%d", n, e.stars(p))
	}
	// underpaid: not credited, the owner is told
	var alerts []string
	e.d.Topup.Alert = func(ctx context.Context, text string) { alerts = append(alerts, text) }
	chain.Txs = []ton.Tx{{Hash: "h-under", Value: price - 1, Comment: comment, Time: created.Add(2 * time.Second)}}
	if n, _ := e.d.Topup.PollTon(ctx); n != 0 || e.stars(p) != 0 || len(alerts) != 1 {
		t.Fatalf("underpaid: n=%d stars=%d alerts=%d", n, e.stars(p), len(alerts))
	}
	// the right amount arrives
	chain.Txs = append(chain.Txs, ton.Tx{Hash: "h-ok", Value: price + 7, Comment: comment, Time: created.Add(3 * time.Second)})
	if n, err := e.d.Topup.PollTon(ctx); err != nil || n != 1 {
		t.Fatalf("credit: %d %v", n, err)
	}
	if e.stars(p) != 550 {
		t.Fatalf("500 stars + 10%% bonus expected, got %d", e.stars(p))
	}
	// polling again, even from two workers at once, never pays twice
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, _ = e.d.Topup.PollTon(ctx) }()
	}
	wg.Wait()
	if e.stars(p) != 550 {
		t.Fatalf("stars %d", e.stars(p))
	}
	var rows int
	_ = e.d.Pool.QueryRow(ctx, `SELECT count(*) FROM ledger_entries WHERE player_id=$1 AND reason IN ('topup','topup_bonus')`, p.ID).Scan(&rows)
	if rows != 2 {
		t.Fatalf("ledger lines: %d (purchase + bonus)", rows)
	}
	st := e.do("GET", "/api/topup/orders/"+o["id"].(string), p.Token, nil).JSON(t)
	if st["status"] != "PAID" {
		t.Fatalf("%v", st)
	}
	// another player cannot read the order
	q := e.login(211, "en", "")
	if r := e.do("GET", "/api/topup/orders/"+o["id"].(string), q.Token, nil); r.Status != 404 {
		t.Fatalf("%d", r.Status)
	}
}

func TestTonLatePaymentAndAdminCredit(t *testing.T) {
	e := newEnv(t)
	_, chain := e.moneyConfig()
	p := e.login(212, "en", "")
	o := e.tonOrder(p, 200)
	comment, price := o["comment"].(string), int64(o["amountNano"].(float64))
	created := e.now
	// the order expires after 30 minutes; a transfer a day later is still honoured
	e.now = created.Add(25 * time.Hour)
	if err := e.d.Topup.ExpireOrders(context.Background()); err != nil {
		t.Fatal(err)
	}
	var status string
	_ = e.d.Pool.QueryRow(context.Background(), `SELECT status FROM topup_orders WHERE id=$1`, o["id"]).Scan(&status)
	if status != "EXPIRED" {
		t.Fatalf("status %s", status)
	}
	chain.Txs = []ton.Tx{{Hash: "h-late", Value: price, Comment: comment, Time: created.Add(24 * time.Hour)}}
	if n, _ := e.d.Topup.PollTon(context.Background()); n != 1 || e.stars(p) != 220 {
		t.Fatalf("late payment: n=%d stars=%d", n, e.stars(p))
	}
	// but not after more than a week
	o2 := e.tonOrder(p, 200)
	c2 := o2["comment"].(string)
	chain.Txs = []ton.Tx{{Hash: "h-very-late", Value: price, Comment: c2, Time: e.now.Add(9 * 24 * time.Hour)}}
	e.now = e.now.Add(9 * 24 * time.Hour)
	_ = e.d.Topup.ExpireOrders(context.Background())
	if n, _ := e.d.Topup.PollTon(context.Background()); n != 0 {
		t.Fatalf("too late must be left for a human: %d", n)
	}
	// the finance admin credits it by hand
	fin := e.adminLogin("fin@example.com", admin.RoleFinance)
	if r := e.do("POST", "/api/admin/topup/orders/"+o2["id"].(string)+"/credit", fin, map[string]any{"reason": "owner checked the wallet"}); r.Status != 200 {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	if e.stars(p) != 440 {
		t.Fatalf("stars %d", e.stars(p))
	}
	if r := e.do("POST", "/api/admin/topup/orders/"+o2["id"].(string)+"/credit", fin, map[string]any{"reason": "again"}); r.Status != 400 {
		t.Fatalf("crediting twice: %d", r.Status)
	}
	sup := e.adminLogin("sup@example.com", admin.RoleSupport)
	if r := e.do("POST", "/api/admin/topup/orders/"+o2["id"].(string)+"/credit", sup, map[string]any{"reason": "x"}); r.Status != 403 {
		t.Fatalf("support must not credit payments: %d", r.Status)
	}
}

// ---------------------------------------------------------------- withdrawals

func (e *env) wdConfig() {
	e.setSetting(settings.KeyWdCoinsPerTon, 100000)
	e.setSetting(settings.KeyWdFeeTon, 0.005)
	e.setSetting(settings.KeyWdMin, 10000)
	e.setSetting(settings.KeyWdMax, 5000000)
	e.setSetting(settings.KeyWdCooldownHours, 24)
	e.setSetting(settings.KeyWdMinLevel, 1)
	e.setSetting(settings.KeyWdMinAgeDays, 0)
	e.setSetting(settings.KeyWdDailyLimit, 1000000)
	e.setSetting(settings.KeyWdTotalLimit, 10000000)
}

func (e *env) wd(p player, coins int64, addr string) resp {
	return e.do("POST", "/api/withdrawals", p.Token, map[string]any{"targetAddress": addr, "amountCoins": coins})
}

func TestWithdrawalRules(t *testing.T) {
	e := newEnv(t)
	p := e.login(300, "ru", "")
	addr := walletAddr(9)
	e.give(p, ledger.Coin, 2000000)

	if r := e.wd(p, 10000, addr); r.Code(t) != "CONFIG_MISSING" {
		t.Fatalf("not configured: %s", r.Body)
	}
	e.wdConfig()

	info := e.do("GET", "/api/withdrawals/info", p.Token, nil).JSON(t)
	if info["canWithdraw"] != true || info["minimumCoinWithdrawal"].(float64) != 10000 || info["withdrawalFeeTon"] != "0.005" || info["coinsPerTon"].(float64) != 100000 {
		t.Fatalf("info: %v", info)
	}
	cases := []struct {
		coins int64
		addr  string
		code  string
	}{
		{10000, "nonsense", "TON_ADDRESS_INVALID"},
		{9999, addr, "WITHDRAWAL_BELOW_MIN"},
		{5000001, addr, "WITHDRAWAL_ABOVE_MAX"},
	}
	for _, c := range cases {
		if r := e.wd(p, c.coins, c.addr); r.Code(t) != c.code {
			t.Errorf("%d %s: got %s want %s", c.coins, c.addr, r.Body, c.code)
		}
	}
	r := e.wd(p, 9999, addr)
	if m := r.JSON(t)["error"].(map[string]any)["message"]; m != "Минимальный вывод — 10000 монет" {
		t.Fatalf("message %v", m)
	}
	if e.coins(p) != 2000000 {
		t.Fatalf("refused requests must not take coins: %d", e.coins(p))
	}

	// success: 10,000 coins = 0.1 TON, minus the 0.005 fee
	r = e.wd(p, 10000, addr)
	if r.Status != 200 {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	w := r.JSON(t)["withdrawal"].(map[string]any)
	if w["amountNano"].(float64) != 95_000_000 || w["amountTon"] != "0.095" || w["status"] != "PENDING" || e.coins(p) != 1990000 {
		t.Fatalf("withdrawal: %v coins=%d", w, e.coins(p))
	}
	id := int64(w["id"].(float64))
	// one open request at a time
	if r := e.wd(p, 10000, addr); r.Code(t) != "WITHDRAWAL_ALREADY_OPEN" {
		t.Fatalf("%s", r.Body)
	}
	// changing the rate later does not change the request that exists
	e.setSetting(settings.KeyWdCoinsPerTon, 50000)
	list := e.do("GET", "/api/withdrawals", p.Token, nil).JSON(t)
	if list["total"].(float64) != 1 || list["data"].([]any)[0].(map[string]any)["amountNano"].(float64) != 95_000_000 {
		t.Fatalf("%v", list)
	}
	e.setSetting(settings.KeyWdCoinsPerTon, 100000)

	// cancel returns the coins; a cancelled request does not count for the cooldown
	c := e.do("POST", fmt.Sprintf("/api/withdrawals/%d/cancel", id), p.Token, nil)
	if c.Status != 200 || e.coins(p) != 2000000 {
		t.Fatalf("cancel: %d %s coins=%d", c.Status, c.Body, e.coins(p))
	}
	if r := e.do("POST", fmt.Sprintf("/api/withdrawals/%d/cancel", id), p.Token, nil); r.Code(t) != "WITHDRAWAL_NOT_CANCELLABLE" {
		t.Fatalf("cancelling twice: %s", r.Body)
	}
	if e.coins(p) != 2000000 {
		t.Fatal("the refund must be paid once")
	}
	if r := e.wd(p, 10000, addr); r.Status != 200 {
		t.Fatalf("new request after a cancel: %d %s", r.Status, r.Body)
	}
}

func TestWithdrawalCooldownLimitsLevelAge(t *testing.T) {
	e := newEnv(t)
	e.wdConfig()
	fin := e.adminLogin("fin@example.com", admin.RoleFinance)
	p := e.login(301, "en", "")
	e.give(p, ledger.Coin, 5000000)
	addr := walletAddr(9)

	r := e.wd(p, 400000, addr)
	id := int(r.JSON(t)["withdrawal"].(map[string]any)["id"].(float64))
	e.do("POST", fmt.Sprintf("/api/admin/withdrawals/%d/complete", id), fin, map[string]any{"txHash": "abc"})
	if r := e.wd(p, 10000, addr); r.Code(t) != "WITHDRAWAL_COOLDOWN" {
		t.Fatalf("cooldown: %s", r.Body)
	}
	info := e.do("GET", "/api/withdrawals/info", p.Token, nil).JSON(t)
	if info["canWithdraw"] != false || info["blockedReason"] != "WITHDRAWAL_COOLDOWN" || info["nextWithdrawalAt"] == nil {
		t.Fatalf("info during cooldown: %v", info)
	}
	e.now = e.now.Add(25 * time.Hour) // a new game day and the cooldown is over
	e.d.PlayerTokenTTL = 100000 * time.Hour
	fin = e.adminRelogin("fin@example.com") // the old admin token expired with the clock jump
	r = e.wd(p, 700000, addr)
	if r.Status != 200 {
		t.Fatalf("700k on the new day: %d %s", r.Status, r.Body)
	}
	e.do("POST", fmt.Sprintf("/api/admin/withdrawals/%d/complete", int(r.JSON(t)["withdrawal"].(map[string]any)["id"].(float64))), fin, map[string]any{"txHash": "def"})
	e.setSetting(settings.KeyWdCooldownHours, 0)
	// the daily limit is 1,000,000 coins: 700k are already taken today
	if r := e.wd(p, 400000, addr); r.Code(t) != "WITHDRAWAL_LIMIT" {
		t.Fatalf("daily limit: %s", r.Body)
	}
	if r := e.wd(p, 300000, addr); r.Status != 200 {
		t.Fatalf("300k fits: %d %s", r.Status, r.Body)
	}
}

func TestWithdrawalTotalLimitLevelAgeAndHolds(t *testing.T) {
	e := newEnv(t)
	e.wdConfig()
	addr := walletAddr(9)

	lvl := e.login(302, "en", "")
	e.give(lvl, ledger.Coin, 100000)
	e.setSetting(settings.KeyWdMinLevel, 3)
	if r := e.wd(lvl, 10000, addr); r.Code(t) != "WITHDRAWAL_LEVEL_TOO_LOW" {
		t.Fatalf("%s", r.Body)
	}
	e.exec(`UPDATE players SET level=3 WHERE id=$1`, lvl.ID)
	if r := e.wd(lvl, 10000, addr); r.Status != 200 {
		t.Fatalf("%d %s", r.Status, r.Body)
	}

	e.setSetting(settings.KeyWdMinLevel, 1)
	young := e.login(303, "en", "")
	e.give(young, ledger.Coin, 100000)
	e.setSetting(settings.KeyWdMinAgeDays, 7)
	if r := e.wd(young, 10000, addr); r.Code(t) != "WITHDRAWAL_ACCOUNT_TOO_NEW" {
		t.Fatalf("%s", r.Body)
	}
	e.now = e.now.Add(8 * 24 * time.Hour)
	e.d.PlayerTokenTTL = 1000000 * time.Hour
	if r := e.wd(young, 10000, addr); r.Status != 200 {
		t.Fatalf("old enough: %d %s", r.Status, r.Body)
	}

	held := e.login(304, "en", "")
	e.give(held, ledger.Coin, 100000)
	e.exec(`UPDATE players SET hold_withdrawals=TRUE WHERE id=$1`, held.ID)
	if r := e.wd(held, 10000, addr); r.Code(t) != "UNDER_REVIEW" {
		t.Fatalf("held account: %s", r.Body)
	}

	// too small to cover the fee
	e.setSetting(settings.KeyWdMin, 1)
	small := e.login(305, "en", "")
	e.give(small, ledger.Coin, 1000)
	if r := e.wd(small, 400, addr); r.Code(t) != "WITHDRAWAL_TOO_SMALL" { // 400 coins = 0.004 TON < 0.005 fee
		t.Fatalf("%s", r.Body)
	}

	// lifetime limit
	e.setSetting(settings.KeyWdTotalLimit, 15000)
	e.setSetting(settings.KeyWdCooldownHours, 0)
	e.setSetting(settings.KeyWdMinAgeDays, 0)
	tot := e.login(306, "en", "")
	e.give(tot, ledger.Coin, 100000)
	fin := e.adminLogin("fin@example.com", admin.RoleFinance)
	r := e.wd(tot, 10000, addr)
	e.do("POST", fmt.Sprintf("/api/admin/withdrawals/%d/complete", int(r.JSON(t)["withdrawal"].(map[string]any)["id"].(float64))), fin, map[string]any{"txHash": "x"})
	if r := e.wd(tot, 6000, addr); r.Code(t) != "WITHDRAWAL_LIMIT" {
		t.Fatalf("total limit: %s", r.Body)
	}
}

func TestConcurrentWithdrawalsTakeCoinsOnce(t *testing.T) {
	e := newEnv(t)
	e.wdConfig()
	p := e.login(310, "en", "")
	e.give(p, ledger.Coin, 1000000)
	var ok int64
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if e.wd(p, 20000, walletAddr(9)).Status == 200 {
				atomic.AddInt64(&ok, 1)
			}
		}()
	}
	wg.Wait()
	if ok != 1 || e.coins(p) != 980000 {
		t.Fatalf("ok=%d coins=%d", ok, e.coins(p))
	}
}

func TestAdminWithdrawalFlow(t *testing.T) {
	e := newEnv(t)
	e.wdConfig()
	fin := e.adminLogin("fin@example.com", admin.RoleFinance)
	sup := e.adminLogin("sup@example.com", admin.RoleSupport)
	p := e.login(320, "en", "")
	e.give(p, ledger.Coin, 100000)
	id := int(e.wd(p, 30000, walletAddr(9)).JSON(t)["withdrawal"].(map[string]any)["id"].(float64))
	base := fmt.Sprintf("/api/admin/withdrawals/%d", id)

	if r := e.do("GET", "/api/admin/withdrawals?status=PENDING", sup, nil); r.Status != 403 {
		t.Fatalf("support has no access to withdrawals: %d", r.Status)
	}
	l := e.do("GET", "/api/admin/withdrawals?status=PENDING", fin, nil).JSON(t)["withdrawals"].([]any)
	if len(l) != 1 {
		t.Fatalf("%v", l)
	}
	if r := e.do("POST", base+"/processing", fin, nil); r.Status != 200 || r.JSON(t)["status"] != "PROCESSING" {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	// the player can no longer cancel once the admin is paying
	if r := e.do("POST", fmt.Sprintf("/api/withdrawals/%d/cancel", id), p.Token, nil); r.Code(t) != "WITHDRAWAL_NOT_CANCELLABLE" {
		t.Fatalf("%s", r.Body)
	}
	if r := e.do("POST", base+"/complete", fin, map[string]any{"txHash": "0xabc"}); r.Status != 200 || r.JSON(t)["txHash"] != "0xabc" {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	if r := e.do("POST", base+"/reject", fin, map[string]any{"reason": "late"}); r.Status != 400 {
		t.Fatalf("a completed request cannot be rejected: %d", r.Status)
	}
	// reject returns the coins exactly once
	id2 := int(func() float64 {
		e.exec(`UPDATE withdrawals SET created_at = created_at - interval '3 days'`)
		e.setSetting(settings.KeyWdCooldownHours, 0)
		return e.wd(p, 30000, walletAddr(9)).JSON(t)["withdrawal"].(map[string]any)["id"].(float64)
	}())
	before := e.coins(p)
	for i := 0; i < 3; i++ {
		e.do("POST", fmt.Sprintf("/api/admin/withdrawals/%d/reject", id2), fin, map[string]any{"reason": "bad address owner"})
	}
	if e.coins(p) != before+30000 {
		t.Fatalf("coins %d, want %d (one refund)", e.coins(p), before+30000)
	}
	hist := e.do("GET", "/api/withdrawals", p.Token, nil).JSON(t)["data"].([]any)
	if hist[0].(map[string]any)["status"] != "FAILED" || hist[1].(map[string]any)["status"] != "COMPLETED" {
		t.Fatalf("%v", hist)
	}
	// every admin action is in the audit log
	rows := e.do("GET", "/api/admin/audit?action=withdrawal.reject", e.adminLogin("root@example.com", admin.RoleSuper), nil).JSON(t)["audit"].([]any)
	if len(rows) != 1 {
		t.Fatalf("audit rows: %d", len(rows))
	}
}

// ------------------------------------------------------------ automatic payouts

type fakeSender struct {
	mu    sync.Mutex
	calls []string
	err   error
	hash  string
}

func (f *fakeSender) Address() string { return walletAddr(50) }
func (f *fakeSender) Send(ctx context.Context, payoutID, to string, amountNano int64) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, fmt.Sprintf("%s|%s|%d", payoutID, to, amountNano))
	if f.err != nil {
		return "", f.err
	}
	return f.hash, nil
}

func (e *env) autoSetup(sender withdrawals.Sender) *ton.FakeChain {
	chain := &ton.FakeChain{}
	e.d.Chain = chain
	e.wdConfig()
	e.setSetting(settings.KeyWdCooldownHours, 0)
	e.setSetting(settings.KeyWdAutoMaxTon, 1.0)
	e.setSetting(settings.KeyWdAutoDailyCap, 2.0)
	e.d.Withdrawals = withdrawals.New(e.d.Pool, e.d.Settings, e.d.Players, e.d.Chain, sender, true)
	e.d.Withdrawals.Now = func() time.Time { return e.now }
	e.d.Withdrawals.Alert = func(ctx context.Context, text string) {}
	return chain
}

func TestAutomaticPayoutSendsOnce(t *testing.T) {
	e := newEnv(t)
	sender := &fakeSender{hash: "hash-1"}
	e.autoSetup(sender)
	p := e.login(330, "en", "")
	e.give(p, ledger.Coin, 1000000)
	r := e.wd(p, 20000, walletAddr(9)) // 0.2 TON - 0.005 fee: automatic
	id := int(r.JSON(t)["withdrawal"].(map[string]any)["id"].(float64))

	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, _ = e.d.Withdrawals.RunPayouts(context.Background(), 10) }()
	}
	wg.Wait()
	if len(sender.calls) != 1 || sender.calls[0] != fmt.Sprintf("bpw%d|%s|%d", id, walletAddr(9), 195_000_000) {
		t.Fatalf("exactly one transfer expected: %v", sender.calls)
	}
	got := e.do("GET", "/api/withdrawals", p.Token, nil).JSON(t)["data"].([]any)[0].(map[string]any)
	if got["status"] != "COMPLETED" || got["txHash"] != "hash-1" {
		t.Fatalf("%v", got)
	}
	_, _ = e.d.Withdrawals.RunPayouts(context.Background(), 10)
	if len(sender.calls) != 1 {
		t.Fatal("a completed payout is never sent again")
	}
}

// The API process never holds the wallet (only the worker does), yet a small request must still be marked automatic.
func TestAutomaticPayoutIsDecidedWithoutASenderInTheAPI(t *testing.T) {
	e := newEnv(t)
	e.autoSetup(nil)
	p := e.login(331, "en", "")
	e.give(p, ledger.Coin, 1000000)
	r := e.wd(p, 20000, walletAddr(8))
	id := int64(r.JSON(t)["withdrawal"].(map[string]any)["id"].(float64))
	var auto bool
	_ = e.d.Pool.QueryRow(context.Background(), `SELECT auto FROM withdrawals WHERE id=$1`, id).Scan(&auto)
	if !auto {
		t.Fatal("a small request must be automatic even when this process has no sender")
	}
	sender := &fakeSender{hash: "hash-w"}
	worker := withdrawals.New(e.d.Pool, e.d.Settings, e.d.Players, e.d.Chain, sender, true)
	worker.Now = func() time.Time { return e.now }
	worker.Alert = func(ctx context.Context, text string) {}
	_, _ = worker.RunPayouts(context.Background(), 10)
	if len(sender.calls) != 1 {
		t.Fatalf("the worker must pay it: %v", sender.calls)
	}
}

func TestAutomaticPayoutLimitsAndExclusions(t *testing.T) {
	e := newEnv(t)
	sender := &fakeSender{hash: "h"}
	e.autoSetup(sender)
	isAuto := func(id any) bool {
		var a bool
		_ = e.d.Pool.QueryRow(context.Background(), `SELECT auto FROM withdrawals WHERE id=$1`, int64(id.(float64))).Scan(&a)
		return a
	}
	mk := func(tg int64, coins int64) any {
		p := e.login(tg, "en", "")
		e.give(p, ledger.Coin, 5000000)
		r := e.wd(p, coins, walletAddr(9))
		if r.Status != 200 {
			t.Fatalf("%d %s", r.Status, r.Body)
		}
		return r.JSON(t)["withdrawal"].(map[string]any)["id"]
	}
	if !isAuto(mk(331, 50000)) { // 0.495 TON
		t.Fatal("a small payout is automatic")
	}
	if isAuto(mk(332, 150000)) { // 1.495 TON > the 1 TON automatic limit
		t.Fatal("a big payout is manual")
	}
	if !isAuto(mk(333, 100000)) { // 0.995: total so far 0.495 + 0.995 = 1.49 <= cap 2
		t.Fatal("within the daily cap")
	}
	if isAuto(mk(334, 100000)) { // would reach 2.485 > 2
		t.Fatal("over the daily cap must be manual")
	}
	// a blocked or held player's automatic payout is handed to a human instead of being sent
	id := mk(335, 20000)
	e.exec(`UPDATE players SET hold_withdrawals=TRUE WHERE telegram_id=335`)
	n, _ := e.d.Withdrawals.RunPayouts(context.Background(), 20)
	if n != 2 { // 331 and 333 only
		t.Fatalf("sent %d, want 2", n)
	}
	if isAuto(id) {
		t.Fatal("a held player's request must leave the automatic queue")
	}
}

func TestAutomaticPayoutFailureModes(t *testing.T) {
	e := newEnv(t)
	sender := &fakeSender{err: fmt.Errorf("%w: wallet balance too low", withdrawals.ErrNotSent)}
	chain := e.autoSetup(sender)
	p := e.login(340, "en", "")
	e.give(p, ledger.Coin, 1000000)
	id := int(e.wd(p, 20000, walletAddr(9)).JSON(t)["withdrawal"].(map[string]any)["id"].(float64))

	// certainly not sent -> back to the manual queue, coins stay taken
	_, _ = e.d.Withdrawals.RunPayouts(context.Background(), 5)
	var status string
	var auto bool
	q := func() {
		_ = e.d.Pool.QueryRow(context.Background(), `SELECT status, auto FROM withdrawals WHERE id=$1`, id).Scan(&status, &auto)
	}
	q()
	if status != "PENDING" || auto {
		t.Fatalf("not sent: %s auto=%v", status, auto)
	}
	if e.coins(p) != 980000 {
		t.Fatal("coins stay with the request")
	}
	_, _ = e.d.Withdrawals.RunPayouts(context.Background(), 5)
	if len(sender.calls) != 1 {
		t.Fatal("a handed-back request is not tried again automatically")
	}

	// unclear result -> flagged, never resent, rejecting needs force
	p2 := e.login(341, "en", "")
	e.give(p2, ledger.Coin, 1000000)
	id2 := int(e.wd(p2, 20000, walletAddr(9)).JSON(t)["withdrawal"].(map[string]any)["id"].(float64))
	sender.err = errors.New("timeout while waiting for confirmation")
	_, _ = e.d.Withdrawals.RunPayouts(context.Background(), 5)
	_ = e.d.Pool.QueryRow(context.Background(), `SELECT status, auto FROM withdrawals WHERE id=$1`, id2).Scan(&status, &auto)
	var review bool
	_ = e.d.Pool.QueryRow(context.Background(), `SELECT needs_review FROM withdrawals WHERE id=$1`, id2).Scan(&review)
	if status != "PROCESSING" || !review {
		t.Fatalf("unclear: %s review=%v", status, review)
	}
	calls := len(sender.calls)
	sender.err = nil
	sender.hash = "late"
	_, _ = e.d.Withdrawals.RunPayouts(context.Background(), 5)
	if len(sender.calls) != calls {
		t.Fatal("an unclear payout must never be sent again")
	}
	fin := e.adminLogin("fin@example.com", admin.RoleFinance)
	if r := e.do("POST", fmt.Sprintf("/api/admin/withdrawals/%d/reject", id2), fin, map[string]any{"reason": "x"}); r.Status != 400 {
		t.Fatalf("rejecting an unclear payout needs force: %d", r.Status)
	}

	// reconcile finds the transfer on the chain (the crash happened after sending)
	chain.Txs = []ton.Tx{{Hash: "onchain-hash", Outgoing: []ton.OutMsg{{Destination: walletAddr(9), Value: 195_000_000, Comment: fmt.Sprintf("bpw%d", id2)}}}}
	e.d.Withdrawals = withdrawals.New(e.d.Pool, e.d.Settings, e.d.Players, e.d.Chain, sender, true)
	e.d.Withdrawals.Now = func() time.Time { return e.now.Add(5 * time.Minute) }
	n, err := e.d.Withdrawals.Reconcile(context.Background())
	if err != nil || n != 1 {
		t.Fatalf("reconcile: %d %v", n, err)
	}
	var hash string
	_ = e.d.Pool.QueryRow(context.Background(), `SELECT status, tx_hash FROM withdrawals WHERE id=$1`, id2).Scan(&status, &hash)
	if status != "COMPLETED" || hash != "onchain-hash" {
		t.Fatalf("%s %s", status, hash)
	}
	// with force an admin may reject a payout they checked was never sent
	e.exec(`UPDATE withdrawals SET status='PROCESSING', needs_review=TRUE, tx_hash=NULL WHERE id=$1`, id2)
	before := e.coins(p2)
	if r := e.do("POST", fmt.Sprintf("/api/admin/withdrawals/%d/reject", id2), fin, map[string]any{"reason": "checked: never sent", "force": true}); r.Status != 200 || e.coins(p2) != before+20000 {
		t.Fatalf("force reject: %d %s coins=%d", r.Status, r.Body, e.coins(p2))
	}
}

func TestAutomaticPayoutIsOffByDefault(t *testing.T) {
	e := newEnv(t)
	e.wdConfig()
	e.setSetting(settings.KeyWdAutoMaxTon, 5)
	e.setSetting(settings.KeyWdAutoDailyCap, 50)
	p := e.login(350, "en", "")
	e.give(p, ledger.Coin, 100000)
	id := int(e.wd(p, 20000, walletAddr(9)).JSON(t)["withdrawal"].(map[string]any)["id"].(float64))
	var auto bool
	_ = e.d.Pool.QueryRow(context.Background(), `SELECT auto FROM withdrawals WHERE id=$1`, id).Scan(&auto)
	if auto {
		t.Fatal("without AUTO_PAYOUT and a payout wallet nothing is automatic")
	}
}
