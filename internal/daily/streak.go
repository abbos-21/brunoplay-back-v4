// Package daily holds the two daily activities: the login streak (a 10-day reward ladder that loops) and the daily combo.
package daily

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"brunoplay/internal/ads"
	"brunoplay/internal/apperr"
	"brunoplay/internal/db"
	"brunoplay/internal/gameday"
	"brunoplay/internal/ledger"
	"brunoplay/internal/players"
	"brunoplay/internal/settings"
)

const LadderDays = 10

type Service struct {
	pool     *pgxpool.Pool
	settings *settings.Service
	players  *players.Store
	ads      *ads.Service
	Now      func() time.Time

	// OnStreakClaimed / OnComboClaimed run inside the claim transaction (notifications, tournaments).
	OnStreakClaimed []func(ctx context.Context, tx pgx.Tx, playerID int64, streak int, coins int64) error
	OnComboClaimed  []func(ctx context.Context, tx pgx.Tx, playerID int64, coins int64) error
}

func New(pool *pgxpool.Pool, st *settings.Service, ps *players.Store, a *ads.Service) *Service {
	return &Service{pool: pool, settings: st, players: ps, ads: a, Now: time.Now}
}

func (s *Service) now() time.Time { return s.Now() }

func (s *Service) rewards(ctx context.Context) ([]int64, error) {
	r, err := settings.Get[[]int64](ctx, s.settings, settings.KeyStreakRewards)
	if err != nil {
		return nil, err
	}
	if len(r) != LadderDays {
		return nil, apperr.ConfigMissing.With("key", settings.KeyStreakRewards)
	}
	return r, nil
}

type DayReward struct {
	Day     int   `json:"day"`
	Coins   int64 `json:"coins"`
	Claimed bool  `json:"claimed"`
	IsToday bool  `json:"isToday"`
}

type StreakStatus struct {
	CurrentStreak   int         `json:"currentStreak"`
	BestStreak      int         `json:"bestStreak"`
	CanClaim        bool        `json:"canClaim"`
	TodayReward     int64       `json:"todayReward"`
	TodayDay        int         `json:"todayDay"`
	StreakRewards   []DayReward `json:"streakRewards"`
	ResetsAt        time.Time   `json:"resetsAt"`
	DoubleAvailable bool        `json:"doubleAvailable"`
}

type streakRow struct {
	Current, Best int
	Last          *time.Time
}

func (s *Service) loadStreak(ctx context.Context, q db.Querier, playerID int64, forUpdate bool) (streakRow, error) {
	var r streakRow
	lock := ""
	if forUpdate {
		lock = " FOR UPDATE"
	}
	err := q.QueryRow(ctx, `SELECT current, best, last_claim_day FROM daily_streaks WHERE player_id=$1`+lock, playerID).Scan(&r.Current, &r.Best, &r.Last)
	if db.IsNoRows(err) {
		return streakRow{}, nil
	}
	return r, err
}

// plan works out the ladder for `now`: whether today can be claimed, the streak it would make and the ladder day.
func plan(r streakRow, now time.Time) (alive, claimedToday bool, nextStreak, ladderDay, shownStreak, claimedCells int) {
	today := gameday.Of(now)
	if r.Last != nil {
		last := gameday.Of(*r.Last)
		switch {
		case last.Equal(today):
			alive, claimedToday = true, true
		case last.Equal(today.AddDate(0, 0, -1)):
			alive = true
		}
	}
	switch {
	case claimedToday:
		nextStreak = r.Current
	case alive:
		nextStreak = r.Current + 1
	default:
		nextStreak = 1
	}
	ladderDay = (nextStreak-1)%LadderDays + 1
	if alive {
		shownStreak = r.Current
		pos := (r.Current-1)%LadderDays + 1
		claimedCells = pos
		if !claimedToday && pos == LadderDays {
			claimedCells = 0 // a new round of the ladder starts today
		}
	}
	return
}

func (s *Service) Streak(ctx context.Context, playerID int64) (*StreakStatus, error) {
	rw, err := s.rewards(ctx)
	if err != nil {
		return nil, err
	}
	r, err := s.loadStreak(ctx, s.pool, playerID, false)
	if err != nil {
		return nil, err
	}
	now := s.now()
	_, claimedToday, _, day, shown, cells := plan(r, now)
	st := &StreakStatus{CurrentStreak: shown, BestStreak: r.Best, CanClaim: !claimedToday, TodayDay: day, TodayReward: rw[day-1],
		ResetsAt: gameday.NextReset(now), DoubleAvailable: !claimedToday}
	for d := 1; d <= LadderDays; d++ {
		cell := DayReward{Day: d, Coins: rw[d-1], Claimed: d <= cells}
		if claimedToday {
			cell.IsToday = d == cells
		} else {
			cell.IsToday = d == day
		}
		st.StreakRewards = append(st.StreakRewards, cell)
	}
	return st, nil
}

type ClaimResult struct {
	Reward     int64 `json:"reward"`
	Doubled    bool  `json:"doubled"`
	NewStreak  int   `json:"newStreak"`
	NewBalance int64 `json:"newBalance"`
	LadderDay  int   `json:"ladderDay"`
}

// ClaimStreak pays today's ladder reward (twice when a watched ad is given). One claim per game day.
func (s *Service) ClaimStreak(ctx context.Context, playerID int64, double bool, adID string) (*ClaimResult, error) {
	rw, err := s.rewards(ctx)
	if err != nil {
		return nil, err
	}
	now := s.now()
	var res *ClaimResult
	err = db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := s.players.Lock(ctx, tx, playerID); err != nil {
			return err
		}
		r, err := s.loadStreak(ctx, tx, playerID, true)
		if err != nil {
			return err
		}
		_, claimedToday, next, day, _, _ := plan(r, now)
		if claimedToday {
			return apperr.StreakClaimed
		}
		coins := rw[day-1]
		if double {
			if err := s.ads.Consume(ctx, tx, playerID, adID, ads.DoubleStreak, "double_streak"); err != nil {
				return err
			}
			coins *= 2
		}
		best := r.Best
		if next > best {
			best = next
		}
		if _, err := tx.Exec(ctx, `INSERT INTO daily_streaks (player_id, current, best, last_claim_day) VALUES ($1,$2,$3,$4)
			ON CONFLICT (player_id) DO UPDATE SET current=EXCLUDED.current, best=EXCLUDED.best, last_claim_day=EXCLUDED.last_claim_day`,
			playerID, next, best, gameday.Of(now)); err != nil {
			return err
		}
		var bal int64
		if coins > 0 {
			bal, err = ledger.Apply(ctx, tx, ledger.Entry{PlayerID: playerID, Currency: ledger.Coin, Amount: coins, Reason: ledger.ReasonDailyStreak,
				RefType: "streak", RefID: fmt.Sprint(next), IdemKey: fmt.Sprintf("streak:%d:%s", playerID, gameday.Key(now)), Earned: true,
				Meta: map[string]any{"ladderDay": day, "doubled": double}})
			if err == ledger.ErrDuplicate {
				return apperr.StreakClaimed
			}
			if err != nil {
				return err
			}
		} else {
			bal, _, err = ledger.Balances(ctx, tx, playerID)
			if err != nil {
				return err
			}
		}
		res = &ClaimResult{Reward: coins, Doubled: double, NewStreak: next, NewBalance: bal, LadderDay: day}
		for _, h := range s.OnStreakClaimed {
			if err := h(ctx, tx, playerID, next, coins); err != nil {
				return err
			}
		}
		return nil
	})
	return res, err
}

// StreakAtRisk lists players who have a streak of at least minStreak, have not claimed today, and may lose it at the reset
// (used by the bot's "don't lose your streak" message).
func (s *Service) StreakAtRisk(ctx context.Context, minStreak, limit int, afterID int64) ([]int64, error) {
	rows, err := s.pool.Query(ctx, `SELECT player_id FROM daily_streaks WHERE current >= $1 AND last_claim_day = $2 AND player_id > $3 ORDER BY player_id LIMIT $4`,
		minStreak, gameday.Of(s.now()).AddDate(0, 0, -1), afterID, limit)
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
