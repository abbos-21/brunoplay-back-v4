// Package tournaments runs timed leaderboards over one metric (stars spent, best Ride score, coins mined, friends invited).
// Joining costs an entry fee; scores come from the same events that write the ledger; the admin confirms the payout.
package tournaments

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"brunoplay/internal/apperr"
	"brunoplay/internal/db"
	"brunoplay/internal/games"
	"brunoplay/internal/ledger"
	"brunoplay/internal/players"
)

const (
	MetricStars   = "STARS_SPENT"
	MetricRide    = "RIDE_BEST"
	MetricCoins   = "COINS_MINED"
	MetricFriends = "FRIENDS_INVITED"
)

func ValidMetric(m string) bool {
	return m == MetricStars || m == MetricRide || m == MetricCoins || m == MetricFriends
}

type Prize struct {
	Place int    `json:"place" validate:"required,gte=1,lte=100"`
	Coins int64  `json:"coins" validate:"gte=0"`
	Stars int64  `json:"stars" validate:"gte=0"`
	Gift  string `json:"gift" validate:"max=40"`
}

type Service struct {
	pool    *pgxpool.Pool
	players *players.Store
	games   *games.Service
	Now     func() time.Time

	// OnPaid runs for every winner inside the payout transaction (bot message).
	OnPaid []func(ctx context.Context, tx pgx.Tx, playerID, tournamentID int64, place int, p Prize) error
}

func New(pool *pgxpool.Pool, ps *players.Store, g *games.Service) *Service {
	return &Service{pool: pool, players: ps, games: g, Now: time.Now}
}

func (s *Service) now() time.Time { return s.Now() }

type Tournament struct {
	ID         int64      `json:"id"`
	NameEN     string     `json:"nameEn,omitempty"`
	NameRU     string     `json:"nameRu,omitempty"`
	DescEN     string     `json:"descEn,omitempty"`
	DescRU     string     `json:"descRu,omitempty"`
	Name       string     `json:"name"`
	Desc       string     `json:"description"`
	Metric     string     `json:"metric"`
	StartsAt   time.Time  `json:"startAt"`
	EndsAt     time.Time  `json:"endAt"`
	EntryFee   int64      `json:"entryFee"`
	Prizes     []Prize    `json:"prizes"`
	Status     string     `json:"status"` // SCHEDULED | ACTIVE | ENDED | PAID | CANCELLED
	Cancelled  *time.Time `json:"cancelledAt,omitempty"`
	PaidAt     *time.Time `json:"paidAt,omitempty"`
	TimeLeftMs int64      `json:"timeRemainingMs"`
	// for the player
	Joined bool   `json:"joined"`
	Score  int64  `json:"score"`
	Rank   *int64 `json:"rank"`
}

const cols = `id, name_en, name_ru, desc_en, desc_ru, metric, starts_at, ends_at, entry_fee, prizes, cancelled_at, paid_at`

func (s *Service) scan(r pgx.Row, lang apperr.Lang, full bool) (*Tournament, error) {
	var t Tournament
	var prizes []byte
	if err := r.Scan(&t.ID, &t.NameEN, &t.NameRU, &t.DescEN, &t.DescRU, &t.Metric, &t.StartsAt, &t.EndsAt, &t.EntryFee, &prizes, &t.Cancelled, &t.PaidAt); err != nil {
		if db.IsNoRows(err) {
			return nil, apperr.NotFound
		}
		return nil, err
	}
	_ = json.Unmarshal(prizes, &t.Prizes)
	t.Name, t.Desc = t.NameEN, t.DescEN
	if lang == apperr.RU {
		t.Name, t.Desc = t.NameRU, t.DescRU
	}
	if !full {
		t.NameEN, t.NameRU, t.DescEN, t.DescRU = "", "", "", ""
	}
	now := s.now()
	switch {
	case t.Cancelled != nil:
		t.Status = "CANCELLED"
	case t.PaidAt != nil:
		t.Status = "PAID"
	case now.Before(t.StartsAt):
		t.Status = "SCHEDULED"
	case now.Before(t.EndsAt):
		t.Status = "ACTIVE"
		t.TimeLeftMs = t.EndsAt.Sub(now).Milliseconds()
	default:
		t.Status = "ENDED"
	}
	return &t, nil
}

// rankOf is the place of a score among the not-disqualified entries (earlier time wins a tie).
func (s *Service) rankOf(ctx context.Context, q db.Querier, tid int64, score int64, at time.Time) (int64, error) {
	var n int64
	err := q.QueryRow(ctx, `SELECT count(*) FROM tournament_entries WHERE tournament_id=$1 AND NOT disqualified AND (score > $2 OR (score = $2 AND score_at < $3))`, tid, score, at).Scan(&n)
	return n + 1, err
}

// List returns the tournaments a player should see: running, starting soon, and just ended ones that are not paid yet.
func (s *Service) List(ctx context.Context, p *players.Player) ([]Tournament, error) {
	now := s.now()
	rows, err := s.pool.Query(ctx, `SELECT `+cols+` FROM tournaments WHERE cancelled_at IS NULL AND (paid_at IS NULL OR paid_at > $1) AND starts_at < $2 ORDER BY starts_at`,
		now.Add(-48*time.Hour), now.Add(7*24*time.Hour))
	if err != nil {
		return nil, err
	}
	var ids []*Tournament
	for rows.Next() {
		t, err := s.scan(rows, p.Lang(), false)
		if err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, t)
	}
	rows.Close()
	out := make([]Tournament, 0, len(ids))
	for _, t := range ids {
		var score int64
		var at time.Time
		var dq bool
		err := s.pool.QueryRow(ctx, `SELECT score, score_at, disqualified FROM tournament_entries WHERE tournament_id=$1 AND player_id=$2`, t.ID, p.ID).Scan(&score, &at, &dq)
		if err == nil {
			t.Joined, t.Score = true, score
			if !dq {
				r, err := s.rankOf(ctx, s.pool, t.ID, score, at)
				if err != nil {
					return nil, err
				}
				t.Rank = &r
			}
		} else if !db.IsNoRows(err) {
			return nil, err
		}
		out = append(out, *t)
	}
	return out, nil
}

// Join charges the entry fee and enters the player (only before the tournament ends).
func (s *Service) Join(ctx context.Context, playerID, tid int64) error {
	now := s.now()
	return db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := s.players.Lock(ctx, tx, playerID); err != nil {
			return err
		}
		t, err := s.scan(tx.QueryRow(ctx, `SELECT `+cols+` FROM tournaments WHERE id=$1 FOR UPDATE`, tid), apperr.EN, false)
		if err != nil {
			return err
		}
		if t.Status != "ACTIVE" && t.Status != "SCHEDULED" {
			return apperr.TournamentClosed
		}
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM tournament_entries WHERE tournament_id=$1 AND player_id=$2)`, tid, playerID).Scan(&exists); err != nil {
			return err
		}
		if exists {
			return apperr.TournamentJoined
		}
		if t.EntryFee > 0 {
			if _, err := ledger.Apply(ctx, tx, ledger.Entry{PlayerID: playerID, Currency: ledger.Star, Amount: -t.EntryFee, Reason: ledger.ReasonTournamentFee,
				RefType: "tournament", RefID: ledger.RefID(tid), IdemKey: fmt.Sprintf("tfee:%d:%d", tid, playerID)}); err != nil {
				return err
			}
		}
		_, err = tx.Exec(ctx, `INSERT INTO tournament_entries (tournament_id, player_id, joined_at, score, score_at) VALUES ($1,$2,$3,0,$3)`, tid, playerID, now)
		return err
	})
}

// Bump moves the score of every running tournament of that metric the player has joined: mode "add" adds, "max" keeps the best.
func (s *Service) Bump(ctx context.Context, q db.Querier, playerID int64, metric string, value int64, mode string) error {
	if value <= 0 {
		return nil
	}
	now := s.now()
	var err error
	if mode == "max" {
		_, err = q.Exec(ctx, `UPDATE tournament_entries e SET score=$4, score_at=$3 FROM tournaments t
			WHERE e.player_id=$1 AND e.tournament_id=t.id AND t.metric=$2 AND t.cancelled_at IS NULL AND t.starts_at <= $3 AND t.ends_at > $3 AND NOT e.disqualified AND e.score < $4`,
			playerID, metric, now, value)
	} else {
		_, err = q.Exec(ctx, `UPDATE tournament_entries e SET score=e.score+$4, score_at=$3 FROM tournaments t
			WHERE e.player_id=$1 AND e.tournament_id=t.id AND t.metric=$2 AND t.cancelled_at IS NULL AND t.starts_at <= $3 AND t.ends_at > $3 AND NOT e.disqualified`,
			playerID, metric, now, value)
	}
	return err
}

// Hooks to register on the other modules.

func (s *Service) OnSpend(ctx context.Context, tx db.Querier, playerID int64, game string, stars int64) error {
	return s.Bump(ctx, tx, playerID, MetricStars, stars, "add")
}

func (s *Service) OnRide(ctx context.Context, tx pgx.Tx, playerID, score, coins int64) error {
	return s.Bump(ctx, tx, playerID, MetricRide, score, "max")
}

func (s *Service) OnCoinsMined(ctx context.Context, tx pgx.Tx, playerID, coins int64) error {
	return s.Bump(ctx, tx, playerID, MetricCoins, coins, "add")
}

func (s *Service) OnFriendCounted(ctx context.Context, tx pgx.Tx, inviterID int64) error {
	return s.Bump(ctx, tx, inviterID, MetricFriends, 1, "add")
}

// ---- leaderboard

type Row struct {
	Rank      int64  `json:"rank"`
	UserID    int64  `json:"userId"`
	Username  string `json:"username"`
	FirstName string `json:"firstName"`
	Score     int64  `json:"score"`
}

type Board struct {
	Tournament *Tournament `json:"tournament"`
	Entries    []Row       `json:"entries"`
	Total      int64       `json:"total"`
	UserRank   *int64      `json:"userRank"`
	UserScore  *int64      `json:"userScore"`
}

func (s *Service) Leaderboard(ctx context.Context, p *players.Player, tid int64, page int) (*Board, error) {
	t, err := s.scan(s.pool.QueryRow(ctx, `SELECT `+cols+` FROM tournaments WHERE id=$1`, tid), p.Lang(), false)
	if err != nil {
		return nil, err
	}
	if page < 1 {
		page = 1
	}
	const per = 20
	b := &Board{Tournament: t, Entries: []Row{}}
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM tournament_entries WHERE tournament_id=$1 AND NOT disqualified`, tid).Scan(&b.Total); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `SELECT e.player_id, p.username, p.first_name, e.score FROM tournament_entries e JOIN players p ON p.id=e.player_id
		WHERE e.tournament_id=$1 AND NOT e.disqualified ORDER BY e.score DESC, e.score_at ASC, e.player_id LIMIT $2 OFFSET $3`, tid, per, (page-1)*per)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	rank := int64((page-1)*per) + 1
	for rows.Next() {
		var r Row
		if err := rows.Scan(&r.UserID, &r.Username, &r.FirstName, &r.Score); err != nil {
			return nil, err
		}
		r.Rank = rank
		rank++
		b.Entries = append(b.Entries, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var sc int64
	var at time.Time
	var dq bool
	if err := s.pool.QueryRow(ctx, `SELECT score, score_at, disqualified FROM tournament_entries WHERE tournament_id=$1 AND player_id=$2`, tid, p.ID).Scan(&sc, &at, &dq); err == nil && !dq {
		r, err := s.rankOf(ctx, s.pool, tid, sc, at)
		if err != nil {
			return nil, err
		}
		b.UserRank, b.UserScore = &r, &sc
	}
	return b, nil
}

// ---- admin

type Input struct {
	NameEN   string    `json:"nameEn" validate:"required,max=120"`
	NameRU   string    `json:"nameRu" validate:"required,max=120"`
	DescEN   string    `json:"descEn" validate:"max=500"`
	DescRU   string    `json:"descRu" validate:"max=500"`
	Metric   string    `json:"metric" validate:"required"`
	StartsAt time.Time `json:"startAt" validate:"required"`
	EndsAt   time.Time `json:"endAt" validate:"required"`
	EntryFee int64     `json:"entryFee" validate:"gte=0"`
	Prizes   []Prize   `json:"prizes" validate:"required,min=1,max=100,dive"`
}

func (s *Service) validate(ctx context.Context, in *Input) error {
	if !ValidMetric(in.Metric) {
		return apperr.Validation.With("field", "metric")
	}
	if !in.EndsAt.After(in.StartsAt) {
		return apperr.Validation.With("field", "endAt")
	}
	seen := map[int]bool{}
	for _, p := range in.Prizes {
		if seen[p.Place] || (p.Coins == 0 && p.Stars == 0 && p.Gift == "") {
			return apperr.Validation.With("field", "prizes")
		}
		seen[p.Place] = true
		if p.Gift != "" {
			if _, m, err := s.games.Gifts(ctx); err == nil {
				if _, ok := m[p.Gift]; !ok {
					return apperr.Validation.With("field", "prizes.gift")
				}
			}
		}
	}
	return nil
}

func (s *Service) Create(ctx context.Context, in Input, adminID int64) (*Tournament, error) {
	if err := s.validate(ctx, &in); err != nil {
		return nil, err
	}
	pb, _ := json.Marshal(in.Prizes)
	return s.scan(s.pool.QueryRow(ctx, `INSERT INTO tournaments (name_en, name_ru, desc_en, desc_ru, metric, starts_at, ends_at, entry_fee, prizes, created_by, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) RETURNING `+cols, in.NameEN, in.NameRU, in.DescEN, in.DescRU, in.Metric, in.StartsAt, in.EndsAt, in.EntryFee, pb, adminID, s.now()), apperr.EN, true)
}

// Update changes a tournament. After it started only the texts and the prizes can change (never the metric, fee or dates).
func (s *Service) Update(ctx context.Context, id int64, in Input) (*Tournament, error) {
	if err := s.validate(ctx, &in); err != nil {
		return nil, err
	}
	cur, err := s.scan(s.pool.QueryRow(ctx, `SELECT `+cols+` FROM tournaments WHERE id=$1`, id), apperr.EN, true)
	if err != nil {
		return nil, err
	}
	if cur.Status == "PAID" || cur.Status == "CANCELLED" {
		return nil, apperr.BadRequest
	}
	if cur.Status != "SCHEDULED" && (in.Metric != cur.Metric || in.EntryFee != cur.EntryFee || !in.StartsAt.Equal(cur.StartsAt) || !in.EndsAt.Equal(cur.EndsAt)) {
		return nil, apperr.BadRequest.With("reason", "a started tournament keeps its metric, fee and dates")
	}
	pb, _ := json.Marshal(in.Prizes)
	return s.scan(s.pool.QueryRow(ctx, `UPDATE tournaments SET name_en=$2, name_ru=$3, desc_en=$4, desc_ru=$5, metric=$6, starts_at=$7, ends_at=$8, entry_fee=$9, prizes=$10 WHERE id=$1 RETURNING `+cols,
		id, in.NameEN, in.NameRU, in.DescEN, in.DescRU, in.Metric, in.StartsAt, in.EndsAt, in.EntryFee, pb), apperr.EN, true)
}

func (s *Service) AdminList(ctx context.Context) ([]Tournament, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+cols+` FROM tournaments ORDER BY starts_at DESC LIMIT 100`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Tournament{}
	for rows.Next() {
		t, err := s.scan(rows, apperr.EN, true)
		if err != nil {
			return nil, err
		}
		out = append(out, *t)
	}
	return out, rows.Err()
}

type ReviewRow struct {
	Rank         int64  `json:"rank"`
	PlayerID     int64  `json:"playerId"`
	Username     string `json:"username"`
	Score        int64  `json:"score"`
	Disqualified bool   `json:"disqualified"`
	Blocked      bool   `json:"blocked"`
	Risk         int    `json:"riskScore"`
	Flags        any    `json:"flags"`
}

// Review is the frozen leaderboard the admin checks before paying (shows disqualified and risky players too).
func (s *Service) Review(ctx context.Context, tid int64, limit int) ([]ReviewRow, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.pool.Query(ctx, `SELECT e.player_id, p.username, e.score, e.disqualified, p.is_blocked, p.risk_score, p.flags
		FROM tournament_entries e JOIN players p ON p.id=e.player_id WHERE e.tournament_id=$1
		ORDER BY e.disqualified, e.score DESC, e.score_at ASC, e.player_id LIMIT $2`, tid, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ReviewRow{}
	var rank int64
	for rows.Next() {
		var r ReviewRow
		if err := rows.Scan(&r.PlayerID, &r.Username, &r.Score, &r.Disqualified, &r.Blocked, &r.Risk, &r.Flags); err != nil {
			return nil, err
		}
		if !r.Disqualified {
			rank++
			r.Rank = rank
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Service) Disqualify(ctx context.Context, tid, playerID int64, on bool) error {
	t, err := s.scan(s.pool.QueryRow(ctx, `SELECT `+cols+` FROM tournaments WHERE id=$1`, tid), apperr.EN, false)
	if err != nil {
		return err
	}
	if t.Status == "PAID" || t.Status == "CANCELLED" {
		return apperr.BadRequest
	}
	ct, err := s.pool.Exec(ctx, `UPDATE tournament_entries SET disqualified=$3 WHERE tournament_id=$1 AND player_id=$2`, tid, playerID, on)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return apperr.NotFound
	}
	return nil
}

type PayoutRow struct {
	PlayerID int64  `json:"playerId"`
	Place    int    `json:"place"`
	Score    int64  `json:"score"`
	Coins    int64  `json:"coins"`
	Stars    int64  `json:"stars"`
	Gift     string `json:"gift"`
}

// Pay pays the prizes of an ended tournament once: places are decided among not-disqualified, not-blocked players.
func (s *Service) Pay(ctx context.Context, tid int64) ([]PayoutRow, error) {
	var out []PayoutRow
	err := db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		t, err := s.scan(tx.QueryRow(ctx, `SELECT `+cols+` FROM tournaments WHERE id=$1 FOR UPDATE`, tid), apperr.EN, false)
		if err != nil {
			return err
		}
		if t.Status != "ENDED" {
			return apperr.TournamentClosed
		}
		rows, err := tx.Query(ctx, `SELECT e.player_id, e.score FROM tournament_entries e JOIN players p ON p.id=e.player_id
			WHERE e.tournament_id=$1 AND NOT e.disqualified AND NOT p.is_blocked AND e.score > 0
			ORDER BY e.score DESC, e.score_at ASC, e.player_id LIMIT 100`, tid)
		if err != nil {
			return err
		}
		type w struct{ pid, score int64 }
		var winners []w
		for rows.Next() {
			var x w
			if err := rows.Scan(&x.pid, &x.score); err != nil {
				rows.Close()
				return err
			}
			winners = append(winners, x)
		}
		rows.Close()
		prizes := map[int]Prize{}
		for _, p := range t.Prizes {
			prizes[p.Place] = p
		}
		for i, wn := range winners {
			place := i + 1
			pr, ok := prizes[place]
			if !ok {
				continue
			}
			idem := fmt.Sprintf("tprize:%d:%d", tid, wn.pid)
			if pr.Coins > 0 {
				if _, err := ledger.Apply(ctx, tx, ledger.Entry{PlayerID: wn.pid, Currency: ledger.Coin, Amount: pr.Coins, Reason: ledger.ReasonTournamentPrize,
					RefType: "tournament", RefID: ledger.RefID(tid), IdemKey: idem + ":coin", Earned: true}); err != nil && err != ledger.ErrDuplicate {
					return err
				}
			}
			if pr.Stars > 0 {
				if _, err := ledger.Apply(ctx, tx, ledger.Entry{PlayerID: wn.pid, Currency: ledger.Star, Amount: pr.Stars, Reason: ledger.ReasonTournamentPrize,
					RefType: "tournament", RefID: ledger.RefID(tid), IdemKey: idem + ":star"}); err != nil && err != ledger.ErrDuplicate {
					return err
				}
			}
			if pr.Gift != "" {
				if _, err := s.games.GrantGift(ctx, tx, wn.pid, pr.Gift, "tournament", ledger.RefID(tid)); err != nil {
					return err
				}
			}
			if _, err := tx.Exec(ctx, `INSERT INTO tournament_payouts (tournament_id, player_id, place, score, coins, stars, gift, paid_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
				tid, wn.pid, place, wn.score, pr.Coins, pr.Stars, pr.Gift, s.now()); err != nil {
				return err
			}
			out = append(out, PayoutRow{PlayerID: wn.pid, Place: place, Score: wn.score, Coins: pr.Coins, Stars: pr.Stars, Gift: pr.Gift})
			for _, h := range s.OnPaid {
				if err := h(ctx, tx, wn.pid, tid, place, pr); err != nil {
					return err
				}
			}
		}
		_, err = tx.Exec(ctx, `UPDATE tournaments SET paid_at=$2 WHERE id=$1`, tid, s.now())
		return err
	})
	return out, err
}

// Cancel stops a tournament that was not paid and gives every entry fee back.
func (s *Service) Cancel(ctx context.Context, tid int64) (int, error) {
	n := 0
	err := db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		t, err := s.scan(tx.QueryRow(ctx, `SELECT `+cols+` FROM tournaments WHERE id=$1 FOR UPDATE`, tid), apperr.EN, false)
		if err != nil {
			return err
		}
		if t.Status == "PAID" || t.Status == "CANCELLED" {
			return apperr.BadRequest
		}
		if _, err := tx.Exec(ctx, `UPDATE tournaments SET cancelled_at=$2 WHERE id=$1`, tid, s.now()); err != nil {
			return err
		}
		if t.EntryFee == 0 {
			return nil
		}
		rows, err := tx.Query(ctx, `SELECT player_id FROM tournament_entries WHERE tournament_id=$1`, tid)
		if err != nil {
			return err
		}
		var ids []int64
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			ids = append(ids, id)
		}
		rows.Close()
		for _, id := range ids {
			if _, err := ledger.Apply(ctx, tx, ledger.Entry{PlayerID: id, Currency: ledger.Star, Amount: t.EntryFee, Reason: ledger.ReasonTournamentRefund,
				RefType: "tournament", RefID: ledger.RefID(tid), IdemKey: fmt.Sprintf("trefund:%d:%d", tid, id)}); err != nil && err != ledger.ErrDuplicate {
				return err
			}
			n++
		}
		return nil
	})
	return n, err
}

// Due lists tournaments the bot should announce (just started, or ending soon).
func (s *Service) Due(ctx context.Context, endingWithin time.Duration) (started, ending []int64, err error) {
	now := s.now()
	rows, err := s.pool.Query(ctx, `SELECT id FROM tournaments WHERE cancelled_at IS NULL AND start_notified_at IS NULL AND starts_at <= $1 AND ends_at > $1`, now)
	if err != nil {
		return nil, nil, err
	}
	for rows.Next() {
		var id int64
		_ = rows.Scan(&id)
		started = append(started, id)
	}
	rows.Close()
	rows, err = s.pool.Query(ctx, `SELECT id FROM tournaments WHERE cancelled_at IS NULL AND ending_notified_at IS NULL AND starts_at <= $1 AND ends_at > $1 AND ends_at <= $2`, now, now.Add(endingWithin))
	if err != nil {
		return nil, nil, err
	}
	for rows.Next() {
		var id int64
		_ = rows.Scan(&id)
		ending = append(ending, id)
	}
	rows.Close()
	return started, ending, nil
}

func (s *Service) MarkNotified(ctx context.Context, tid int64, kind string) error {
	col := "start_notified_at"
	if kind == "ending" {
		col = "ending_notified_at"
	}
	_, err := s.pool.Exec(ctx, `UPDATE tournaments SET `+col+`=$2 WHERE id=$1`, tid, s.now())
	return err
}

// Participants of a tournament (for the "ending soon" message).
func (s *Service) Participants(ctx context.Context, tid int64) ([]int64, error) {
	rows, err := s.pool.Query(ctx, `SELECT player_id FROM tournament_entries WHERE tournament_id=$1 AND NOT disqualified`, tid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// Get returns one tournament (admin).
func (s *Service) Get(ctx context.Context, id int64) (*Tournament, error) {
	return s.scan(s.pool.QueryRow(ctx, `SELECT `+cols+` FROM tournaments WHERE id=$1`, id), apperr.EN, true)
}

// Ranks returns every participant's current place (for the "ending soon" message).
func (s *Service) Ranks(ctx context.Context, tid int64) (map[int64]int64, error) {
	rows, err := s.pool.Query(ctx, `SELECT player_id, rank() OVER (ORDER BY score DESC, score_at ASC) FROM tournament_entries WHERE tournament_id=$1 AND NOT disqualified`, tid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]int64{}
	for rows.Next() {
		var id, r int64
		if err := rows.Scan(&id, &r); err != nil {
			return nil, err
		}
		out[id] = r
	}
	return out, rows.Err()
}
