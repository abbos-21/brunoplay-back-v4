// Package ads proves that a player really watched an Adsgram ad before an action. The browser is never trusted:
// completion arrives from Adsgram's server-to-server reward callback, and each proof pays for exactly one action.
package ads

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"brunoplay/internal/apperr"
	"brunoplay/internal/db"
	"brunoplay/internal/gameday"
	"brunoplay/internal/ledger"
	"brunoplay/internal/settings"
)

const (
	FishingStart = "FISHING_START"
	Collect      = "COLLECT"
	BonusCoins   = "BONUS_COINS"
	DoubleStreak = "DOUBLE_STREAK"
	DoubleCombo  = "DOUBLE_COMBO"
	ComboExtra   = "COMBO_EXTRA"
	RideFree     = "RIDE_FREE"
	TaskAds      = "TASK_ADS"
)

var Placements = []string{FishingStart, Collect, BonusCoins, DoubleStreak, DoubleCombo, ComboExtra, RideFree, TaskAds}

func ValidPlacement(p string) bool {
	for _, x := range Placements {
		if x == p {
			return true
		}
	}
	return false
}

// fallbackAllowed lists the placements that may continue without an ad after repeated failed loads.
// Reward-for-ad placements never do: no ad, no reward.
func fallbackAllowed(p string) bool { return p == FishingStart || p == Collect }

type Service struct {
	pool     *pgxpool.Pool
	settings *settings.Service
	secret   string
	Now      func() time.Time
}

func New(pool *pgxpool.Pool, st *settings.Service, webhookSecret string) *Service {
	return &Service{pool: pool, settings: st, secret: webhookSecret, Now: time.Now}
}

func (s *Service) now() time.Time { return s.Now() }

// Required reports whether ads gate fishing (ads.required).
func (s *Service) Required(ctx context.Context) bool {
	return s.settings.BoolOr(ctx, settings.KeyAdsRequired, true)
}

// View is what the app needs to show an ad.
type View struct {
	ID        string    `json:"adViewId"`
	Placement string    `json:"placement"`
	BlockID   string    `json:"blockId"`
	ExpiresAt time.Time `json:"expiresAt"`
	Required  bool      `json:"required"`
	Fallback  bool      `json:"fallback,omitempty"`
	// Skip: this player has ads switched off (admin panel). The proof is already completed: the app must not show an ad, only use it.
	Skip bool `json:"skip,omitempty"`
}

// PlayerAdsOff reports whether the admin switched the ads off for this player.
func (s *Service) PlayerAdsOff(ctx context.Context, q db.Querier, playerID int64) bool {
	var off bool
	err := q.QueryRow(ctx, `SELECT ads_disabled FROM players WHERE id=$1`, playerID).Scan(&off)
	return err == nil && off
}

func newID() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Enabled reports whether a placement is switched on in the admin settings.
func (s *Service) Enabled(ctx context.Context, placement string) bool {
	return s.enabled(ctx, placement)
}

func (s *Service) enabled(ctx context.Context, placement string) bool {
	m, err := settings.Get[map[string]bool](ctx, s.settings, settings.KeyAdsPlacements)
	if err != nil {
		return true
	}
	v, ok := m[placement]
	return !ok || v
}

func (s *Service) blockID(ctx context.Context, placement string) (string, error) {
	m, err := settings.Get[map[string]string](ctx, s.settings, settings.KeyAdsBlockIDs)
	if err != nil {
		return "", err
	}
	return m[placement], nil
}

// Request opens a PENDING proof and returns what the Adsgram SDK needs.
func (s *Service) Request(ctx context.Context, playerID int64, placement string) (*View, error) {
	if !ValidPlacement(placement) {
		return nil, apperr.Validation.With("field", "placement")
	}
	if !s.enabled(ctx, placement) {
		return nil, apperr.AdDisabled
	}
	if s.PlayerAdsOff(ctx, s.pool, playerID) {
		// no ad for this player: hand over a proof that is already completed, so everything after it (limits, rewards, idempotency) works as usual
		ttl, err := s.settings.Int(ctx, settings.KeyAdsViewExpiry)
		if err != nil {
			return nil, err
		}
		now := s.now()
		v := &View{ID: newID(), Placement: placement, ExpiresAt: now.Add(time.Duration(ttl) * time.Second), Required: true, Skip: true}
		_, err = s.pool.Exec(ctx, `INSERT INTO ad_views (id, player_id, placement, status, block_id, created_at, completed_at, expires_at)
			VALUES ($1,$2,$3,'COMPLETED','',$4,$4,$5)`, v.ID, playerID, placement, now, v.ExpiresAt)
		if err != nil {
			return nil, err
		}
		return v, nil
	}
	required := s.Required(ctx)
	block, err := s.blockID(ctx, placement)
	if err != nil {
		if required {
			return nil, err // CONFIG_MISSING: the owner has not entered the Adsgram block ids yet
		}
		block = ""
	}
	if required && block == "" {
		return nil, apperr.ConfigMissing.With("key", settings.KeyAdsBlockIDs)
	}
	ttl, err := s.settings.Int(ctx, settings.KeyAdsViewExpiry)
	if err != nil {
		return nil, err
	}
	now := s.now()
	v := &View{ID: newID(), Placement: placement, BlockID: block, ExpiresAt: now.Add(time.Duration(ttl) * time.Second), Required: required}
	// An ad the player never finished must not catch Adsgram's confirmation meant for this one: the callback carries only
	// the user and the block, so there can be just one open request per player and placement.
	if _, err := s.pool.Exec(ctx, `UPDATE ad_views SET status='EXPIRED' WHERE player_id=$1 AND placement=$2 AND status='PENDING'`, playerID, placement); err != nil {
		return nil, err
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO ad_views (id, player_id, placement, status, block_id, created_at, expires_at)
		VALUES ($1,$2,$3,'PENDING',$4,$5,$6)`, v.ID, playerID, placement, block, now, v.ExpiresAt)
	return v, err
}

// SecretOK checks the shared secret of the reward callback in constant time.
func (s *Service) SecretOK(got string) bool {
	return s.secret != "" && subtle.ConstantTimeCompare([]byte(got), []byte(s.secret)) == 1
}

// Complete is called by Adsgram's reward callback: the oldest open proof of that Telegram user (and block) becomes COMPLETED.
// It reports whether a proof was found; unknown users or repeats are not errors (the callback is always answered 200).
func (s *Service) Complete(ctx context.Context, telegramID int64, blockID string) (bool, error) {
	now := s.now()
	var playerID int64
	var id string
	err := db.WithTx(ctx, s.pool, func(tx pgxTx) error {
		if err := tx.QueryRow(ctx, `SELECT id FROM players WHERE telegram_id=$1`, telegramID).Scan(&playerID); err != nil {
			return err
		}
		err := tx.QueryRow(ctx, `UPDATE ad_views SET status='COMPLETED', completed_at=$3 WHERE id = (
			SELECT id FROM ad_views WHERE player_id=$1 AND status='PENDING' AND expires_at > $3 AND ($2='' OR block_id=$2)
			ORDER BY created_at DESC LIMIT 1 FOR UPDATE SKIP LOCKED) RETURNING id`, playerID, blockID, now).Scan(&id)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `DELETE FROM ad_failures WHERE player_id=$1`, playerID)
		return err
	})
	if db.IsNoRows(err) {
		return false, nil
	}
	return err == nil, err
}

// Failed records that the SDK could not load an ad. After N failures in a row the player gets a short no-ad window
// (only for fishing start/collect), logged as a fallback.
func (s *Service) Failed(ctx context.Context, playerID int64, placement string) (*View, error) {
	if !ValidPlacement(placement) {
		return nil, apperr.Validation.With("field", "placement")
	}
	if !fallbackAllowed(placement) {
		return nil, nil
	}
	maxFailed, err := s.settings.Int(ctx, settings.KeyAdsMaxFailed)
	if err != nil {
		return nil, err
	}
	now := s.now()
	var count int
	err = s.pool.QueryRow(ctx, `INSERT INTO ad_failures (player_id, placement, count, last_at) VALUES ($1,$2,1,$3)
		ON CONFLICT (player_id, placement) DO UPDATE SET
			count = CASE WHEN ad_failures.last_at < $3 - interval '30 minutes' THEN 1 ELSE ad_failures.count + 1 END, last_at = $3
		RETURNING count`, playerID, placement, now).Scan(&count)
	if err != nil {
		return nil, err
	}
	if int64(count) < maxFailed {
		return nil, nil
	}
	mins, err := s.settings.Int(ctx, settings.KeyAdsFallbackMin)
	if err != nil {
		return nil, err
	}
	v := &View{ID: newID(), Placement: placement, ExpiresAt: now.Add(time.Duration(mins) * time.Minute), Fallback: true, Required: true}
	if _, err := s.pool.Exec(ctx, `INSERT INTO ad_views (id, player_id, placement, status, fallback, created_at, completed_at, expires_at)
		VALUES ($1,$2,$3,'COMPLETED',TRUE,$4,$4,$5)`, v.ID, playerID, placement, now, v.ExpiresAt); err != nil {
		return nil, err
	}
	_, _ = s.pool.Exec(ctx, `DELETE FROM ad_failures WHERE player_id=$1 AND placement=$2`, playerID, placement)
	return v, nil
}

// Consume spends a proof for one action, inside the action's transaction. It returns the reason in the error when the
// proof cannot be used. usedFor labels the action ("start", "collect", ...).
func (s *Service) Consume(ctx context.Context, tx db.Querier, playerID int64, adID, placement, usedFor string) error {
	now := s.now()
	var id string
	err := tx.QueryRow(ctx, `UPDATE ad_views SET status='USED', used_at=$4, used_for=$5
		WHERE id=$1 AND player_id=$2 AND placement=$3 AND status='COMPLETED' AND expires_at > $4 RETURNING id`,
		adID, playerID, placement, now, usedFor).Scan(&id)
	if err == nil {
		return nil
	}
	if !db.IsNoRows(err) {
		return err
	}
	var status string
	var exp time.Time
	err = tx.QueryRow(ctx, `SELECT status, expires_at FROM ad_views WHERE id=$1 AND player_id=$2 AND placement=$3`, adID, playerID, placement).Scan(&status, &exp)
	if db.IsNoRows(err) {
		return apperr.AdNotCompleted
	}
	if err != nil {
		return err
	}
	switch {
	case status == "USED":
		return apperr.AdUsed
	case status == "COMPLETED" && !exp.After(now), status == "EXPIRED":
		return apperr.AdExpired
	case status == "PENDING" && !exp.After(now):
		return apperr.AdExpired
	}
	return apperr.AdNotCompleted
}

// Gate is Consume plus the "ads not required" switch: with ads off and no proof given the action goes ahead.
func (s *Service) Gate(ctx context.Context, tx db.Querier, playerID int64, adID, placement, usedFor string) error {
	if adID == "" {
		if !s.Required(ctx) || s.PlayerAdsOff(ctx, tx, playerID) {
			return nil
		}
		return apperr.AdNotCompleted
	}
	return s.Consume(ctx, tx, playerID, adID, placement, usedFor)
}

// UsedFor tells what an already used proof paid for (so a retried request can be answered idempotently).
func (s *Service) UsedFor(ctx context.Context, q db.Querier, playerID int64, adID string) (string, bool) {
	var used string
	err := q.QueryRow(ctx, `SELECT used_for FROM ad_views WHERE id=$1 AND player_id=$2 AND status='USED'`, adID, playerID).Scan(&used)
	return used, err == nil
}

// Purge removes old proofs (worker).
func (s *Service) Purge(ctx context.Context, olderThan time.Duration) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM ad_views WHERE created_at < $1`, s.now().Add(-olderThan))
	return err
}

type pgxTx = pgx.Tx

// ---- bonus ad ("watch an ad for free coins")

type BonusStatus struct {
	Coins  int64 `json:"coins"`
	PerDay int64 `json:"perDay"`
	Used   int64 `json:"usedToday"`
	Left   int64 `json:"left"`
}

const bonusKey = "bonus_ad"

func (s *Service) Bonus(ctx context.Context, playerID int64) (*BonusStatus, error) {
	coins, err := s.settings.Int(ctx, settings.KeyAdsBonusCoins)
	if err != nil {
		return nil, err
	}
	per, err := s.settings.Int(ctx, settings.KeyAdsBonusPerDay)
	if err != nil {
		return nil, err
	}
	used, err := gameday.Get(ctx, s.pool, playerID, s.now(), bonusKey)
	if err != nil {
		return nil, err
	}
	left := per - used
	if left < 0 {
		left = 0
	}
	return &BonusStatus{Coins: coins, PerDay: per, Used: used, Left: left}, nil
}

type BonusResult struct {
	Coins      int64 `json:"coins"`
	NewBalance int64 `json:"newBalance"`
	Left       int64 `json:"left"`
}

// ClaimBonus pays the bonus coins for one watched ad, at most `per day` times per game day.
func (s *Service) ClaimBonus(ctx context.Context, playerID int64, adID string) (*BonusResult, error) {
	coins, err := s.settings.Int(ctx, settings.KeyAdsBonusCoins)
	if err != nil {
		return nil, err
	}
	per, err := s.settings.Int(ctx, settings.KeyAdsBonusPerDay)
	if err != nil {
		return nil, err
	}
	now := s.now()
	var res *BonusResult
	err = db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT 1 FROM players WHERE id=$1 FOR UPDATE`, playerID); err != nil {
			return err
		}
		used, ok, err := gameday.Take(ctx, tx, playerID, now, bonusKey, 1, per)
		if err != nil {
			return err
		}
		if !ok {
			return apperr.AdLimit
		}
		if err := s.Consume(ctx, tx, playerID, adID, BonusCoins, "bonus_ad"); err != nil {
			return err
		}
		bal, err := ledger.Apply(ctx, tx, ledger.Entry{PlayerID: playerID, Currency: ledger.Coin, Amount: coins, Reason: ledger.ReasonBonusAd,
			RefType: "ad", RefID: adID, IdemKey: "bonusad:" + adID, Earned: true})
		if err != nil {
			return err
		}
		res = &BonusResult{Coins: coins, NewBalance: bal, Left: per - used}
		return nil
	})
	return res, err
}
