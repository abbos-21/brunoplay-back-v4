package games

import (
	"context"
	"math"
	"time"

	"github.com/jackc/pgx/v5"

	"brunoplay/internal/ads"
	"brunoplay/internal/apperr"
	"brunoplay/internal/db"
	"brunoplay/internal/gameday"
	"brunoplay/internal/ledger"
	"brunoplay/internal/settings"
)

const (
	GameRide     = "ride"
	counterRideC = "ride_coins"
	counterRideF = "ride_free"
)

type RideInfo struct {
	BestScore       int64 `json:"bestScore"`
	RunCostStars    int64 `json:"runCostStars"`
	CoinsLeftToday  int64 `json:"coinsLeftToday"`
	MaxCoinsPerDay  int64 `json:"maxCoinsPerDay"`
	FreeRunsLeft    int64 `json:"freeRunsLeft"` // runs an ad can still pay for today
	CanPayWithStars bool  `json:"canPayWithStars"`
	AdOffered       bool  `json:"adOffered"` // the player cannot pay with stars and an ad run is available
}

func (s *Service) freeLimit(ctx context.Context) int64 {
	v, err := s.settings.Int(ctx, settings.KeyAdsRideFree)
	if err != nil {
		return 0
	}
	return v
}

// RideInfo is the Ride menu's data.
func (s *Service) RideInfo(ctx context.Context, playerID int64) (*RideInfo, error) {
	cost, err := s.settings.Int(ctx, settings.KeyRideCost)
	if err != nil {
		return nil, err
	}
	max, err := s.settings.Int(ctx, settings.KeyRideDailyCoins)
	if err != nil {
		return nil, err
	}
	var best, stars int64
	if err := s.pool.QueryRow(ctx, `SELECT best_ride_score, stars FROM players WHERE id=$1`, playerID).Scan(&best, &stars); err != nil {
		return nil, err
	}
	now := s.now()
	usedCoins, _ := gameday.Get(ctx, s.pool, playerID, now, counterRideC)
	usedFree, _ := gameday.Get(ctx, s.pool, playerID, now, counterRideF)
	left := max - usedCoins
	if left < 0 {
		left = 0
	}
	freeLeft := s.freeLimit(ctx) - usedFree
	if freeLeft < 0 {
		freeLeft = 0
	}
	info := &RideInfo{BestScore: best, RunCostStars: cost, CoinsLeftToday: left, MaxCoinsPerDay: max, FreeRunsLeft: freeLeft, CanPayWithStars: stars >= cost}
	info.AdOffered = !info.CanPayWithStars && freeLeft > 0
	return info, nil
}

type RideRun struct {
	RunID     int64     `json:"runId"`
	SessionID string    `json:"sessionId"`
	StartTime time.Time `json:"startTime"`
	ExpiresAt time.Time `json:"expiresAt"`
	PaidWith  string    `json:"paidWith"`
	Stars     int64     `json:"stars"`
}

// StartRide begins a run. It is paid with stars; when the player has too few stars a watched ad can pay (daily limit).
// Any earlier unfinished run is abandoned (its star is not refunded).
func (s *Service) StartRide(ctx context.Context, playerID int64, adID string) (*RideRun, error) {
	cost, err := s.settings.Int(ctx, settings.KeyRideCost)
	if err != nil {
		return nil, err
	}
	if _, err := s.settings.Int(ctx, settings.KeyRideDailyCoins); err != nil {
		return nil, err // the daily coin cap must be configured before anybody pays for a run
	}
	maxSec, err := s.settings.Int(ctx, settings.KeyRideMaxSeconds)
	if err != nil {
		return nil, err
	}
	now := s.now()
	var run *RideRun
	err = db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		p, err := s.players.Lock(ctx, tx, playerID)
		if err != nil {
			return err
		}
		paid := "STARS"
		var adArg any
		switch {
		case cost == 0:
			paid = "FREE"
		case p.Stars >= cost:
			if _, err := ledger.Apply(ctx, tx, ledger.Entry{PlayerID: playerID, Currency: ledger.Star, Amount: -cost, Reason: ledger.ReasonRideCharge,
				RefType: "ride", RefID: "run"}); err != nil {
				return err
			}
			if err := s.recordSpend(ctx, tx, GameRide, "run", cost); err != nil {
				return err
			}
			if err := s.spent(ctx, tx, playerID, GameRide, cost); err != nil {
				return err
			}
		default:
			// not enough stars: an ad may pay for the run
			if adID == "" {
				return apperr.InsufficientStars
			}
			if _, ok, err := gameday.Take(ctx, tx, playerID, now, counterRideF, 1, s.freeLimit(ctx)); err != nil {
				return err
			} else if !ok {
				return apperr.AdLimit
			}
			if err := s.ads.Consume(ctx, tx, playerID, adID, ads.RideFree, "ride_free"); err != nil {
				return err
			}
			paid, adArg = "AD", adID
		}
		if _, err := tx.Exec(ctx, `UPDATE ride_runs SET status='ABANDONED' WHERE player_id=$1 AND status='STARTED'`, playerID); err != nil {
			return err
		}
		exp := now.Add(time.Duration(maxSec) * time.Second)
		var id int64
		if err := tx.QueryRow(ctx, `INSERT INTO ride_runs (player_id, status, paid_with, ad_view_id, started_at, expires_at) VALUES ($1,'STARTED',$2,$3,$4,$5) RETURNING id`,
			playerID, paid, adArg, now, exp).Scan(&id); err != nil {
			return err
		}
		_, stars, err := ledger.Balances(ctx, tx, playerID)
		if err != nil {
			return err
		}
		run = &RideRun{RunID: id, SessionID: ledger.RefID(id), StartTime: now, ExpiresAt: exp, PaidWith: paid, Stars: stars}
		return nil
	})
	return run, err
}

type RideFinish struct {
	CoinsEarned    int64 `json:"coinsEarned"`
	CoinsClaimed   int64 `json:"coinsClaimed"`
	NewBalance     int64 `json:"newBalance"`
	Score          int64 `json:"score"`
	BestScore      int64 `json:"bestScore"`
	NewBest        bool  `json:"newBest"`
	Capped         bool  `json:"capped"`
	CoinsLeftToday int64 `json:"coinsLeftToday"`
}

// FinishRide accepts the result of a run once, only if it is physically possible for the time that really passed on the server.
func (s *Service) FinishRide(ctx context.Context, playerID, runID, coins, score int64) (*RideFinish, error) {
	if coins < 0 || score < 0 {
		return nil, apperr.RideRunInvalid
	}
	capDay, err := s.settings.Int(ctx, settings.KeyRideDailyCoins)
	if err != nil {
		return nil, err
	}
	minSec, _ := s.settings.Int(ctx, settings.KeyRideMinSeconds)
	spawn, _ := s.settings.Int(ctx, settings.KeyRideSpawnDelay)
	maxCoinsSec, _ := s.settings.Float(ctx, settings.KeyRideMaxCoinsSec)
	maxPtsSec, _ := s.settings.Float(ctx, settings.KeyRideMaxPtsSec)
	now := s.now()
	var res *RideFinish
	var rejected error
	err = db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		p, err := s.players.Lock(ctx, tx, playerID)
		if err != nil {
			return err
		}
		var started, expires time.Time
		var status string
		if err := tx.QueryRow(ctx, `SELECT status, started_at, expires_at FROM ride_runs WHERE id=$1 AND player_id=$2 FOR UPDATE`, runID, playerID).Scan(&status, &started, &expires); err != nil {
			if db.IsNoRows(err) {
				return apperr.RideRunInvalid
			}
			return err
		}
		if status != "STARTED" || now.After(expires) {
			return apperr.RideRunInvalid
		}
		dur := now.Sub(started)
		reject := func(why string) error {
			if _, err := tx.Exec(ctx, `UPDATE ride_runs SET status='REJECTED', reject_reason=$2, finished_at=$3, duration_ms=$4, claimed_coins=$5, claimed_score=$6 WHERE id=$1`,
				runID, why, now, dur.Milliseconds(), coins, score); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE players SET risk_score = risk_score + 10, flags = flags || '["ride_implausible"]'::jsonb WHERE id=$1`, playerID); err != nil {
				return err
			}
			rejected = apperr.RideRunInvalid
			return nil // commit the rejection record
		}
		if dur < time.Duration(minSec)*time.Second && (coins > 0 || score > 0) {
			return reject("too short for the result")
		}
		playable := math.Max(0, dur.Seconds()-float64(spawn))
		if float64(coins) > math.Floor(playable*maxCoinsSec)+3 {
			return reject("too many coins for the time played")
		}
		if float64(score) > math.Floor(dur.Seconds()*maxPtsSec)+50 {
			return reject("score too high for the time played")
		}
		used, _ := gameday.Get(ctx, tx, playerID, now, counterRideC)
		room := capDay - used
		if room < 0 {
			room = 0
		}
		pay := coins
		if pay > room {
			pay = room
		}
		var bal int64 = p.Coins
		if pay > 0 {
			if _, _, err := gameday.Take(ctx, tx, playerID, now, counterRideC, pay, -1); err != nil {
				return err
			}
			bal, err = ledger.Apply(ctx, tx, ledger.Entry{PlayerID: playerID, Currency: ledger.Coin, Amount: pay, Reason: ledger.ReasonRideReward,
				RefType: "ride", RefID: ledger.RefID(runID), IdemKey: "ride:" + ledger.RefID(runID), Earned: true, Meta: map[string]any{"score": score, "claimed": coins}})
			if err != nil {
				return err
			}
		}
		best := p.BestRideScore
		newBest := score > best
		if newBest {
			best = score
			if _, err := tx.Exec(ctx, `UPDATE players SET best_ride_score=$2 WHERE id=$1`, playerID, score); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE ride_runs SET status='FINISHED', finished_at=$2, duration_ms=$3, claimed_coins=$4, claimed_score=$5, paid_coins=$6 WHERE id=$1`,
			runID, now, dur.Milliseconds(), coins, score, pay); err != nil {
			return err
		}
		for _, h := range s.OnRide {
			if err := h(ctx, tx, playerID, score, pay); err != nil {
				return err
			}
		}
		res = &RideFinish{CoinsEarned: pay, CoinsClaimed: coins, NewBalance: bal, Score: score, BestScore: best, NewBest: newBest, Capped: pay < coins, CoinsLeftToday: room - pay}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if rejected != nil {
		return nil, rejected
	}
	return res, nil
}

var _ = ledger.Coin
