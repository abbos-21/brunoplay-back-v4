package ads

import (
	"context"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"brunoplay/internal/apperr"
	"brunoplay/internal/db"
	"brunoplay/internal/gameday"
	"brunoplay/internal/ledger"
	"brunoplay/internal/settings"
)

// The sponsored task is Adsgram's Task block: a card in the task list ("join this channel", "open this app"). The player does
// it in Adsgram's own element; Adsgram then calls our Reward URL of the Task block (server to server) and only after that
// can the game claim the coins. A browser that merely claims, with no callback behind it, gets nothing.
// It reuses the ad_views table: the callback writes a COMPLETED row, the claim turns it into USED.
const (
	// SponsoredTask is the placement name of those rows. It is deliberately NOT in Placements: the app cannot request it.
	SponsoredTask = "SPONSORED_TASK"

	sponsoredKey     = "sponsored_task" // per-day counter
	sponsoredKeep    = 6 * time.Hour    // how long a completed task can wait to be claimed
	sponsoredMaxOpen = 5                // completions waiting to be claimed, per player
	sponsoredRepeat  = 10 * time.Second // the same callback sent twice in a row counts once
)

// InterstitialConfig tells the game when it may show an Interstitial ad. The server keeps no state for them: nothing is paid.
type InterstitialConfig struct {
	Enabled           bool   `json:"enabled"`
	BlockID           string `json:"blockId"`
	IntervalSeconds   int64  `json:"intervalSeconds"`
	FirstAfterSeconds int64  `json:"firstAfterSeconds"`
}

// SponsoredStatus is what the Tasks screen needs to show (or hide) the sponsored task.
type SponsoredStatus struct {
	Enabled bool   `json:"enabled"`
	BlockID string `json:"blockId"`
	Coins   int64  `json:"coins"`
	PerDay  int64  `json:"perDay"`
	Used    int64  `json:"usedToday"`
	Left    int64  `json:"left"`
}

// Config is the answer of GET /api/ads/config.
type Config struct {
	Interstitial InterstitialConfig `json:"interstitial"`
	Task         SponsoredStatus    `json:"task"`
}

// Config reads the interstitial settings and the player's sponsored-task status.
func (s *Service) Config(ctx context.Context, playerID int64) (*Config, error) {
	c := &Config{}
	block := strings.TrimSpace(s.settings.StrOr(ctx, settings.KeyAdsInterBlock, ""))
	c.Interstitial.Enabled = s.settings.BoolOr(ctx, settings.KeyAdsInterEnabled, false) && block != ""
	c.Interstitial.BlockID = block
	c.Interstitial.IntervalSeconds, _ = s.settings.Int(ctx, settings.KeyAdsInterGap)
	c.Interstitial.FirstAfterSeconds, _ = s.settings.Int(ctx, settings.KeyAdsInterFirst)

	st, err := s.Sponsored(ctx, playerID)
	if err != nil {
		return nil, err
	}
	c.Task = *st
	return c, nil
}

// Sponsored tells whether the task is on, and how many more rewards the player can get today.
func (s *Service) Sponsored(ctx context.Context, playerID int64) (*SponsoredStatus, error) {
	block := strings.TrimSpace(s.settings.StrOr(ctx, settings.KeyAdsTaskBlock, ""))
	st := &SponsoredStatus{Enabled: s.settings.BoolOr(ctx, settings.KeyAdsTaskEnabled, false) && block != "", BlockID: block}
	if !st.Enabled {
		return st, nil
	}
	var err error
	if st.Coins, err = s.settings.Int(ctx, settings.KeyAdsTaskCoins); err != nil {
		return nil, err
	}
	if st.PerDay, err = s.settings.Int(ctx, settings.KeyAdsTaskPerDay); err != nil {
		return nil, err
	}
	if st.Used, err = gameday.Get(ctx, s.pool, playerID, s.now(), sponsoredKey); err != nil {
		return nil, err
	}
	st.Left = max(st.PerDay-st.Used, 0)
	return st, nil
}

// blockNumber lets "task-123" and "123" mean the same block (Adsgram's callback and its web component do not agree on the form).
func blockNumber(id string) string { return strings.TrimPrefix(strings.TrimSpace(id), "task-") }

// SponsoredDone is called by Adsgram's reward callback of the Task block: the player completed a sponsored task.
// It records one claimable completion. Unknown users, repeats and switched-off tasks are not errors (the callback is answered 200).
func (s *Service) SponsoredDone(ctx context.Context, telegramID int64, blockID string) (bool, error) {
	block := strings.TrimSpace(s.settings.StrOr(ctx, settings.KeyAdsTaskBlock, ""))
	if !s.settings.BoolOr(ctx, settings.KeyAdsTaskEnabled, false) || block == "" {
		return false, nil
	}
	if blockID != "" && blockNumber(blockID) != blockNumber(block) {
		return false, nil
	}
	now := s.now()
	recorded := false
	err := db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		recorded = false
		var playerID int64
		if err := tx.QueryRow(ctx, `SELECT id FROM players WHERE telegram_id=$1 FOR UPDATE`, telegramID).Scan(&playerID); err != nil {
			return err
		}
		var open, recent int
		if err := tx.QueryRow(ctx, `SELECT count(*), count(*) FILTER (WHERE created_at > $3) FROM ad_views
			WHERE player_id=$1 AND placement=$2 AND status='COMPLETED' AND expires_at > $4`,
			playerID, SponsoredTask, now.Add(-sponsoredRepeat), now).Scan(&open, &recent); err != nil {
			return err
		}
		if open >= sponsoredMaxOpen || recent > 0 {
			return nil
		}
		_, err := tx.Exec(ctx, `INSERT INTO ad_views (id, player_id, placement, status, block_id, created_at, completed_at, expires_at)
			VALUES ($1,$2,$3,'COMPLETED',$4,$5,$5,$6)`, newID(), playerID, SponsoredTask, block, now, now.Add(sponsoredKeep))
		recorded = err == nil
		return err
	})
	if db.IsNoRows(err) {
		return false, nil
	}
	return recorded, err
}

// SponsoredResult is what the claim returns.
type SponsoredResult struct {
	Coins      int64 `json:"coins"`
	NewBalance int64 `json:"newBalance"`
	Left       int64 `json:"left"`
}

// ClaimSponsored pays the coins for one completed sponsored task: it needs a completion recorded by Adsgram's callback
// (otherwise AD_NOT_COMPLETED: the game asks again for a few seconds), and at most `per day` times per game day.
func (s *Service) ClaimSponsored(ctx context.Context, playerID int64) (*SponsoredResult, error) {
	st, err := s.Sponsored(ctx, playerID)
	if err != nil {
		return nil, err
	}
	if !st.Enabled {
		return nil, apperr.AdDisabled
	}
	now := s.now()
	var res *SponsoredResult
	err = db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT 1 FROM players WHERE id=$1 FOR UPDATE`, playerID); err != nil {
			return err
		}
		used, ok, err := gameday.Take(ctx, tx, playerID, now, sponsoredKey, 1, st.PerDay)
		if err != nil {
			return err
		}
		if !ok {
			return apperr.AdLimit
		}
		var id string
		err = tx.QueryRow(ctx, `UPDATE ad_views SET status='USED', used_at=$3, used_for='sponsored_task' WHERE id = (
			SELECT id FROM ad_views WHERE player_id=$1 AND placement=$2 AND status='COMPLETED' AND expires_at > $3
			ORDER BY created_at LIMIT 1 FOR UPDATE SKIP LOCKED) RETURNING id`, playerID, SponsoredTask, now).Scan(&id)
		if db.IsNoRows(err) {
			return apperr.AdNotCompleted // rolls back the day counter above as well
		}
		if err != nil {
			return err
		}
		bal, err := ledger.Apply(ctx, tx, ledger.Entry{PlayerID: playerID, Currency: ledger.Coin, Amount: st.Coins, Reason: ledger.ReasonSponsoredTask,
			RefType: "ad", RefID: id, IdemKey: "sponsored:" + id, Earned: true})
		if err != nil {
			return err
		}
		res = &SponsoredResult{Coins: st.Coins, NewBalance: bal, Left: st.PerDay - used}
		return nil
	})
	return res, err
}
