package api_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"brunoplay/internal/settings"
	"brunoplay/internal/telegram"
	"brunoplay/internal/ton"
)

// A friend's top-up (Telegram Stars or TON) pays the inviter a percent in stars. No level is needed, the top-up also makes the friend count,
// a refund takes the share back, and a blocked inviter gets nothing.
func TestReferralTopupBonus(t *testing.T) {
	e := newEnv(t)
	e.gameConfig()
	_, chain := e.moneyConfig()
	e.setSetting(settings.KeyRefJoinBonus, 0)
	inviter := e.login(5101, "en", "")
	friend := e.login(5102, "en", fmt.Sprintf("ref_%d", inviter.ID))

	pay := func(p player, tgID, pack int64) string {
		t.Helper()
		o := e.do("POST", "/api/topup/telegram-invoice", p.Token, map[string]any{"packStars": pack}).JSON(t)
		id := o["id"].(string)
		if r := e.tgUpdate(telegram.Update{Message: &telegram.Message{From: &telegram.User{ID: tgID}, SuccessfulPayment: &telegram.SuccessfulPayment{
			Currency: "XTR", TotalAmount: pack, InvoicePayload: id, TelegramPaymentChargeID: "chg-" + id}}}); r.Status != 200 {
			t.Fatalf("pay: %d %s", r.Status, r.Body)
		}
		return id
	}
	stats := func() map[string]any { return e.do("GET", "/api/friends/stats", inviter.Token, nil).JSON(t) }

	// off (0): nothing is paid and the friend stays pending
	e.setSetting(settings.KeyRefTopupPct, 0)
	pay(friend, 5102, 200)
	if e.stars(inviter) != 0 || stats()["totalReferrals"].(float64) != 0 {
		t.Fatalf("off: stars %d, %v", e.stars(inviter), stats())
	}

	// 10%: 500 stars give 50, the friend (level 1) counts, nothing else is paid
	e.setSetting(settings.KeyRefTopupPct, 10)
	first := pay(friend, 5102, 500)
	if e.stars(inviter) != 50 || e.coins(inviter) != 0 {
		t.Fatalf("10%% of 500: stars %d coins %d", e.stars(inviter), e.coins(inviter))
	}
	st := stats()
	if st["totalReferrals"].(float64) != 1 || st["pendingReferrals"].(float64) != 0 || st["topupStars"].(float64) != 50 {
		t.Fatalf("stats: %v", st)
	}
	list := e.do("GET", "/api/friends", inviter.Token, nil).JSON(t)["friends"].([]any)
	if len(list) != 1 || list[0].(map[string]any)["topupStars"].(float64) != 50 || list[0].(map[string]any)["counted"] != true {
		t.Fatalf("friends: %v", list)
	}

	// every top-up pays; the share is rounded down (0.33% of 500 = 1.65 -> 1)
	e.setSetting(settings.KeyRefTopupPct, 0.33)
	pay(friend, 5102, 500)
	if e.stars(inviter) != 51 {
		t.Fatalf("rounded down: %d", e.stars(inviter))
	}

	// the same payment delivered again pays nothing more
	e.setSetting(settings.KeyRefTopupPct, 10)
	e.tgUpdate(telegram.Update{Message: &telegram.Message{From: &telegram.User{ID: 5102}, SuccessfulPayment: &telegram.SuccessfulPayment{
		Currency: "XTR", TotalAmount: 500, InvoicePayload: first, TelegramPaymentChargeID: "chg-" + first}}})
	if e.stars(inviter) != 51 {
		t.Fatalf("a retry paid again: %d", e.stars(inviter))
	}

	// a refund takes the share back, as far as the inviter still has the stars
	e.exec(`UPDATE players SET stars = 20 WHERE id=$1`, inviter.ID)
	e.tgUpdate(telegram.Update{Message: &telegram.Message{From: &telegram.User{ID: 5102}, RefundedPayment: &telegram.RefundedPayment{
		Currency: "XTR", TotalAmount: 500, InvoicePayload: first, TelegramPaymentChargeID: "chg-" + first}}})
	if e.stars(inviter) != 0 {
		t.Fatalf("refund clawback: %d", e.stars(inviter))
	}
	if st := stats(); st["topupStars"].(float64) != 31 { // 50 + 1 paid, 20 taken back
		t.Fatalf("stats after the refund: %v", st)
	}

	// a TON top-up pays too, from the pack (not from the friend's own TON bonus): 10% of 500 = 50
	tonFriend := e.login(5104, "en", fmt.Sprintf("ref_%d", inviter.ID))
	o := e.tonOrder(tonFriend, 500)
	chain.Txs = []ton.Tx{{Hash: "h-ref", Value: int64(o["amountNano"].(float64)), Comment: o["comment"].(string), Time: e.now.Add(time.Second)}}
	e.now = e.now.Add(time.Minute)
	if n, err := e.d.Topup.PollTon(context.Background()); err != nil || n != 1 {
		t.Fatalf("ton credit: %d %v", n, err)
	}
	if e.stars(inviter) != 50 {
		t.Fatalf("ton top-up bonus: %d", e.stars(inviter))
	}

	// a blocked inviter gets nothing (and it is not paid later)
	e.exec(`UPDATE players SET is_blocked=TRUE WHERE id=$1`, inviter.ID)
	before := e.stars(inviter)
	other := e.login(5103, "en", fmt.Sprintf("ref_%d", inviter.ID))
	pay(other, 5103, 200)
	if e.stars(inviter) != before {
		t.Fatalf("blocked inviter was paid: %d", e.stars(inviter))
	}
}
