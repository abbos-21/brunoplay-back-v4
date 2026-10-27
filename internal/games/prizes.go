// Package games holds the prize games (Fortune Spin, Mega Chest, Bruno's Ride), the gift collection and the payout guard.
// Every draw is made on the server with a cryptographically secure random number.
package games

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"brunoplay/internal/ads"
	"brunoplay/internal/apperr"
	"brunoplay/internal/db"
	"brunoplay/internal/players"
	"brunoplay/internal/settings"
)

const (
	GameSpin  = "spin"
	GameChest = "chest"

	TypeCoins = "coins"
	TypeStars = "stars"
	TypeGift  = "gift"
)

// Prize is one possible (or drawn) prize. For gifts Value is the gift's star value.
type Prize struct {
	Type   string  `json:"type"`
	Value  int64   `json:"value"`
	Gift   string  `json:"gift,omitempty"`
	Weight float64 `json:"weight,omitempty"`
	Label  string  `json:"label,omitempty"`
}

// Key identifies a prize in logs and reports ("gift:frog", "coins:500").
func (p Prize) Key() string {
	if p.Type == TypeGift {
		return "gift:" + p.Gift
	}
	return fmt.Sprintf("%s:%d", p.Type, p.Value)
}

// randFloat returns a uniform number in [0,1) from the system's secure generator.
func randFloat() float64 {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err) // the system generator is unavailable: refuse to hand out prizes
	}
	return float64(binary.BigEndian.Uint64(b[:])>>11) / (1 << 53)
}

// pick draws one prize by weight.
func pick(list []Prize) (Prize, error) {
	var total float64
	for _, p := range list {
		if p.Weight > 0 {
			total += p.Weight
		}
	}
	if total <= 0 {
		return Prize{}, apperr.ConfigMissing.With("key", "prize odds")
	}
	r := randFloat() * total
	for _, p := range list {
		if p.Weight <= 0 {
			continue
		}
		if r < p.Weight {
			return p, nil
		}
		r -= p.Weight
	}
	// floating point rounding: the last prize with weight
	for i := len(list) - 1; i >= 0; i-- {
		if list[i].Weight > 0 {
			return list[i], nil
		}
	}
	return Prize{}, apperr.ConfigMissing.With("key", "prize odds")
}

// ---- gifts config

type Gift struct {
	Key       string `json:"key"`
	NameEN    string `json:"name_en"`
	NameRU    string `json:"name_ru"`
	StarValue int64  `json:"star_value"`
}

func (g Gift) Name(l apperr.Lang) string {
	if l == apperr.RU && g.NameRU != "" {
		return g.NameRU
	}
	return g.NameEN
}

type Service struct {
	pool     *pgxpool.Pool
	settings *settings.Service
	players  *players.Store
	ads      *ads.Service
	Now      func() time.Time

	// OnGiftWon / OnWin let other modules (tournaments, bot) follow the games.
	OnSpend []func(ctx context.Context, tx db.Querier, playerID int64, game string, stars int64) error
	// OnRide runs inside the transaction of an accepted Ride run (tournaments: best score, coins).
	OnRide []func(ctx context.Context, tx pgx.Tx, playerID, score, coins int64) error
	// OnChestRound runs when a paid round starts (the bot reminds about unopened chests).
	OnChestRound []func(ctx context.Context, tx pgx.Tx, playerID, roundID int64) error
}

func New(pool *pgxpool.Pool, st *settings.Service, ps *players.Store, a *ads.Service) *Service {
	return &Service{pool: pool, settings: st, players: ps, ads: a, Now: time.Now}
}

func (s *Service) now() time.Time { return s.Now() }

func (s *Service) spent(ctx context.Context, tx db.Querier, playerID int64, game string, stars int64) error {
	for _, h := range s.OnSpend {
		if err := h(ctx, tx, playerID, game, stars); err != nil {
			return err
		}
	}
	return nil
}

// Gifts returns the configured gifts in the order of the settings.
func (s *Service) Gifts(ctx context.Context) ([]Gift, map[string]Gift, error) {
	list, err := settings.Get[[]Gift](ctx, s.settings, settings.KeyGifts)
	if err != nil {
		return nil, nil, err
	}
	m := map[string]Gift{}
	for _, g := range list {
		m[g.Key] = g
	}
	return list, m, nil
}

// ---- payout guard

// coinStars is what one coin counts for in the payout ratio.
func (s *Service) coinStars(ctx context.Context) float64 {
	v, err := s.settings.Float(ctx, settings.KeyGuardCoinValue)
	if err != nil {
		return 0
	}
	return v
}

func (s *Service) starValue(p Prize, coinStars float64) int64 {
	switch p.Type {
	case TypeCoins:
		return int64(math.Round(float64(p.Value) * coinStars))
	default:
		return p.Value
	}
}

func hourOf(t time.Time) time.Time { return t.UTC().Truncate(time.Hour) }

// recordSpend adds stars spent on a game tier to the rolling statistics.
func (s *Service) recordSpend(ctx context.Context, tx db.Querier, game, tier string, stars int64) error {
	_, err := tx.Exec(ctx, `INSERT INTO payout_stats (game, tier, hour, spent) VALUES ($1,$2,$3,$4)
		ON CONFLICT (game, tier, hour) DO UPDATE SET spent = payout_stats.spent + EXCLUDED.spent`, game, tier, hourOf(s.now()), stars)
	return err
}

func (s *Service) recordPaid(ctx context.Context, tx db.Querier, game, tier string, value int64) error {
	if value <= 0 {
		return nil
	}
	_, err := tx.Exec(ctx, `INSERT INTO payout_stats (game, tier, hour, paid) VALUES ($1,$2,$3,$4)
		ON CONFLICT (game, tier, hour) DO UPDATE SET paid = payout_stats.paid + EXCLUDED.paid`, game, tier, hourOf(s.now()), value)
	return err
}

// guard re-rolls a drawn prize toward smaller ones while the rolling payout ratio of the game tier would exceed the limit.
// It returns the prize to pay and whether it was changed. With the limit at 0 the guard is off.
func (s *Service) guard(ctx context.Context, tx db.Querier, playerID int64, game, tier string, drawn Prize, pool []Prize, remaining int) (Prize, bool, error) {
	max, err := s.settings.Float(ctx, settings.KeyGuardRatio)
	if err != nil || max <= 0 {
		return drawn, false, nil
	}
	hours, err := s.settings.Int(ctx, settings.KeyGuardWindowH)
	if err != nil {
		return drawn, false, nil
	}
	var spent, paid int64
	if err := tx.QueryRow(ctx, `SELECT COALESCE(sum(spent),0), COALESCE(sum(paid),0) FROM payout_stats WHERE game=$1 AND tier=$2 AND hour >= $3`,
		game, tier, hourOf(s.now()).Add(-time.Duration(hours)*time.Hour)).Scan(&spent, &paid); err != nil {
		return drawn, false, err
	}
	if spent <= 0 {
		return drawn, false, nil
	}
	cs := s.coinStars(ctx)
	cands := append([]Prize(nil), pool...)
	sort.SliceStable(cands, func(i, j int) bool { return s.starValue(cands[i], cs) > s.starValue(cands[j], cs) })
	// the draws still to come in this purchase pay at least the smallest prize: keep room for them
	var reserve int64
	if n := len(cands); n > 0 {
		reserve = int64(remaining) * s.starValue(cands[n-1], cs)
	}
	ratio := func(v int64) float64 { return float64(paid+v+reserve) / float64(spent) }
	dv := s.starValue(drawn, cs)
	if ratio(dv) <= max {
		return drawn, false, nil
	}
	chosen := drawn
	for _, c := range cands {
		cv := s.starValue(c, cs)
		if cv >= dv {
			continue
		}
		chosen = c
		if ratio(cv) <= max {
			break
		}
	}
	if chosen.Key() == drawn.Key() {
		return drawn, false, nil
	}
	cv := s.starValue(chosen, cs)
	_, err = tx.Exec(ctx, `INSERT INTO guard_events (player_id, game, tier, from_key, to_key, from_value, to_value, ratio_after) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
		playerID, game, tier, drawn.Key(), chosen.Key(), dv, cv, ratio(cv))
	return chosen, true, err
}

// GuardStats is the live payout ratio per game and tier (admin).
type GuardStat struct {
	Game  string  `json:"game"`
	Tier  string  `json:"tier"`
	Spent int64   `json:"spent"`
	Paid  int64   `json:"paid"`
	Ratio float64 `json:"ratio"`
}

func (s *Service) GuardStats(ctx context.Context) ([]GuardStat, error) {
	hours, err := s.settings.Int(ctx, settings.KeyGuardWindowH)
	if err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `SELECT game, tier, COALESCE(sum(spent),0), COALESCE(sum(paid),0) FROM payout_stats WHERE hour >= $1 GROUP BY game, tier ORDER BY game, tier`,
		hourOf(s.now()).Add(-time.Duration(hours)*time.Hour))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []GuardStat{}
	for rows.Next() {
		var g GuardStat
		if err := rows.Scan(&g.Game, &g.Tier, &g.Spent, &g.Paid); err != nil {
			return nil, err
		}
		if g.Spent > 0 {
			g.Ratio = float64(g.Paid) / float64(g.Spent)
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

type GuardEvent struct {
	ID        int64     `json:"id"`
	PlayerID  int64     `json:"playerId"`
	Game      string    `json:"game"`
	Tier      string    `json:"tier"`
	From      string    `json:"from"`
	To        string    `json:"to"`
	Ratio     float64   `json:"ratioAfter"`
	CreatedAt time.Time `json:"createdAt"`
}

func (s *Service) GuardEvents(ctx context.Context, beforeID int64, limit int) ([]GuardEvent, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.pool.Query(ctx, `SELECT id, player_id, game, tier, from_key, to_key, ratio_after::float8, created_at FROM guard_events WHERE ($1=0 OR id<$1) ORDER BY id DESC LIMIT $2`, beforeID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []GuardEvent{}
	for rows.Next() {
		var g GuardEvent
		if err := rows.Scan(&g.ID, &g.PlayerID, &g.Game, &g.Tier, &g.From, &g.To, &g.Ratio, &g.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// feed adds a win to the public winners list.
func (s *Service) feed(ctx context.Context, tx db.Querier, playerID int64, game string, p Prize, spent int64) error {
	var username string
	if err := tx.QueryRow(ctx, `SELECT username FROM players WHERE id=$1`, playerID).Scan(&username); err != nil {
		return err
	}
	min, _ := s.settings.Int(ctx, settings.KeyWinnersMin)
	// gifts, stars and coins are listed once they are worth the minimum in stars (a coin counts for guard.coin_value_stars)
	if s.starValue(p, s.coinStars(ctx)) < min {
		return nil
	}
	_, err := tx.Exec(ctx, `INSERT INTO winners_feed (player_id, username, game, prize_type, prize_value, gift_key, spent_stars, created_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
		playerID, username, game, p.Type, p.Value, p.Gift, spent, s.now())
	return err
}

type Win struct {
	ID         int64     `json:"id"`
	Username   string    `json:"username"`
	PrizeType  string    `json:"prizeType"`
	PrizeValue int64     `json:"prizeValue"`
	Gift       string    `json:"gift"`
	Game       string    `json:"game"`
	SpentStars int64     `json:"spentStars"`
	CreatedAt  time.Time `json:"createdAt"`
}

// Winners returns the recent gift wins, newest first.
func (s *Service) Winners(ctx context.Context, page, perPage int) ([]Win, int64, error) {
	if perPage <= 0 || perPage > 50 {
		perPage = 20
	}
	if page < 1 {
		page = 1
	}
	var total int64
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM (SELECT 1 FROM winners_feed ORDER BY id DESC LIMIT 1000) x`).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.pool.Query(ctx, `SELECT id, username, prize_type, prize_value, gift_key, game, spent_stars, created_at FROM winners_feed ORDER BY id DESC LIMIT $1 OFFSET $2`,
		perPage, (page-1)*perPage)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []Win{}
	for rows.Next() {
		var w Win
		if err := rows.Scan(&w.ID, &w.Username, &w.PrizeType, &w.PrizeValue, &w.Gift, &w.Game, &w.SpentStars, &w.CreatedAt); err != nil {
			return nil, 0, err
		}
		out = append(out, w)
	}
	return out, total, rows.Err()
}
