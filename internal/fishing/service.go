package fishing

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"brunoplay/internal/ads"
	"brunoplay/internal/apperr"
	"brunoplay/internal/db"
	"brunoplay/internal/gamecfg"
	"brunoplay/internal/ledger"
	"brunoplay/internal/players"
	"brunoplay/internal/settings"
)

const (
	BoostMining = "MINING_BOOST_1_5X"
	BoostAuto   = "AUTO_MINING"
)

// CollectEvent is passed to the hooks after coins were collected (referral share, tournaments, bot messages...).
type CollectEvent struct {
	PlayerID  int64
	SessionID int64
	Coins     int64
	Auto      bool
	At        time.Time
}

type Service struct {
	pool     *pgxpool.Pool
	settings *settings.Service
	cfg      gamecfg.Config
	ads      *ads.Service
	players  *players.Store
	Now      func() time.Time

	// OnCollected hooks run inside the collect transaction (an error rolls the whole collect back).
	OnCollected []func(ctx context.Context, tx pgx.Tx, ev CollectEvent) error
	// OnStateChanged is called after a session was started, collected or re-settled (the bot schedules "tank full" here).
	OnStateChanged []func(ctx context.Context, tx pgx.Tx, playerID int64, fullAt *time.Time, running bool) error
}

func NewService(pool *pgxpool.Pool, st *settings.Service, a *ads.Service, ps *players.Store) *Service {
	return &Service{pool: pool, settings: st, cfg: gamecfg.Config{S: st}, ads: a, players: ps, Now: time.Now}
}

func (s *Service) now() time.Time { return s.Now() }

type session struct {
	ID, PlayerID int64
	StartedAt    time.Time
	SettledMicro int64
	SettledAt    time.Time
	BaseRate     int64
	Capacity     int64
	FullAt       *time.Time
	StartAd      *string
	CollectAd    *string
	Status       string
	Collected    *int64
	Auto         bool
}

const sessCols = `id, player_id, status, started_at, settled_micro, settled_at, base_rate_micro, capacity_micro, full_at,
	start_ad_id, collect_ad_id, collected_coins, autocollected`

func scanSession(r pgx.Row) (*session, error) {
	var x session
	err := r.Scan(&x.ID, &x.PlayerID, &x.Status, &x.StartedAt, &x.SettledMicro, &x.SettledAt, &x.BaseRate, &x.Capacity, &x.FullAt,
		&x.StartAd, &x.CollectAd, &x.Collected, &x.Auto)
	return &x, err
}

func (s *Service) running(ctx context.Context, q db.Querier, playerID int64, forUpdate bool) (*session, error) {
	lock := ""
	if forUpdate {
		lock = " FOR UPDATE"
	}
	x, err := scanSession(q.QueryRow(ctx, `SELECT `+sessCols+` FROM fishing_sessions WHERE player_id=$1 AND status='RUNNING'`+lock, playerID))
	if db.IsNoRows(err) {
		return nil, nil
	}
	return x, err
}

// windows returns the mining-booster periods that end after `from`.
func windows(ctx context.Context, q db.Querier, playerID int64, from time.Time) ([]Window, error) {
	rows, err := q.Query(ctx, `SELECT starts_at, expires_at FROM player_boosters WHERE player_id=$1 AND type=$2 AND expires_at > $3 ORDER BY starts_at`,
		playerID, BoostMining, from)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Window
	for rows.Next() {
		var w Window
		if err := rows.Scan(&w.From, &w.To); err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

func (s *Service) multMilli(ctx context.Context) int64 {
	m, err := s.settings.Float(ctx, settings.KeyBoosterMulti)
	if err != nil {
		return 1500
	}
	return MultMilliOf(m)
}

func (s *Service) params(ctx context.Context, q db.Querier, x *session) (Params, error) {
	ws, err := windows(ctx, q, x.PlayerID, x.SettledAt)
	if err != nil {
		return Params{}, err
	}
	return Params{SettledMicro: x.SettledMicro, SettledAt: x.SettledAt, BaseRate: x.BaseRate, Capacity: x.Capacity,
		MultMilli: s.multMilli(ctx), Windows: ws}, nil
}

// Stats returns the player's mining speed and tank size (micro units) from the card tables and levels.
func (s *Service) Stats(ctx context.Context, miningLevel, capacityLevel int) (rate, capacity int64, err error) {
	mt, err := s.cfg.Card(ctx, gamecfg.CardMining)
	if err != nil {
		return 0, 0, err
	}
	ct, err := s.cfg.Card(ctx, gamecfg.CardCapacity)
	if err != nil {
		return 0, 0, err
	}
	return MicroOf(gamecfg.ValueAt(mt, miningLevel)), MicroOf(gamecfg.ValueAt(ct, capacityLevel)), nil
}

// ---- API state

type RateChangeView struct {
	At   time.Time `json:"at"`
	Rate float64   `json:"ratePerSecond"`
}

// State is what the app animates locally (ToR A.1): a snapshot with everything needed to move the tank forward.
type State struct {
	IsFishing           bool            `json:"isFishing"`
	FishingStartedAt    *time.Time      `json:"fishingStartedAt"`
	TankCoins           float64         `json:"tankCoins"`
	TankCapacity        float64         `json:"tankCapacity"`
	MiningRate          float64         `json:"miningRate"`
	EffectiveMiningRate float64         `json:"effectiveMiningRate"`
	IsComplete          bool            `json:"isComplete"`
	ElapsedSeconds      int64           `json:"elapsedSeconds"`
	FullAt              *time.Time      `json:"fullAt"`
	ServerTime          time.Time       `json:"serverTime"`
	CanCollect          bool            `json:"canCollect"`
	CollectMinPercent   int64           `json:"collectMinPercent"`
	CollectMinCoins     float64         `json:"collectMinCoins"`
	NextRateChange      *RateChangeView `json:"nextRateChange"`
	AdsRequired         bool            `json:"adsRequired"`
	// AutoCollect: the Autocollect booster is active, so a full tank is collected and restarted by the server on its own
	AutoCollect bool `json:"autoCollect"`
}

func micro(v int64) float64 { return float64(v) / float64(Micro) }

func (s *Service) minMicro(ctx context.Context, capacity int64) (pct int64, need int64, err error) {
	pct, err = s.settings.Int(ctx, settings.KeyCollectMinPct)
	if err != nil {
		return 0, 0, err
	}
	need = mulDivCeil(capacity, pct, 100)
	if need < Micro { // at least one whole coin must be collectable
		need = Micro
	}
	return pct, need, nil
}

func mulDivCeil(a, b, c int64) int64 { return ceilDiv(a, b, c) }

// State computes the current state of a player's fishing without writing anything.
func (s *Service) State(ctx context.Context, q db.Querier, p *players.Player) (*State, error) {
	now := s.now()
	rate, capacity, err := s.Stats(ctx, p.MiningLevel, p.CapacityLevel)
	if err != nil {
		return nil, err
	}
	st := &State{ServerTime: now, MiningRate: micro(rate), TankCapacity: micro(capacity), EffectiveMiningRate: micro(rate), AdsRequired: s.ads.Required(ctx) && !s.ads.PlayerAdsOff(ctx, q, p.ID)}
	if err := q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM player_boosters WHERE player_id=$1 AND type=$2 AND starts_at <= $3 AND expires_at > $3)`,
		p.ID, BoostAuto, now).Scan(&st.AutoCollect); err != nil {
		return nil, err
	}
	x, err := s.running(ctx, q, p.ID, false)
	if err != nil {
		return nil, err
	}
	pct, need, err := s.minMicro(ctx, capacity)
	if err != nil {
		return nil, err
	}
	st.CollectMinPercent = pct
	st.CollectMinCoins = micro(need)
	if x == nil {
		return st, nil
	}
	pr, err := s.params(ctx, q, x)
	if err != nil {
		return nil, err
	}
	res := Tank(pr, now)
	st.IsFishing = true
	started := x.StartedAt
	st.FishingStartedAt = &started
	st.TankCoins = micro(res.Amount)
	st.TankCapacity = micro(x.Capacity)
	st.MiningRate = micro(x.BaseRate)
	st.EffectiveMiningRate = micro(EffectiveRate(pr, now))
	st.IsComplete = res.Full
	st.ElapsedSeconds = int64(now.Sub(x.StartedAt).Seconds())
	if x.FullAt != nil {
		st.FullAt = x.FullAt
	} else if res.FullAt != nil {
		st.FullAt = res.FullAt
	}
	pctNeed := mulDivCeil(x.Capacity, pct, 100)
	if pctNeed < Micro {
		pctNeed = Micro
	}
	st.CollectMinCoins = micro(pctNeed)
	st.CanCollect = res.Amount >= pctNeed
	if rc := NextRateChange(pr, now); rc != nil {
		st.NextRateChange = &RateChangeView{At: rc.At, Rate: micro(rc.Rate)}
	}
	return st, nil
}

func minOf(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

// StateFor loads the player and returns the state.
func (s *Service) StateFor(ctx context.Context, playerID int64) (*State, error) {
	p, err := s.players.Get(ctx, s.pool, playerID)
	if err != nil {
		return nil, err
	}
	return s.State(ctx, s.pool, p)
}

// ---- start

func (s *Service) newSessionTx(ctx context.Context, tx pgx.Tx, p *players.Player, now time.Time, adID string) (*session, error) {
	rate, capacity, err := s.Stats(ctx, p.MiningLevel, p.CapacityLevel)
	if err != nil {
		return nil, err
	}
	ws, err := windows(ctx, tx, p.ID, now)
	if err != nil {
		return nil, err
	}
	pr := Params{SettledAt: now, BaseRate: rate, Capacity: capacity, MultMilli: s.multMilli(ctx), Windows: ws}
	full := FullAt(pr)
	var adArg any
	if adID != "" {
		adArg = adID
	}
	x, err := scanSession(tx.QueryRow(ctx, `INSERT INTO fishing_sessions (player_id, status, started_at, settled_micro, settled_at, base_rate_micro, capacity_micro, full_at, start_ad_id)
		VALUES ($1,'RUNNING',$2,0,$2,$3,$4,$5,$6) RETURNING `+sessCols, p.ID, now, rate, capacity, full, adArg))
	if err != nil {
		return nil, err
	}
	for _, h := range s.OnStateChanged {
		if err := h(ctx, tx, p.ID, full, true); err != nil {
			return nil, err
		}
	}
	return x, nil
}

// Start begins fishing. It needs a completed ad proof (unless ads are switched off).
func (s *Service) Start(ctx context.Context, playerID int64, adID string) (*State, error) {
	now := s.now()
	err := db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		p, err := s.players.Lock(ctx, tx, playerID)
		if err != nil {
			return err
		}
		cur, err := s.running(ctx, tx, playerID, true)
		if err != nil {
			return err
		}
		if cur != nil {
			if adID != "" && cur.StartAd != nil && *cur.StartAd == adID {
				return nil // the same request again: already started
			}
			return apperr.SessionRunning
		}
		if err := s.ads.Gate(ctx, tx, playerID, adID, ads.FishingStart, "fishing_start"); err != nil {
			return err
		}
		_, err = s.newSessionTx(ctx, tx, p, now, adID)
		return err
	})
	if err != nil {
		return nil, err
	}
	return s.StateFor(ctx, playerID)
}

// ---- collect

type CollectResult struct {
	CoinsCollected int64 `json:"coinsCollected"`
	NewBalance     int64 `json:"newBalance"`
	AutoRestarted  bool  `json:"autoRestarted"`
}

// Collect moves the tank to the balance and ends the session. It needs a completed ad proof and a tank of at least the minimum percentage.
func (s *Service) Collect(ctx context.Context, playerID int64, adID string) (*CollectResult, error) {
	now := s.now()
	var out *CollectResult
	err := db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		p, err := s.players.Lock(ctx, tx, playerID)
		if err != nil {
			return err
		}
		x, err := s.running(ctx, tx, playerID, true)
		if err != nil {
			return err
		}
		if x == nil {
			// a retried request of an already finished collect gets the same answer
			if adID != "" {
				prev, perr := scanSession(tx.QueryRow(ctx, `SELECT `+sessCols+` FROM fishing_sessions WHERE player_id=$1 AND collect_ad_id=$2`, playerID, adID))
				if perr == nil && prev.Collected != nil {
					out = &CollectResult{CoinsCollected: *prev.Collected, NewBalance: p.Coins}
					return nil
				}
			}
			return apperr.SessionNotRun
		}
		pr, err := s.params(ctx, tx, x)
		if err != nil {
			return err
		}
		res := Tank(pr, now)
		pct, _, err := s.minMicro(ctx, x.Capacity)
		if err != nil {
			return err
		}
		need := mulDivCeil(x.Capacity, pct, 100)
		if need < Micro {
			need = Micro
		}
		if res.Amount < need {
			return apperr.TankBelowMin.With("percent", pct)
		}
		if err := s.ads.Gate(ctx, tx, playerID, adID, ads.Collect, "fishing_collect"); err != nil {
			return err
		}
		coins := CoinsOf(res.Amount)
		bal, err := s.creditSession(ctx, tx, x, coins, now, adID, false, ledger.ReasonMining)
		if err != nil {
			return err
		}
		out = &CollectResult{CoinsCollected: coins, NewBalance: bal}
		return nil
	})
	return out, err
}

// creditSession credits the coins, marks the session collected and runs the hooks.
func (s *Service) creditSession(ctx context.Context, tx pgx.Tx, x *session, coins int64, now time.Time, adID string, auto bool, reason string) (int64, error) {
	var bal int64
	var err error
	if coins > 0 {
		bal, err = ledger.Apply(ctx, tx, ledger.Entry{PlayerID: x.PlayerID, Currency: ledger.Coin, Amount: coins, Reason: reason,
			RefType: "fishing_session", RefID: ledger.RefID(x.ID), Earned: true})
		if err != nil {
			return 0, err
		}
	} else {
		bal, _, err = ledger.Balances(ctx, tx, x.PlayerID)
		if err != nil {
			return 0, err
		}
	}
	var adArg any
	if adID != "" {
		adArg = adID
	}
	if _, err := tx.Exec(ctx, `UPDATE fishing_sessions SET status='COLLECTED', collected_coins=$2, collected_at=$3, collect_ad_id=$4, autocollected=$5 WHERE id=$1`,
		x.ID, coins, now, adArg, auto); err != nil {
		return 0, err
	}
	ev := CollectEvent{PlayerID: x.PlayerID, SessionID: x.ID, Coins: coins, Auto: auto, At: now}
	for _, h := range s.OnCollected {
		if err := h(ctx, tx, ev); err != nil {
			return 0, err
		}
	}
	for _, h := range s.OnStateChanged {
		if err := h(ctx, tx, x.PlayerID, nil, false); err != nil {
			return 0, err
		}
	}
	return bal, nil
}

// ---- changes of speed or capacity (card upgrade, booster purchase)

// Resettle takes a snapshot of a running session at `now` and re-bases it on the player's current levels and boosters.
// The amount already mined is kept (capped to the new capacity). Call it inside the transaction that changed the levels or boosters.
func (s *Service) Resettle(ctx context.Context, tx pgx.Tx, p *players.Player, now time.Time) error {
	x, err := s.running(ctx, tx, p.ID, true)
	if err != nil || x == nil {
		return err
	}
	old, err := s.params(ctx, tx, x)
	if err != nil {
		return err
	}
	res := Tank(old, now)
	rate, capacity, err := s.Stats(ctx, p.MiningLevel, p.CapacityLevel)
	if err != nil {
		return err
	}
	amount := minOf(res.Amount, capacity)
	ws, err := windows(ctx, tx, p.ID, now)
	if err != nil {
		return err
	}
	np := Params{SettledMicro: amount, SettledAt: now, BaseRate: rate, Capacity: capacity, MultMilli: s.multMilli(ctx), Windows: ws}
	full := FullAt(np)
	if amount >= capacity && x.FullAt != nil && x.FullAt.Before(now) {
		full = x.FullAt // it was already full: keep the true moment
	}
	if _, err := tx.Exec(ctx, `UPDATE fishing_sessions SET settled_micro=$2, settled_at=$3, base_rate_micro=$4, capacity_micro=$5, full_at=$6 WHERE id=$1`,
		x.ID, amount, now, rate, capacity, full); err != nil {
		return err
	}
	for _, h := range s.OnStateChanged {
		if err := h(ctx, tx, p.ID, full, true); err != nil {
			return err
		}
	}
	return nil
}

// ---- Autocollect

// RunAutocollect collects the full tanks of players with an active Autocollect booster and starts their next session.
// It is safe to run in several processes: every session is handled in its own transaction under row locks.
func (s *Service) RunAutocollect(ctx context.Context, batch int) (int, error) {
	now := s.now()
	rows, err := s.pool.Query(ctx, `SELECT s.player_id FROM fishing_sessions s
		JOIN players p ON p.id = s.player_id AND NOT p.is_blocked
		WHERE s.status='RUNNING' AND s.full_at IS NOT NULL AND s.full_at <= $1
		AND EXISTS (SELECT 1 FROM player_boosters b WHERE b.player_id=s.player_id AND b.type=$2 AND b.starts_at <= $1 AND b.expires_at > $1)
		ORDER BY s.full_at LIMIT $3`, now, BoostAuto, batch)
	if err != nil {
		return 0, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	done := 0
	for _, id := range ids {
		ok, err := s.drainAuto(ctx, id)
		if err != nil {
			slog.Error("autocollect failed", "player", id, "err", err)
			continue
		}
		if ok {
			done++
		}
	}
	return done, nil
}

// maxAutoCycles bounds the catch-up of one visit (a player away for a very long time, or the job stopped for a while).
const maxAutoCycles = 100

// SettleAuto collects the full tank of a player whose Autocollect booster is active, at once. The background job does the
// same every few seconds for players who are not looking; when the app asks for the state it is done first, so the player
// sees the coins arrive and the tank start over at the moment it fills.
func (s *Service) SettleAuto(ctx context.Context, playerID int64) error {
	now := s.now()
	var due bool
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM fishing_sessions x
		WHERE x.player_id=$1 AND x.status='RUNNING' AND x.full_at IS NOT NULL AND x.full_at <= $2
		AND EXISTS (SELECT 1 FROM player_boosters b WHERE b.player_id=$1 AND b.type=$3 AND b.starts_at <= $2 AND b.expires_at > $2))`,
		playerID, now, BoostAuto).Scan(&due); err != nil || !due {
		return err
	}
	_, err := s.drainAuto(ctx, playerID)
	return err
}

// drainAuto repeats autocollectOne until every missed tank is collected (a visit pays all of them at once, not a slice per
// request), within a time budget so a huge backlog cannot hold a request for long; the job continues what is left.
func (s *Service) drainAuto(ctx context.Context, playerID int64) (bool, error) {
	deadline := time.Now().Add(4 * time.Second)
	any := false
	for {
		n, err := s.autocollectOne(ctx, playerID, s.now())
		if n > 0 {
			any = true
		}
		if err != nil || n < maxAutoCycles || time.Now().After(deadline) {
			return any, err
		}
	}
}

func (s *Service) autocollectOne(ctx context.Context, playerID int64, now time.Time) (int, error) {
	did := 0
	err := db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		did = 0
		p, err := s.players.Lock(ctx, tx, playerID)
		if err != nil {
			return err
		}
		if p.IsBlocked {
			return nil
		}
		// The booster that is active right now: tanks only count from the moment it started.
		var boostFrom time.Time
		if err := tx.QueryRow(ctx, `SELECT starts_at FROM player_boosters WHERE player_id=$1 AND type=$2 AND starts_at <= $3 AND expires_at > $3
			ORDER BY expires_at DESC LIMIT 1`, playerID, BoostAuto, now).Scan(&boostFrom); err != nil {
			if db.IsNoRows(err) {
				return nil
			}
			return err
		}
		// Several tanks may have filled since the last visit: collect each one. The next tank starts exactly when the previous
		// one filled (never before the booster began), so no mining time is lost between two collections.
		for cycle := 0; cycle < maxAutoCycles; cycle++ {
			x, err := s.running(ctx, tx, playerID, true)
			if err != nil || x == nil {
				return err
			}
			pr, err := s.params(ctx, tx, x)
			if err != nil {
				return err
			}
			res := Tank(pr, now)
			if !res.Full {
				return nil
			}
			filledAt := now
			if x.FullAt != nil && x.FullAt.Before(now) {
				filledAt = *x.FullAt
			}
			coins := CoinsOf(res.Amount)
			if _, err := s.creditSession(ctx, tx, x, coins, now, "", true, ledger.ReasonAutocollect); err != nil {
				return err
			}
			nextFrom := filledAt
			if nextFrom.Before(boostFrom) {
				nextFrom = boostFrom
			}
			if _, err := s.newSessionTx(ctx, tx, p, nextFrom, ""); err != nil {
				return err
			}
			did++
		}
		return nil
	})
	return did, err
}

var _ = errors.New

// SetAds swaps the ads service (tests).
func (s *Service) SetAds(a *ads.Service) { s.ads = a }
