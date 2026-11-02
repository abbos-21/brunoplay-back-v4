package games

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// Monte Carlo of the smart system on the REAL economy (scripts/economy-answers.json): thousands of simulated players, many play lengths,
// every spin tier and the chest. Answers one question: does the project keep its profit?
// Run with: go test ./internal/games -run TestSmartProfit -v

type simAnswers struct {
	Settings []struct {
		Key   string `json:"key"`
		Value any    `json:"value"`
	} `json:"settings"`
	Tables map[string]struct {
		Rows []map[string]any `json:"rows"`
	} `json:"tables"`
}

func loadSim(t *testing.T) (tiers map[string][]Prize, prices map[string]float64, chest []Prize, chestCost float64, coinStars float64) {
	t.Helper()
	b, err := os.ReadFile("../../scripts/economy-answers.json")
	if err != nil {
		t.Skip("no answers file")
	}
	var a simAnswers
	if err := json.Unmarshal(b, &a); err != nil {
		t.Fatal(err)
	}
	num := func(v any) float64 {
		switch x := v.(type) {
		case float64:
			return x
		case string:
			f, _ := strconv.ParseFloat(x, 64)
			return f
		}
		return 0
	}
	coinStars = 0.00006 // the seed's override of guard.coin_value_stars
	for _, s := range a.Settings {
		if s.Key == "chest.round_cost_stars" {
			chestCost = num(s.Value)
		}
	}
	gifts := map[string]int64{}
	for _, r := range a.Tables["gifts"].Rows {
		gifts[r["key"].(string)] = int64(num(r["star_value"]))
	}
	prices = map[string]float64{}
	for _, r := range a.Tables["spin_tiers"].Rows {
		prices[r["tier"].(string)] = num(r["price_stars"])
	}
	tiers = map[string][]Prize{}
	cols := map[string]string{"t1": "TIER_1", "t2": "TIER_2", "t3": "TIER_3", "t4": "TIER_4", "t5": "TIER_5"}
	for _, r := range a.Tables["spin_odds"].Rows {
		key := r["prize"].(string)
		var p Prize
		switch {
		case strings.HasPrefix(key, "stars:"):
			v, _ := strconv.ParseInt(strings.TrimPrefix(key, "stars:"), 10, 64)
			p = Prize{Type: TypeStars, Value: v}
		case strings.HasPrefix(key, "coins:"):
			v, _ := strconv.ParseInt(strings.TrimPrefix(key, "coins:"), 10, 64)
			p = Prize{Type: TypeCoins, Value: v}
		default:
			p = Prize{Type: TypeGift, Gift: key, Value: gifts[key]}
		}
		for col, tier := range cols {
			if num(r[col]) > 0 {
				tiers[tier] = append(tiers[tier], p)
			}
		}
	}
	for _, r := range a.Tables["chest_pool"].Rows {
		switch r["type"] {
		case "gift":
			g := r["gift"].(string)
			chest = append(chest, Prize{Type: TypeGift, Gift: g, Value: gifts[g]})
		case "coins":
			chest = append(chest, Prize{Type: TypeCoins, Value: int64(num(r["amount"]))})
		default:
			chest = append(chest, Prize{Type: TypeStars, Value: int64(num(r["amount"]))})
		}
	}
	return
}

type simResult struct{ spent, paid []float64 }

// one player who makes n plays of a game
func simPlayer(cfg smartCfg, coinStars float64, pool []Prize, price float64, picks int, n int, welcomeN int64) (spent, paid float64) {
	ss := &smartSession{cfg: cfg, eligible: true, gateOpen: true, welcomeN: welcomeN, coinStars: coinStars}
	for i := 0; i < n; i++ {
		w := ss.startPlay()
		for k := 0; k < picks; k++ {
			p := ss.draw(pool, price, w)
			ss.settle(price, p)
		}
	}
	return ss.st.Spent, ss.st.Paid
}

func pct(sorted []float64, q float64) float64 { return sorted[int(q*float64(len(sorted)-1))] }

func TestSmartProfitSimulation(t *testing.T) {
	if os.Getenv("SMART_SIM") == "" {
		t.Skip("the long table of results: run with SMART_SIM=1 and -v")
	}
	tiers, prices, chest, chestCost, coinStars := loadSim(t)
	cfg := smartCfg{Enabled: true, WelcomePct: 150, TargetPct: 80}
	const M = 400
	lengths := []int{1, 3, 5, 6, 8, 10, 15, 20, 30, 50, 100, 300}

	type game struct {
		name  string
		pool  []Prize
		price float64
		picks int
	}
	var games []game
	for _, tier := range []string{"TIER_1", "TIER_2", "TIER_3", "TIER_4", "TIER_5"} {
		games = append(games, game{"spin " + tier, tiers[tier], prices[tier], 1})
	}
	games = append(games, game{"chest", chest, chestCost / 3, 3})

	// profit of the project at each play length, per game (mean over M players): 1 - paid/spent
	breakEven := map[string]int{}
	for _, g := range games {
		var sb strings.Builder
		fmt.Fprintf(&sb, "\n%s (price %.0f): plays -> profit %% of spend (mean) | players in loss %% | worst 5%% player loses (x price)\n", g.name, g.price*float64(g.picks))
		firstProfit := -1
		for _, n := range lengths {
			var sumSpent, sumPaid float64
			prof := make([]float64, M)
			loss := 0
			for i := 0; i < M; i++ {
				s, p := simPlayer(cfg, coinStars, g.pool, g.price, g.picks, n, 5)
				sumSpent += s
				sumPaid += p
				prof[i] = s - p
				if p > s {
					loss++
				}
			}
			sort.Float64s(prof)
			mean := (1 - sumPaid/sumSpent) * 100
			fmt.Fprintf(&sb, "  %5d plays: %+7.1f%% | %5.1f%% | %8.1f\n", n, mean, float64(loss)/M*100, pct(prof, 0.05)/(g.price*float64(g.picks)))
			if mean > 0 && firstProfit < 0 {
				firstProfit = n
			}
		}
		breakEven[g.name] = firstProfit
		t.Log(sb.String())
	}
	t.Logf("first tested play length where the mean is already in profit: %v", breakEven)
}

// ---- a whole population over time, with the overall limit on the welcome plays (smart.welcome_gate_percent)

// churn: how many plays each paying player makes before leaving (weights add up to 1)
type churn struct {
	name  string
	plays []int
	share []float64
}

func (c churn) draw() int {
	r := randFloat()
	for i, s := range c.share {
		if r < s {
			return c.plays[i]
		}
		r -= s
	}
	return c.plays[len(c.plays)-1]
}

// simPopulation: P players arrive over time and play one play at a time in random order; the welcome is only given while the paid/spent
// of the last `window` plays of the whole game is under gatePct. Returns the project's profit as a share of everything spent.
// stepDown is the payout guard (prizes.go): while paying the drawn prize would push the window's paid/spent over max, take the next smaller prize.
func stepDown(vals []float64, drawn int, paid, spent, reserve, max float64) int {
	idx := make([]int, len(vals))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool { return vals[idx[a]] > vals[idx[b]] })
	ratio := func(v float64) float64 { return (paid + v + reserve) / spent }
	if ratio(vals[drawn]) <= max {
		return drawn
	}
	chosen := drawn
	for _, c := range idx {
		if vals[c] >= vals[drawn] {
			continue
		}
		chosen = c
		if ratio(vals[c]) <= max {
			break
		}
	}
	return chosen
}

func simPopulation(cfg smartCfg, guardMax float64, coinStars float64, pool []Prize, price float64, picks int, ch churn, P int, window int) (profit float64, welcomeShare float64) {
	type pl struct {
		ss   *smartSession
		left int
	}
	type rec struct{ spent, paid float64 }
	var active []*pl
	var ring []rec
	var winSpent, winPaid, totSpent, totPaid float64
	arrived, plays, welcomePlays := 0, 0, 0
	for arrived < P || len(active) > 0 {
		if arrived < P && (len(active) == 0 || randFloat() < 0.25) {
			active = append(active, &pl{ss: &smartSession{cfg: cfg, eligible: true, gateOpen: true, welcomeN: 5, coinStars: coinStars}, left: ch.draw()})
			arrived++
			continue
		}
		i := int(randFloat() * float64(len(active)))
		p := active[i]
		ss := p.ss
		ss.gateOpen = cfg.GatePct <= 0 || winSpent <= 0 || winPaid/winSpent*100 < cfg.GatePct
		w := ss.startPlay()
		var spent, paid float64
		vals := make([]float64, len(pool))
		minVal := math.Inf(1)
		for j, q := range pool {
			vals[j] = ss.worth(q)
			minVal = math.Min(minVal, vals[j])
		}
		for k := 0; k < picks; k++ {
			pr := ss.draw(pool, price, w)
			cap := guardMax
			if w {
				cap = cfg.GatePct / 100
			}
			if cap > 0 {
				di := 0
				for j := range pool {
					if pool[j].Key() == pr.Key() {
						di = j
					}
				}
				pr = pool[stepDown(vals, di, winPaid+paid, winSpent+price*float64(picks), float64(picks-1-k)*minVal, cap)]
			}
			ss.settle(price, pr)
			spent += price
			paid += ss.worth(pr)
		}
		plays++
		if w {
			welcomePlays++
		}
		ring = append(ring, rec{spent, paid})
		winSpent, winPaid = winSpent+spent, winPaid+paid
		if len(ring) > window {
			winSpent, winPaid = winSpent-ring[0].spent, winPaid-ring[0].paid
			ring = ring[1:]
		}
		totSpent, totPaid = totSpent+spent, totPaid+paid
		p.left--
		if p.left <= 0 {
			active[i] = active[len(active)-1]
			active = active[:len(active)-1]
		}
	}
	return (1 - totPaid/totSpent) * 100, float64(welcomePlays) / float64(plays) * 100
}

func TestSmartProfitWithTheOverallLimit(t *testing.T) {
	tiers, prices, chest, chestCost, coinStars := loadSim(t)
	crowds := []churn{
		{"typical (average 35 plays)", []int{3, 10, 30, 100, 300}, []float64{0.40, 0.25, 0.20, 0.10, 0.05}},
		{"most quit fast (average 7 plays)", []int{3, 8, 30}, []float64{0.70, 0.20, 0.10}},
		{"hostile: everybody quits after 5 plays", []int{5}, []float64{1}},
		{"hostile: everybody quits after 1 play", []int{1}, []float64{1}},
	}
	type game struct {
		name  string
		pool  []Prize
		price float64
		picks int
	}
	games := []game{{"spin TIER_1", tiers["TIER_1"], prices["TIER_1"], 1}, {"spin TIER_3", tiers["TIER_3"], prices["TIER_3"], 1}, {"spin TIER_5", tiers["TIER_5"], prices["TIER_5"], 1}, {"chest", chest, chestCost / 3, 3}}
	var out strings.Builder
	worst := 1e9
	for _, c := range crowds {
		fmt.Fprintf(&out, "\n%s\n", c.name)
		for _, g := range games {
			fmt.Fprintf(&out, "  %-12s", g.name)
			for _, v := range []struct{ gate, guard float64 }{{0, 0}, {90, 0}, {90, 0.75}, {90, 0.9}} {
				const runs = 2
				var sum, wsum float64
				minP := 1e9
				for r := 0; r < runs; r++ {
					p, w := simPopulation(smartCfg{Enabled: true, WelcomePct: 150, TargetPct: 80, GatePct: v.gate}, v.guard, coinStars, g.pool, g.price, g.picks, c, 600, 600)
					sum += p
					wsum += w
					minP = minFloat(minP, p)
				}
				fmt.Fprintf(&out, " | gate %3.0f guard %.2f: %+6.1f%% (worst %+6.1f%%) welcome %4.1f%%", v.gate, v.guard, sum/runs, minP, wsum/runs)
				if v.gate == 90 && v.guard > 0 { // the real setup: the overall limit on the welcome plus the payout guard
					worst = minFloat(worst, minP)
				}
			}
			out.WriteString("\n")
		}
	}
	t.Log(out.String())
	t.Logf("worst single run with the limit at 90 and the guard on (0.75 or 0.9): %+.1f%% profit", worst)
	if worst < 8 {
		t.Fatalf("with the limit at 90 the project must stay in profit; worst run %.1f%%", worst)
	}
}

func minFloat(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}
