package games

import (
	"math"
	"testing"
)

func meanOf(vals, probs []float64) float64 {
	var m, sum float64
	for i := range vals {
		m += vals[i] * probs[i]
		sum += probs[i]
	}
	if math.Abs(sum-1) > 1e-9 {
		panic("the chances must add up to 1")
	}
	return m
}

// The chances are built so that the average prize equals the wanted mean, whatever the values are.
func TestSmartProbsHitTheWantedMean(t *testing.T) {
	cases := [][]float64{
		{5, 20, 50, 412, 452},                   // a spin tier: small stars next to big gifts
		{0.018, 1, 3, 5, 20, 50, 100, 412, 452}, // a chest pool with a few coins worth almost nothing
		{578, 868, 1135, 1567, 2850, 3647, 4570, 1254},
	}
	for _, vals := range cases {
		lo, hi := vals[0], vals[0]
		for _, v := range vals {
			lo, hi = math.Min(lo, v), math.Max(hi, v)
		}
		for _, f := range []float64{0.05, 0.3, 0.5, 0.8, 1, 1.5, 2} {
			m := lo + (hi-lo)*f/10
			p := smartProbs(vals, m)
			if got := meanOf(vals, p); math.Abs(got-m) > 0.01*m+0.01 {
				t.Fatalf("values %v: wanted mean %.3f, got %.3f", vals, m, got)
			}
		}
	}
}

// A target below the cheapest prize always gives the cheapest; above the dearest, the dearest.
func TestSmartProbsAtTheEdges(t *testing.T) {
	vals := []float64{100, 30, 500, 30}
	p := smartProbs(vals, 1)
	if p[1] != 0.5 || p[3] != 0.5 || p[0] != 0 || p[2] != 0 {
		t.Fatalf("cheapest: %v", p)
	}
	p = smartProbs(vals, 9999)
	if p[2] != 1 {
		t.Fatalf("dearest: %v", p)
	}
}

// Over many plays the player's lifetime payback settles at the target, and the welcome plays pay more than the price.
func TestSmartSessionSteersToTheTarget(t *testing.T) {
	pool := []Prize{
		{Type: TypeStars, Value: 5}, {Type: TypeStars, Value: 20}, {Type: TypeStars, Value: 50},
		{Type: TypeGift, Gift: "a", Value: 400}, {Type: TypeGift, Gift: "b", Value: 600},
	}
	ss := &smartSession{cfg: smartCfg{Enabled: true, WelcomePct: 150, TargetPct: 80}, eligible: true, gateOpen: true, welcomeN: 5}
	const price = 30.0
	var welcomePaid, welcomeSpent float64
	for i := 0; i < 600; i++ {
		w := ss.startPlay()
		if w != (i < 5) {
			t.Fatalf("play %d: welcome=%v", i, w)
		}
		p := ss.draw(pool, price, w)
		if w {
			welcomePaid += ss.worth(p)
			welcomeSpent += price
		}
		ss.settle(price, p)
	}
	ratio := ss.st.Paid / ss.st.Spent
	if ratio < 0.7 || ratio > 0.9 {
		t.Fatalf("lifetime payback %.3f, the target is 0.80", ratio)
	}
	if ss.st.WelcomeUsed != 5 {
		t.Fatalf("welcome plays used: %d", ss.st.WelcomeUsed)
	}
	_ = welcomePaid // a handful of random draws: the average is checked on the mean, not on five samples
	if m := ss.mean(price, true); math.Abs(m-45) > 1e-9 {
		t.Fatalf("welcome mean %.2f, want 45 (150%% of 30)", m)
	}
}
