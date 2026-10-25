// Command loadtest drives a running BrunoPlay API with many simulated players and reports throughput and latency.
//
//	loadtest -url http://localhost:8080 -players 500 -concurrency 64 -duration 30s -admin you@example.com
//
// The target needs DEV_AUTH=true (players are created through /api/auth/dev), so run it against a staging copy, never production.
// With -admin (password in the LOADTEST_ADMIN_PASSWORD variable) the tool first sets the few settings a game needs to run
// (card tables, ads off) so a fresh staging database can be tested right away.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

type result struct {
	name   string
	d      time.Duration
	status int
	err    bool
}

func main() {
	base := flag.String("url", "http://localhost:8080", "API address")
	players := flag.Int("players", 200, "simulated players")
	conc := flag.Int("concurrency", 32, "parallel workers")
	dur := flag.Duration("duration", 20*time.Second, "test length")
	admin := flag.String("admin", "", "admin e-mail to push the minimal game settings (password in LOADTEST_ADMIN_PASSWORD)")
	flag.Parse()

	client := &http.Client{Timeout: 15 * time.Second, Transport: &http.Transport{MaxIdleConnsPerHost: *conc * 2, MaxConnsPerHost: *conc * 2}}
	call := func(method, path, token string, body any) (int, []byte, error) {
		var rd io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rd = bytes.NewReader(b)
		}
		req, _ := http.NewRequest(method, *base+path, rd)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		res, err := client.Do(req)
		if err != nil {
			return 0, nil, err
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, b, nil
	}

	if *admin != "" {
		if err := seed(call, *admin, os.Getenv("LOADTEST_ADMIN_PASSWORD")); err != nil {
			fmt.Fprintln(os.Stderr, "seeding the settings failed:", err)
			os.Exit(1)
		}
		fmt.Println("settings seeded")
	}

	fmt.Printf("creating %d players...\n", *players)
	tokens := make([]string, *players)
	var wg sync.WaitGroup
	sem := make(chan struct{}, *conc)
	var failed int64
	t0 := time.Now()
	for i := range tokens {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			st, b, err := call("POST", "/api/auth/dev", "", map[string]any{"telegramId": 9_000_000 + i, "firstName": fmt.Sprintf("Load%d", i), "language": "en"})
			if err != nil || st != 200 {
				atomic.AddInt64(&failed, 1)
				return
			}
			var r struct{ Token string }
			_ = json.Unmarshal(b, &r)
			tokens[i] = r.Token
		}(i)
	}
	wg.Wait()
	if failed > 0 {
		fmt.Printf("could not create %d players: is DEV_AUTH=true on the target?\n", failed)
		os.Exit(1)
	}
	fmt.Printf("players ready in %s\n", time.Since(t0).Round(time.Millisecond))

	type op struct {
		name   string
		weight int
		run    func(tok string) (int, error)
	}
	g := func(name, path string) func(string) (int, error) {
		return func(tok string) (int, error) { st, _, err := call("GET", path, tok, nil); return st, err }
	}
	ops := []op{
		{"GET /fishing", 45, g("", "/api/fishing")},
		{"GET /me", 15, g("", "/api/me")},
		{"GET /shop/cards", 10, g("", "/api/shop/cards")},
		{"GET /levels", 5, g("", "/api/levels")},
		{"POST /fishing/start", 10, func(tok string) (int, error) {
			st, _, err := call("POST", "/api/fishing/start", tok, map[string]any{})
			return st, err
		}},
		{"POST /fishing/collect", 10, func(tok string) (int, error) {
			st, _, err := call("POST", "/api/fishing/collect", tok, map[string]any{})
			return st, err
		}},
		{"PATCH /me/language", 5, func(tok string) (int, error) {
			st, _, err := call("PATCH", "/api/me/language", tok, map[string]any{"language": []string{"EN", "RU"}[rand.Intn(2)]})
			return st, err
		}},
	}
	total := 0
	for _, o := range ops {
		total += o.weight
	}

	results := make(chan result, 1<<16)
	stop := time.Now().Add(*dur)
	var done sync.WaitGroup
	for w := 0; w < *conc; w++ {
		done.Add(1)
		go func(seed int64) {
			defer done.Done()
			rnd := rand.New(rand.NewSource(seed))
			for time.Now().Before(stop) {
				pick := rnd.Intn(total)
				var o op
				for _, c := range ops {
					if pick < c.weight {
						o = c
						break
					}
					pick -= c.weight
				}
				tok := tokens[rnd.Intn(len(tokens))]
				s := time.Now()
				st, err := o.run(tok)
				results <- result{name: o.name, d: time.Since(s), status: st, err: err != nil}
			}
		}(int64(w) + 1)
	}
	go func() { done.Wait(); close(results) }()

	byOp := map[string][]time.Duration{}
	statuses := map[int]int{}
	var errs, serverErrs, n int
	var all []time.Duration
	for r := range results {
		n++
		all = append(all, r.d)
		byOp[r.name] = append(byOp[r.name], r.d)
		statuses[r.status]++
		if r.err {
			errs++
		} else if r.status >= 500 {
			serverErrs++
		}
	}
	elapsed := dur.Seconds()
	fmt.Printf("\n%d requests in %s: %.0f requests/second, %d network errors, %d server errors (5xx)\n", n, *dur, float64(n)/elapsed, errs, serverErrs)
	fmt.Println("status codes:", statuses, "(4xx on start/collect are normal game answers: already running, tank not ready, ad needed)")
	report := func(name string, ds []time.Duration) {
		sort.Slice(ds, func(i, j int) bool { return ds[i] < ds[j] })
		p := func(q float64) time.Duration { return ds[int(float64(len(ds)-1)*q)] }
		fmt.Printf("%-24s n=%-7d p50=%-8s p95=%-8s p99=%-8s max=%s\n", name, len(ds), p(.5).Round(10*time.Microsecond), p(.95).Round(10*time.Microsecond), p(.99).Round(10*time.Microsecond), ds[len(ds)-1].Round(10*time.Microsecond))
	}
	fmt.Println()
	names := make([]string, 0, len(byOp))
	for k := range byOp {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		report(k, byOp[k])
	}
	if len(all) > 0 {
		report("ALL", all)
	}
	if errs+serverErrs > 0 {
		os.Exit(2)
	}
}

// seed sets the settings a fresh staging database needs: card tables and no ads, so start and collect work at once.
func seed(call func(method, path, token string, body any) (int, []byte, error), email, password string) error {
	st, b, err := call("POST", "/api/admin/auth/login", "", map[string]any{"email": email, "password": password})
	if err != nil || st != 200 {
		return fmt.Errorf("admin sign-in failed (%d)", st)
	}
	var s struct{ AccessToken string }
	_ = json.Unmarshal(b, &s)
	var mining, capacity []map[string]any
	for l := 1; l <= 10; l++ {
		mining = append(mining, map[string]any{"level": l, "value": 100 * l, "cost": 10 * l, "currency": "COIN"})
		capacity = append(capacity, map[string]any{"level": l, "value": 10 * l, "cost": 10 * l, "currency": "COIN"})
	}
	for k, v := range map[string]any{"cards.mining.levels": mining, "cards.capacity.levels": capacity, "ads.required": false, "fishing.collect_min_percent": 1} {
		if st, _, err := call("PUT", "/api/admin/settings/"+k, s.AccessToken, map[string]any{"value": v}); err != nil || st != 200 {
			return fmt.Errorf("setting %s failed (%d)", k, st)
		}
	}
	return nil
}
