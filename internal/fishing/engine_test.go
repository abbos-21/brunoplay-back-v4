package fishing

import (
	"math/rand"
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 24, 0, 0, 0, 0, time.UTC)

func sec(n float64) time.Duration { return time.Duration(n * float64(time.Second)) }

func TestSimpleFill(t *testing.T) {
	p := Params{SettledAt: t0, BaseRate: 25_000 /* 0.025 coins/s */, Capacity: 5 * Micro, MultMilli: 1500}
	r := Tank(p, t0.Add(sec(100)))
	if r.Amount != 2_500_000 || r.Full {
		t.Fatalf("100s at 0.025/s = 2.5 coins, got %d full=%v", r.Amount, r.Full)
	}
	r = Tank(p, t0.Add(sec(200)))
	if r.Amount != 5*Micro || !r.Full || r.FullAt == nil || !r.FullAt.Equal(t0.Add(sec(200))) {
		t.Fatalf("200s fills a 5-coin tank exactly: %+v", r)
	}
	r = Tank(p, t0.Add(sec(10_000)))
	if r.Amount != 5*Micro || !r.Full || !r.FullAt.Equal(t0.Add(sec(200))) {
		t.Fatalf("it never overfills and FullAt stays the true moment: %+v", r)
	}
	if got := FullAt(p); got == nil || !got.Equal(t0.Add(sec(200))) {
		t.Fatalf("FullAt = %v", got)
	}
	if got := CoinsOf(2_999_999); got != 2 {
		t.Fatalf("coins are rounded down: %d", got)
	}
}

func TestNothingBeforeStartAndZeroRate(t *testing.T) {
	p := Params{SettledAt: t0, BaseRate: 10_000, Capacity: Micro, SettledMicro: 123}
	if r := Tank(p, t0.Add(-time.Hour)); r.Amount != 123 {
		t.Fatalf("time before the snapshot changes nothing: %d", r.Amount)
	}
	z := Params{SettledAt: t0, BaseRate: 0, Capacity: Micro}
	if r := Tank(z, t0.Add(24*time.Hour)); r.Amount != 0 || r.Full {
		t.Fatalf("zero rate never fills: %+v", r)
	}
	if FullAt(z) != nil {
		t.Fatal("zero rate has no full time")
	}
}

func TestBoosterWindowMidSession(t *testing.T) {
	// 1 coin/s base, x1.5 between 100s and 200s. Capacity large.
	p := Params{SettledAt: t0, BaseRate: Micro, Capacity: 1000 * Micro, MultMilli: 1500,
		Windows: []Window{{From: t0.Add(sec(100)), To: t0.Add(sec(200))}}}
	cases := map[float64]int64{
		50:  50 * Micro,                           // before the window
		100: 100 * Micro,                          // the window starts
		150: 100*Micro + 50*1_500_000,             // 50s boosted
		200: 100*Micro + 100*1_500_000,            // the window ends
		260: 100*Micro + 100*1_500_000 + 60*Micro, // normal again
	}
	for s, want := range cases {
		if got := Tank(p, t0.Add(sec(s))).Amount; got != want {
			t.Errorf("at %vs: got %d want %d", s, got, want)
		}
	}
}

func TestBoosterOnlyAffectsItsSeconds(t *testing.T) {
	// the booster started before the snapshot and ends inside the interval
	p := Params{SettledAt: t0.Add(sec(1000)), BaseRate: 100_000, Capacity: 1000 * Micro, MultMilli: 1500,
		Windows: []Window{{From: t0, To: t0.Add(sec(1010))}}}
	got := Tank(p, t0.Add(sec(1030))).Amount
	want := int64(10*150_000 + 20*100_000)
	if got != want {
		t.Fatalf("10s boosted + 20s normal: got %d want %d", got, want)
	}
	// a booster entirely in the past or future of the interval changes nothing
	q := p
	q.Windows = []Window{{From: t0, To: t0.Add(sec(500))}, {From: t0.Add(sec(5000)), To: t0.Add(sec(6000))}}
	if got := Tank(q, t0.Add(sec(1030))).Amount; got != 30*100_000 {
		t.Fatalf("got %d", got)
	}
}

func TestFullDuringBoost(t *testing.T) {
	// capacity 300, 1/s, x2 from 100s: full when 100 + 2*(t-100) = 300 -> t = 200s
	p := Params{SettledAt: t0, BaseRate: Micro, Capacity: 300 * Micro, MultMilli: 2000,
		Windows: []Window{{From: t0.Add(sec(100)), To: t0.Add(sec(10_000))}}}
	got := FullAt(p)
	if got == nil || !got.Equal(t0.Add(sec(200))) {
		t.Fatalf("FullAt = %v, want 200s", got)
	}
	r := Tank(p, t0.Add(sec(500)))
	if !r.Full || !r.FullAt.Equal(t0.Add(sec(200))) {
		t.Fatalf("%+v", r)
	}
}

func TestOverlappingWindowsAreMerged(t *testing.T) {
	p := Params{SettledAt: t0, BaseRate: Micro, Capacity: 1e6 * Micro, MultMilli: 1500,
		Windows: []Window{{From: t0, To: t0.Add(sec(60))}, {From: t0.Add(sec(30)), To: t0.Add(sec(90))}}}
	// merged [0,90): the booster must not be counted twice
	if got := Tank(p, t0.Add(sec(90))).Amount; got != 135*Micro {
		t.Fatalf("got %d want %d", got, 135*Micro)
	}
}

func TestSettleAndContinueEqualsDirect(t *testing.T) {
	rnd := rand.New(rand.NewSource(1))
	for i := 0; i < 2000; i++ {
		p := Params{SettledAt: t0, BaseRate: int64(rnd.Intn(500_000) + 1), Capacity: int64(rnd.Intn(500)+1) * Micro, MultMilli: 1500}
		for w := 0; w < rnd.Intn(4); w++ {
			from := t0.Add(sec(float64(rnd.Intn(5000))))
			p.Windows = append(p.Windows, Window{From: from, To: from.Add(sec(float64(rnd.Intn(3000) + 1)))})
		}
		end := t0.Add(sec(float64(rnd.Intn(8000))))
		mid := t0.Add(sec(float64(rnd.Intn(8000))))
		if mid.After(end) {
			mid, end = end, mid
		}
		direct := Tank(p, end)
		m := Tank(p, mid)
		q := p
		q.SettledMicro, q.SettledAt = m.Amount, mid
		via := Tank(q, end)
		// settling rounds each segment down by at most 1 micro-coin per segment
		if d := direct.Amount - via.Amount; d < -8 || d > 8 {
			t.Fatalf("settle+continue differs by %d (direct %d via %d) case %d", d, direct.Amount, via.Amount, i)
		}
	}
}

func TestMonotonicAndFullAtConsistent(t *testing.T) {
	rnd := rand.New(rand.NewSource(2))
	for i := 0; i < 2000; i++ {
		p := Params{SettledAt: t0, BaseRate: int64(rnd.Intn(300_000) + 1), Capacity: int64(rnd.Intn(300)+1) * Micro, MultMilli: 1500}
		for w := 0; w < rnd.Intn(3); w++ {
			from := t0.Add(sec(float64(rnd.Intn(3000))))
			p.Windows = append(p.Windows, Window{From: from, To: from.Add(sec(float64(rnd.Intn(2000) + 1)))})
		}
		prev := int64(0)
		for s := 0; s <= 20000; s += 997 {
			a := Tank(p, t0.Add(sec(float64(s)))).Amount
			if a < prev || a > p.Capacity {
				t.Fatalf("not monotonic / over capacity: %d after %d (cap %d)", a, prev, p.Capacity)
			}
			prev = a
		}
		fa := FullAt(p)
		if fa == nil {
			t.Fatal("positive rate must fill")
		}
		if r := Tank(p, *fa); !r.Full {
			t.Fatalf("tank must be full at FullAt (%d of %d)", r.Amount, p.Capacity)
		}
		if r := Tank(p, fa.Add(-time.Second)); r.Full && r.Amount >= p.Capacity && p.BaseRate < 1_000_000 {
			// one second earlier the tank can only be full if less than a second of mining was left
			t.Fatalf("full a second before FullAt: %d of %d rate %d", r.Amount, p.Capacity, p.BaseRate)
		}
	}
}

func TestHugeNumbersDoNotOverflow(t *testing.T) {
	p := Params{SettledAt: t0, BaseRate: 1 << 50, Capacity: 1 << 61, MultMilli: 1500}
	r := Tank(p, t0.Add(100*365*24*time.Hour))
	if r.Amount != p.Capacity || !r.Full {
		t.Fatalf("%+v", r)
	}
}

func TestNextRateChange(t *testing.T) {
	p := Params{SettledAt: t0, BaseRate: Micro, Capacity: 1e6 * Micro, MultMilli: 1500,
		Windows: []Window{{From: t0.Add(sec(100)), To: t0.Add(sec(200))}}}
	rc := NextRateChange(p, t0)
	if rc == nil || !rc.At.Equal(t0.Add(sec(100))) || rc.Rate != 1_500_000 {
		t.Fatalf("%+v", rc)
	}
	rc = NextRateChange(p, t0.Add(sec(150)))
	if rc == nil || !rc.At.Equal(t0.Add(sec(200))) || rc.Rate != Micro {
		t.Fatalf("%+v", rc)
	}
	if NextRateChange(p, t0.Add(sec(300))) != nil {
		t.Fatal("no more changes")
	}
	if EffectiveRate(p, t0.Add(sec(150))) != 1_500_000 || EffectiveRate(p, t0) != Micro {
		t.Fatal("effective rate")
	}
}
