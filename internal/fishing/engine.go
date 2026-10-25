// Package fishing is the mining simulation: the tank fills from timestamps, never from client ticks.
// The engine is pure maths on integers (micro-coins, microseconds) so it is exact and deterministic.
package fishing

import (
	"math"
	"math/bits"
	"sort"
	"time"
)

// Micro is the number of micro-coins in one coin. The tank is kept in micro-coins; balances are whole coins.
const Micro int64 = 1_000_000

// Window is a period during which the mining-speed booster is active: [From, To).
type Window struct{ From, To time.Time }

// Params is a settled snapshot of a session plus the facts needed to move it forward in time.
type Params struct {
	SettledMicro int64     // tank content at SettledAt
	SettledAt    time.Time // the moment the snapshot was taken
	BaseRate     int64     // micro-coins per second without a booster
	Capacity     int64     // tank size in micro-coins
	MultMilli    int64     // booster multiplier in thousandths (1500 = x1.5); 0 or 1000 = none
	Windows      []Window  // booster windows (any order; overlapping ones are merged)
}

// Result is the tank at a moment.
type Result struct {
	Amount int64      // micro-coins in the tank
	Full   bool       // the tank is at capacity
	FullAt *time.Time // when it became (or will become) full, if known from this computation
}

// mulDiv returns a*b/c without overflow (clamped to MaxInt64).
func mulDiv(a, b, c int64) int64 {
	if a <= 0 || b <= 0 || c <= 0 {
		return 0
	}
	hi, lo := bits.Mul64(uint64(a), uint64(b))
	if hi >= uint64(c) {
		return math.MaxInt64
	}
	q, _ := bits.Div64(hi, lo, uint64(c))
	if q > math.MaxInt64 {
		return math.MaxInt64
	}
	return int64(q)
}

// ceilDiv returns ceil(a*b/c) for positive numbers (clamped).
func ceilDiv(a, b, c int64) int64 {
	if a <= 0 || b <= 0 || c <= 0 {
		return 0
	}
	hi, lo := bits.Mul64(uint64(a), uint64(b))
	if hi >= uint64(c) {
		return math.MaxInt64
	}
	q, r := bits.Div64(hi, lo, uint64(c))
	if r > 0 {
		q++
	}
	if q > math.MaxInt64 {
		return math.MaxInt64
	}
	return int64(q)
}

func mergeWindows(ws []Window) []Window {
	out := make([]Window, 0, len(ws))
	for _, w := range ws {
		if w.To.After(w.From) {
			out = append(out, w)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].From.Before(out[j].From) })
	merged := out[:0:0]
	for _, w := range out {
		if n := len(merged); n > 0 && !w.From.After(merged[n-1].To) {
			if w.To.After(merged[n-1].To) {
				merged[n-1].To = w.To
			}
			continue
		}
		merged = append(merged, w)
	}
	return merged
}

func (p Params) rateAt(t time.Time, ws []Window) int64 {
	if p.MultMilli > 0 && p.MultMilli != 1000 {
		for _, w := range ws {
			if !t.Before(w.From) && t.Before(w.To) {
				return mulDiv(p.BaseRate, p.MultMilli, 1000)
			}
		}
	}
	return p.BaseRate
}

// breakpoints returns the instants after `from` where the rate changes.
func (p Params) breakpoints(from time.Time, ws []Window) []time.Time {
	var pts []time.Time
	if p.MultMilli <= 0 || p.MultMilli == 1000 {
		return nil
	}
	for _, w := range ws {
		if w.From.After(from) {
			pts = append(pts, w.From)
		}
		if w.To.After(from) {
			pts = append(pts, w.To)
		}
	}
	sort.Slice(pts, func(i, j int) bool { return pts[i].Before(pts[j]) })
	return pts
}

// Tank returns the tank content at `now`: min(capacity, settled + sum of rate x seconds over the segments).
func Tank(p Params, now time.Time) Result {
	cur := p.SettledMicro
	if cur < 0 {
		cur = 0
	}
	if cur >= p.Capacity {
		t := p.SettledAt
		return Result{Amount: p.Capacity, Full: true, FullAt: &t}
	}
	if !now.After(p.SettledAt) {
		return Result{Amount: cur}
	}
	ws := mergeWindows(p.Windows)
	a := p.SettledAt
	points := append(p.breakpoints(p.SettledAt, ws), now)
	for _, b := range points {
		if b.After(now) {
			b = now
		}
		if !b.After(a) {
			continue
		}
		rate := p.rateAt(a, ws)
		dt := b.Sub(a).Microseconds()
		gain := mulDiv(rate, dt, 1_000_000) // micro-coins per second x microseconds / 1e6
		if cur+gain >= p.Capacity || gain == math.MaxInt64 {
			need := p.Capacity - cur
			us := ceilDiv(need, 1_000_000, rate)
			t := a.Add(time.Duration(us) * time.Microsecond)
			return Result{Amount: p.Capacity, Full: true, FullAt: &t}
		}
		cur += gain
		a = b
		if !a.Before(now) {
			break
		}
	}
	return Result{Amount: cur}
}

// FullAt returns when the tank will be full (nil when it never fills, e.g. rate 0).
func FullAt(p Params) *time.Time {
	cur := p.SettledMicro
	if cur >= p.Capacity {
		t := p.SettledAt
		return &t
	}
	ws := mergeWindows(p.Windows)
	a := p.SettledAt
	for _, b := range p.breakpoints(p.SettledAt, ws) {
		if !b.After(a) {
			continue
		}
		rate := p.rateAt(a, ws)
		gain := mulDiv(rate, b.Sub(a).Microseconds(), 1_000_000)
		if cur+gain >= p.Capacity {
			us := ceilDiv(p.Capacity-cur, 1_000_000, rate)
			t := a.Add(time.Duration(us) * time.Microsecond)
			return &t
		}
		cur += gain
		a = b
	}
	rate := p.rateAt(a, ws)
	if rate <= 0 {
		return nil
	}
	us := ceilDiv(p.Capacity-cur, 1_000_000, rate)
	if us >= math.MaxInt64/2 {
		return nil
	}
	t := a.Add(time.Duration(us) * time.Microsecond)
	return &t
}

// RateChange is a future change of the effective mining speed.
type RateChange struct {
	At   time.Time `json:"at"`
	Rate int64     `json:"-"` // micro-coins per second from At
}

// EffectiveRate is the speed at `t` in micro-coins per second.
func EffectiveRate(p Params, t time.Time) int64 { return p.rateAt(t, mergeWindows(p.Windows)) }

// NextRateChange returns the next instant after `from` where the speed changes (with the new speed).
func NextRateChange(p Params, from time.Time) *RateChange {
	ws := mergeWindows(p.Windows)
	pts := p.breakpoints(from, ws)
	if len(pts) == 0 {
		return nil
	}
	return &RateChange{At: pts[0], Rate: p.rateAt(pts[0], ws)}
}

// CoinsOf converts micro-coins to whole coins (rounded down).
func CoinsOf(micro int64) int64 {
	if micro <= 0 {
		return 0
	}
	return micro / Micro
}

// MicroOf converts a coin amount with decimals to micro-coins (rounded to the nearest micro-coin).
func MicroOf(coins float64) int64 { return int64(math.Round(coins * float64(Micro))) }

// MultMilliOf converts 1.5 to 1500.
func MultMilliOf(m float64) int64 { return int64(math.Round(m * 1000)) }
