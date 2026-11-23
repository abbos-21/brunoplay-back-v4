package api_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"brunoplay/internal/admin"
	"brunoplay/internal/settings"
)

// fakeQuoter answers with the prices it is given and remembers what it was asked
type fakeQuoter struct {
	price map[int64]int64
	err   error
	asked []int64
}

func (f *fakeQuoter) StarsPriceNano(ctx context.Context, stars int64) (int64, error) {
	f.asked = append(f.asked, stars)
	if f.err != nil {
		return 0, f.err
	}
	return f.price[stars], nil
}

func TestTonPriceFollowsFragmentForStarsPlusBonus(t *testing.T) {
	e := newEnv(t)
	e.moneyConfig() // packs 200, 500, 1000; manual rate 100 stars per TON; TON bonus 10 %
	q := &fakeQuoter{price: map[int64]int64{220: 1_100_000_000, 550: 2_500_000_000, 1100: 4_900_000_000}}
	e.d.Topup.Quoter = q
	var alerts []string
	e.d.Topup.Alert = func(ctx context.Context, text string) { alerts = append(alerts, text) }
	p := e.login(890, "en", "")
	ctx := context.Background()

	// off: nothing changes, the manual rate on the pack's stars (500 stars = 5 TON)
	if err := e.d.Topup.RefreshQuotes(ctx); err != nil || len(q.asked) != 0 {
		t.Fatalf("off does not call Fragment: %v %v", err, q.asked)
	}
	if o := e.tonOrder(p, 500); int64(o["amountNano"].(float64)) != 5_000_000_000 {
		t.Fatalf("manual price: %v", o)
	}

	// on: 500 + 10% bonus = Fragment's price for 550 stars, plus the 5% margin
	e.setSetting(settings.KeyTopupTonAuto, true)
	e.setSetting(settings.KeyTopupTonMargin, 5)
	if err := e.d.Topup.RefreshQuotes(ctx); err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprint(q.asked); got != "[220 550 1100]" {
		t.Fatalf("asked Fragment for stars + bonus: %s", got)
	}
	o := e.tonOrder(p, 500)
	if int64(o["amountNano"].(float64)) != 2_625_000_000 || int64(o["bonusStars"].(float64)) != 50 {
		t.Fatalf("live price with margin: %v", o)
	}
	packs := e.do("GET", "/api/topup/packs", p.Token, nil).JSON(t)
	list := packs["packs"].([]any)
	if packs["tonAvailable"] != true || list[1].(map[string]any)["ton"] != "2.625" {
		t.Fatalf("the packs show the live price: %v", packs)
	}

	// a wild answer is not used; the earlier price stays
	q.price[550] = 25_000_000_000
	if err := e.d.Topup.RefreshQuotes(ctx); err == nil {
		t.Fatal("a jump of several times is refused")
	}
	if o := e.tonOrder(p, 500); int64(o["amountNano"].(float64)) != 2_625_000_000 {
		t.Fatalf("the old price stays: %v", o)
	}

	// no answer for over 30 minutes: the manual rate on stars + bonus (550 stars = 5.5 TON) and one alert
	q.err = errors.New("fragment: HTTP 403 IP_NOT_ALLOWED")
	e.now = e.now.Add(31 * time.Minute)
	for i := 0; i < 2; i++ {
		if err := e.d.Topup.RefreshQuotes(ctx); err == nil {
			t.Fatal("the failure is reported")
		}
	}
	if o := e.tonOrder(p, 500); int64(o["amountNano"].(float64)) != 5_500_000_000 {
		t.Fatalf("fallback: manual rate on stars + bonus: %v", o)
	}
	if len(alerts) != 1 || !strings.Contains(alerts[0], "automatic TON price") {
		t.Fatalf("one alert: %v", alerts)
	}

	// it is back: fresh price again, and the next outage can alert again
	q.err = nil
	q.price[550] = 2_600_000_000
	if err := e.d.Topup.RefreshQuotes(ctx); err != nil {
		t.Fatal(err)
	}
	if o := e.tonOrder(p, 500); int64(o["amountNano"].(float64)) != 2_730_000_000 {
		t.Fatalf("back to live: %v", o)
	}

	// the admin sees the prices and can refresh
	super := e.adminLogin("owner@example.com", admin.RoleSuper)
	view := e.do("GET", "/api/admin/topup/ton-quotes", super, nil).JSON(t)
	if view["auto"] != true || view["keySet"] != true || len(view["quotes"].([]any)) != 3 {
		t.Fatalf("admin view: %v", view)
	}
	if r := e.do("POST", "/api/admin/topup/ton-quotes/refresh", super, nil); r.Status != 200 {
		t.Fatalf("admin refresh: %d %s", r.Status, r.Body)
	}
}
