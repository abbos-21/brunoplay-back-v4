package api_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"brunoplay/internal/admin"
	"brunoplay/internal/ledger"
)

func (e *env) tournament(content, metric string, fee int, startIn, length time.Duration, prizes []map[string]any) int {
	e.t.Helper()
	r := e.do("POST", "/api/admin/tournaments", content, map[string]any{
		"nameEn": "Cup " + metric, "nameRu": "Кубок " + metric, "descEn": "d", "descRu": "о", "metric": metric,
		"startAt": e.now.Add(startIn).Format(time.RFC3339), "endAt": e.now.Add(startIn + length).Format(time.RFC3339), "entryFee": fee, "prizes": prizes})
	if r.Status != 201 {
		e.t.Fatalf("create tournament: %d %s", r.Status, r.Body)
	}
	return int(r.JSON(e.t)["id"].(float64))
}

var cupPrizes = []map[string]any{
	{"place": 1, "coins": 1000, "stars": 5, "gift": "small"},
	{"place": 2, "coins": 500},
}

func (e *env) join(p player, id int) resp {
	return e.do("POST", fmt.Sprintf("/api/tournaments/%d/join", id), p.Token, nil)
}

func (e *env) score(p player, id int) (score float64, joined bool) {
	list := e.do("GET", "/api/tournaments", p.Token, nil).JSON(e.t)["tournaments"].([]any)
	for _, x := range list {
		m := x.(map[string]any)
		if int(m["id"].(float64)) == id {
			return m["score"].(float64), m["joined"].(bool)
		}
	}
	e.t.Fatalf("tournament %d is not listed", id)
	return
}

func TestTournamentCreateValidationAndRoles(t *testing.T) {
	e := newEnv(t)
	e.gamesConfig()
	content := e.adminLogin("content@example.com", admin.RoleContent)
	sup := e.adminLogin("sup@example.com", admin.RoleSupport)
	start := e.now.Add(time.Hour).Format(time.RFC3339)
	end := e.now.Add(2 * time.Hour).Format(time.RFC3339)
	base := func() map[string]any {
		return map[string]any{"nameEn": "A", "nameRu": "Б", "metric": "STARS_SPENT", "startAt": start, "endAt": end, "entryFee": 0, "prizes": cupPrizes}
	}
	with := func(k string, v any) map[string]any { b := base(); b[k] = v; return b }
	for name, body := range map[string]map[string]any{
		"unknown metric":  with("metric", "LUCK"),
		"ends first":      with("endAt", e.now.Format(time.RFC3339)),
		"duplicate place": with("prizes", []map[string]any{{"place": 1, "coins": 5}, {"place": 1, "coins": 6}}),
		"empty prize":     with("prizes", []map[string]any{{"place": 1}}),
		"unknown gift":    with("prizes", []map[string]any{{"place": 1, "gift": "dragon"}}),
		"no prizes":       with("prizes", []map[string]any{}),
	} {
		if r := e.do("POST", "/api/admin/tournaments", content, body); r.Status != 422 {
			t.Errorf("%s must be refused: %d %s", name, r.Status, r.Body)
		}
	}
	if r := e.do("POST", "/api/admin/tournaments", sup, base()); r.Status != 403 {
		t.Fatalf("support cannot create: %d", r.Status)
	}
	if r := e.do("POST", "/api/admin/tournaments", content, base()); r.Status != 201 {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
}

func TestTournamentJoinFeeAndWindow(t *testing.T) {
	e := newEnv(t)
	e.gamesConfig()
	content := e.adminLogin("content@example.com", admin.RoleContent)
	p := e.login(700, "en", "")
	id := e.tournament(content, "STARS_SPENT", 50, 0, 2*time.Hour, cupPrizes)

	if r := e.join(p, id); r.Code(t) != "INSUFFICIENT_STARS" {
		t.Fatalf("%s", r.Body)
	}
	if _, joined := e.score(p, id); joined {
		t.Fatal("a refused join leaves no entry")
	}
	e.give(p, ledger.Star, 200)
	if r := e.join(p, id); r.Status != 200 || e.stars(p) != 150 {
		t.Fatalf("join: %d %s stars=%d", r.Status, r.Body, e.stars(p))
	}
	if r := e.join(p, id); r.Code(t) != "TOURNAMENT_ALREADY_JOINED" || e.stars(p) != 150 {
		t.Fatalf("joining twice charges nothing: %s stars=%d", r.Body, e.stars(p))
	}
	e.now = e.now.Add(3 * time.Hour)
	q := e.login(701, "en", "")
	e.give(q, ledger.Star, 200)
	if r := e.join(q, id); r.Code(t) != "TOURNAMENT_NOT_OPEN" || e.stars(q) != 200 {
		t.Fatalf("an ended tournament cannot be joined: %s", r.Body)
	}
	// the list is in the player's language and shows the status
	l := e.do("GET", "/api/tournaments", p.Token, nil).JSON(t)["tournaments"].([]any)
	if len(l) != 1 || l[0].(map[string]any)["status"] != "ENDED" || l[0].(map[string]any)["name"] != "Cup STARS_SPENT" {
		t.Fatalf("%v", l)
	}
}

func TestTournamentMetricsCountOnlyAfterJoiningAndInsideTheWindow(t *testing.T) {
	e := newEnv(t)
	e.gamesConfig()
	content := e.adminLogin("content@example.com", admin.RoleContent)
	stars := e.tournament(content, "STARS_SPENT", 0, 0, 24*time.Hour, cupPrizes)
	ride := e.tournament(content, "RIDE_BEST", 0, 0, 24*time.Hour, cupPrizes)
	coins := e.tournament(content, "COINS_MINED", 0, 0, 24*time.Hour, cupPrizes)
	friends := e.tournament(content, "FRIENDS_INVITED", 0, 0, 24*time.Hour, cupPrizes)

	p := e.login(710, "en", "")
	e.give(p, ledger.Star, 100000)
	// stars spent before joining do not count
	e.spin(p, "TIER_1", 1) // 100 stars
	for _, id := range []int{stars, ride, coins, friends} {
		if r := e.join(p, id); r.Status != 200 {
			t.Fatalf("%d %s", r.Status, r.Body)
		}
	}
	if s, _ := e.score(p, stars); s != 0 {
		t.Fatalf("spending before joining does not count: %v", s)
	}
	e.spin(p, "TIER_1", 3)                         // 300 stars
	e.do("POST", "/api/chest/round", p.Token, nil) // 10 stars
	if s, _ := e.score(p, stars); s != 310 {
		t.Fatalf("stars spent in games: %v", s)
	}
	// Ride: the best score, never lowered
	run := e.ride(p)
	e.now = e.now.Add(60 * time.Second)
	e.finishRide(p, run, 10, 900)
	run = e.ride(p)
	e.now = e.now.Add(60 * time.Second)
	e.finishRide(p, run, 10, 400)
	if s, _ := e.score(p, ride); s != 900 {
		t.Fatalf("best ride score: %v", s)
	}
	// coins mined
	e.do("POST", "/api/fishing/start", p.Token, map[string]any{"adViewId": e.watchAd(p, "FISHING_START")})
	e.now = e.now.Add(time.Hour)
	e.do("POST", "/api/fishing/collect", p.Token, map[string]any{"adViewId": e.watchAd(p, "COLLECT")})
	if s, _ := e.score(p, coins); s != 5 {
		t.Fatalf("coins mined: %v", s)
	}
	// friends: a friend reaching level 2 counts for the inviter
	f := e.login(711, "en", fmt.Sprintf("ref_%d", p.ID))
	e.levelUp(f, 2)
	if s, _ := e.score(p, friends); s != 1 {
		t.Fatalf("friends invited: %v", s)
	}
	// a player who did not join has no score anywhere
	q := e.login(712, "en", "")
	e.give(q, ledger.Star, 1000)
	e.spin(q, "TIER_1", 1)
	if _, joined := e.score(q, stars); joined {
		t.Fatal("not joined")
	}
	// after the end nothing counts
	e.now = e.now.Add(48 * time.Hour)
	e.d.PlayerTokenTTL = 100000 * time.Hour
	e.spin(p, "TIER_1", 1)
	if s, _ := e.score(p, stars); s != 312 { // 310 + the two Ride runs
		t.Fatalf("after the end the score is frozen: %v", s)
	}
}

func TestTournamentLeaderboardRankingAndTies(t *testing.T) {
	e := newEnv(t)
	e.gamesConfig()
	content := e.adminLogin("content@example.com", admin.RoleContent)
	id := e.tournament(content, "STARS_SPENT", 0, 0, 24*time.Hour, cupPrizes)
	a, b, c := e.login(720, "en", ""), e.login(721, "en", ""), e.login(722, "en", "")
	for _, p := range []player{a, b, c} {
		e.give(p, ledger.Star, 10000)
		e.join(p, id)
	}
	e.spin(b, "TIER_1", 3) // 300 first
	e.now = e.now.Add(time.Minute)
	e.spin(a, "TIER_1", 3) // 300 later: same score, reached later
	e.now = e.now.Add(time.Minute)
	e.spin(c, "TIER_1", 5) // 500: first place
	lb := e.do("GET", fmt.Sprintf("/api/tournaments/%d/leaderboard", id), a.Token, nil).JSON(t)
	rows := lb["entries"].([]any)
	order := []string{rows[0].(map[string]any)["username"].(string), rows[1].(map[string]any)["username"].(string), rows[2].(map[string]any)["username"].(string)}
	if order[0] != "user722" || order[1] != "user721" || order[2] != "user720" {
		t.Fatalf("score first, then who got there first: %v", order)
	}
	if lb["userRank"].(float64) != 3 || lb["userScore"].(float64) != 300 || lb["total"].(float64) != 3 {
		t.Fatalf("own place: %v", lb)
	}
	// a player outside the tournament sees the board but has no rank
	o := e.login(723, "en", "")
	if lb := e.do("GET", fmt.Sprintf("/api/tournaments/%d/leaderboard", id), o.Token, nil).JSON(t); lb["userRank"] != nil {
		t.Fatalf("%v", lb)
	}
}

func TestTournamentReviewDisqualifyAndConfirmedPayout(t *testing.T) {
	e := newEnv(t)
	e.gamesConfig()
	fake := e.botEnv()
	content := e.adminLogin("content@example.com", admin.RoleContent)
	fin := e.adminLogin("fin@example.com", admin.RoleFinance)
	id := e.tournament(content, "STARS_SPENT", 20, 0, 3*time.Hour, cupPrizes)
	players := []player{e.login(730, "en", ""), e.login(731, "ru", ""), e.login(732, "en", ""), e.login(733, "en", "")}
	for _, p := range players {
		e.give(p, ledger.Star, 10000)
		e.reach(p)
		e.join(p, id)
	}
	spins := []int{10, 5, 3, 1} // 1000, 500, 300, 100 stars
	for i, p := range players {
		e.spin(p, "TIER_1", spins[i])
	}
	if r := e.do("POST", fmt.Sprintf("/api/admin/tournaments/%d/pay", id), fin, nil); r.Code(t) != "TOURNAMENT_NOT_OPEN" {
		t.Fatalf("a running tournament cannot be paid: %s", r.Body)
	}
	e.now = e.now.Add(4 * time.Hour)
	e.d.PlayerTokenTTL = 100000 * time.Hour
	content = e.adminRelogin("content@example.com")
	fin = e.adminRelogin("fin@example.com")

	// the leader is a cheater: the admin disqualifies them, everyone moves up
	if r := e.do("POST", fmt.Sprintf("/api/admin/tournaments/%d/disqualify", id), content, map[string]any{"playerId": players[0].ID, "on": true}); r.Status != 200 {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	rev := e.do("GET", fmt.Sprintf("/api/admin/tournaments/%d/review", id), content, nil).JSON(t)["entries"].([]any)
	if len(rev) != 4 || rev[0].(map[string]any)["playerId"].(float64) != float64(players[1].ID) || rev[3].(map[string]any)["disqualified"] != true {
		t.Fatalf("review: %v", rev)
	}
	// a blocked player does not win either
	e.exec(`UPDATE players SET is_blocked=TRUE WHERE id=$1`, players[1].ID)

	// only finance (and super) pay; content cannot
	if r := e.do("POST", fmt.Sprintf("/api/admin/tournaments/%d/pay", id), content, nil); r.Status != 403 {
		t.Fatalf("content must not pay: %d", r.Status)
	}
	r := e.do("POST", fmt.Sprintf("/api/admin/tournaments/%d/pay", id), fin, nil)
	if r.Status != 200 {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	pay := r.JSON(t)["payouts"].([]any)
	if len(pay) != 2 || pay[0].(map[string]any)["playerId"].(float64) != float64(players[2].ID) || pay[1].(map[string]any)["playerId"].(float64) != float64(players[3].ID) {
		t.Fatalf("places among the eligible: %v", pay)
	}
	if e.coins(players[2]) != 1000 || e.stars(players[2]) != 10000-20-300+5 || e.coins(players[3]) != 500 || e.coins(players[0]) != 0 || e.coins(players[1]) != 0 {
		t.Fatalf("prizes: %d %d %d", e.coins(players[2]), e.stars(players[2]), e.coins(players[3]))
	}
	if g := e.do("GET", "/api/gifts", players[2].Token, nil).JSON(t)["gifts"].([]any); len(g) < 1 {
		t.Fatal("the gift prize reaches the collection")
	}
	// paying twice is refused and pays nothing more
	if r := e.do("POST", fmt.Sprintf("/api/admin/tournaments/%d/pay", id), fin, nil); r.Status != 409 || e.coins(players[2]) != 1000 {
		t.Fatalf("second payout: %d coins=%d", r.Status, e.coins(players[2]))
	}
	// the winner hears about it
	e.sendDue()
	got := ""
	for _, m := range fake.MessagesTo(732) {
		got = m.Text
	}
	if got != "🏆 Tournament «Cup STARS_SPENT»: you took place 1! Prize: Reward: 1000 coins, 5 stars, gift small" {
		t.Fatalf("result message: %q", got)
	}
	for _, p := range players {
		if e.ledgerSum(p, "STAR") != e.stars(p) || e.ledgerSum(p, "COIN") != e.coins(p) {
			t.Fatalf("ledger must match the balance of %d", p.TG)
		}
	}
}

func TestTournamentCancelRefundsFees(t *testing.T) {
	e := newEnv(t)
	e.gamesConfig()
	content := e.adminLogin("content@example.com", admin.RoleContent)
	id := e.tournament(content, "COINS_MINED", 30, 0, 24*time.Hour, cupPrizes)
	a, b := e.login(740, "en", ""), e.login(741, "en", "")
	for _, p := range []player{a, b} {
		e.give(p, ledger.Star, 100)
		e.join(p, id)
	}
	r := e.do("POST", fmt.Sprintf("/api/admin/tournaments/%d/cancel", id), content, nil)
	if r.Status != 200 || r.JSON(t)["refunded"].(float64) != 2 || e.stars(a) != 100 || e.stars(b) != 100 {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	if r := e.do("POST", fmt.Sprintf("/api/admin/tournaments/%d/cancel", id), content, nil); r.Status != 400 {
		t.Fatalf("cancelling twice: %d", r.Status)
	}
	if e.stars(a) != 100 {
		t.Fatal("refunded once")
	}
	if r := e.join(a, id); r.Code(t) != "TOURNAMENT_NOT_OPEN" {
		t.Fatalf("a cancelled tournament is closed: %s", r.Body)
	}
}

func TestTournamentEditRulesAndAnnouncements(t *testing.T) {
	e := newEnv(t)
	e.gamesConfig()
	fake := e.botEnv()
	content := e.adminLogin("content@example.com", admin.RoleContent)
	a, b := e.login(750, "en", ""), e.login(751, "ru", "")
	e.reach(a)
	e.reach(b)
	e.give(a, ledger.Star, 1000)
	id := e.tournament(content, "STARS_SPENT", 10, time.Hour, 10*time.Hour, cupPrizes)
	body := func(fee int) map[string]any {
		return map[string]any{"nameEn": "New", "nameRu": "Новый", "metric": "STARS_SPENT", "startAt": e.now.Add(time.Hour).Format(time.RFC3339),
			"endAt": e.now.Add(11 * time.Hour).Format(time.RFC3339), "entryFee": fee, "prizes": cupPrizes}
	}
	if r := e.do("PUT", fmt.Sprintf("/api/admin/tournaments/%d", id), content, body(15)); r.Status != 200 {
		t.Fatalf("a scheduled tournament can change everything: %d %s", r.Status, r.Body)
	}
	e.now = e.now.Add(90 * time.Minute) // started
	e.d.PlayerTokenTTL = 100000 * time.Hour
	content = e.adminRelogin("content@example.com")
	if r := e.do("PUT", fmt.Sprintf("/api/admin/tournaments/%d", id), content, body(99)); r.Status != 400 {
		t.Fatalf("a started tournament keeps its fee: %d", r.Status)
	}
	// the start is announced once, to everybody who can be reached, in their language
	if err := e.d.AnnounceTournaments(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := e.d.AnnounceTournaments(context.Background()); err != nil {
		t.Fatal(err)
	}
	e.sendDue()
	if m := fake.MessagesTo(750); len(m) != 1 || m[0].Text != "🏁 Tournament «New» has started! Join and win prizes." {
		t.Fatalf("%v", m)
	}
	if m := fake.MessagesTo(751); len(m) != 1 || m[0].Text != "🏁 Турнир «Новый» начался! Участвуйте и выигрывайте призы." {
		t.Fatalf("%v", m)
	}
	// "ending soon" goes to participants only, with their place
	e.join(a, id)
	e.now = e.now.Add(6 * time.Hour) // 3.5 h before the end: inside the 6 h window
	_ = e.d.AnnounceTournaments(context.Background())
	e.sendDue()
	if m := fake.MessagesTo(750); len(m) != 2 || m[1].Text != "⏰ Tournament «New» ends soon. Your place: 1." {
		t.Fatalf("%v", m)
	}
	if len(fake.MessagesTo(751)) != 1 {
		t.Fatal("non-participants are not told that it ends soon")
	}
}
