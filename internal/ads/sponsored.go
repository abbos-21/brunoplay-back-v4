package ads

import (
	"context"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"brunoplay/internal/apperr"
	"brunoplay/internal/db"
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

	sponsoredKeep    = 6 * time.Hour    // how long a completed task can wait to be claimed
	sponsoredMaxOpen = 5                // completions waiting to be claimed, per player
	sponsoredRepeat  = 10 * time.Second // the same callback sent twice in a row counts once
)

// InterstitialConfig tells the game when it may show an Interstitial ad. The server keeps no state for them: nothing is paid.
type InterstitialConfig struct {
	Enabled bool   `json:"enabled"`
	BlockID string `json:"blockId"` // the Adsgram block (kept for older apps); Providers is the full list
	// Providers is every provider that is switched on, in priority order: the app shows an ad of each one, one after the other.
	Providers         []ProviderRef `json:"providers"`
	IntervalSeconds   int64         `json:"intervalSeconds"`
	FirstAfterSeconds int64         `json:"firstAfterSeconds"`
}

// SponsoredStatus is what the Tasks screen needs to show (or hide) the sponsored task. After a paid task the next one is
// available only when the admin's interval (minutes) has passed: Available says whether it is now, NextInSeconds how long to wait.
type SponsoredStatus struct {
	Enabled         bool   `json:"enabled"`
	BlockID         string `json:"blockId"`
	Coins           int64  `json:"coins"`
	IntervalMinutes int64  `json:"intervalMinutes"`
	Available       bool   `json:"available"`
	NextInSeconds   int64  `json:"nextInSeconds"`
}

// BannerConfig is a block of ad cards inside a screen (TADS TGB). Nothing is paid for it.
type BannerConfig struct {
	Enabled  bool   `json:"enabled"`
	Provider string `json:"provider"`
	WidgetID string `json:"widgetId"`
	Debug    bool   `json:"debug"`
}

// Config is the answer of GET /api/ads/config.
type Config struct {
	Interstitial InterstitialConfig `json:"interstitial"`
	Task         SponsoredStatus    `json:"task"`
	Banner       BannerConfig       `json:"banner"`
}

// Config reads the interstitial settings and the player's sponsored-task status.
func (s *Service) Config(ctx context.Context, playerID int64) (*Config, error) {
	c := &Config{}
	block := strings.TrimSpace(s.settings.StrOr(ctx, settings.KeyAdsInterBlock, ""))
	c.Interstitial.BlockID = block
	c.Interstitial.Providers = s.interstitialChain(ctx, block)
	c.Interstitial.Enabled = s.settings.BoolOr(ctx, settings.KeyAdsInterEnabled, false) && len(c.Interstitial.Providers) > 0
	if c.Interstitial.Enabled && s.PlayerAdsOff(ctx, s.pool, playerID) {
		c.Interstitial.Enabled = false
	}
	c.Interstitial.IntervalSeconds, _ = s.settings.Int(ctx, settings.KeyAdsInterGap)
	c.Interstitial.FirstAfterSeconds, _ = s.settings.Int(ctx, settings.KeyAdsInterFirst)

	st, err := s.Sponsored(ctx, playerID)
	if err != nil {
		return nil, err
	}
	c.Task = *st

	w := strings.TrimSpace(s.settings.StrOr(ctx, settings.KeyAdsTadsTGB, ""))
	c.Banner = BannerConfig{Provider: "tads", WidgetID: w, Debug: s.settings.BoolOr(ctx, settings.KeyAdsTadsDebug, false)}
	c.Banner.Enabled = widgetRe.MatchString(w) && s.ProviderOn(ctx, "tads", FormatBanner) && !s.PlayerAdsOff(ctx, s.pool, playerID)
	return c, nil
}

// interstitialChain lists, in priority order, the providers that show an interstitial now: Adsgram with its interstitial block,
// Monetag with its zone (the others do not serve the format). Unlike rewarded ads, ALL of them are shown, one after the other.
func (s *Service) interstitialChain(ctx context.Context, adsgramBlock string) []ProviderRef {
	out := []ProviderRef{}
	for _, e := range s.entries(ctx) {
		p := findProvider(e.ID)
		if !e.Enabled || !p.has(FormatInterstitial) {
			continue
		}
		switch e.ID {
		case "adsgram":
			if adsgramBlock != "" {
				out = append(out, ProviderRef{ID: "adsgram", BlockID: adsgramBlock})
			}
		default:
			if ref, ok := p.Ref(ctx, s, ""); ok {
				ref.WaitSeconds = 0 // nothing is paid: no proof to wait for
				out = append(out, ref)
			}
		}
	}
	return out
}

// lastPaid is when the player was last paid for a sponsored task (nil: never).
func (s *Service) lastPaid(ctx context.Context, q db.Querier, playerID int64) (*time.Time, error) {
	var at *time.Time
	err := q.QueryRow(ctx, `SELECT max(used_at) FROM ad_views WHERE player_id=$1 AND placement=$2 AND status='USED'`, playerID, SponsoredTask).Scan(&at)
	return at, err
}

// waitFor is how long until the next paid task is allowed (0 = now).
func waitFor(last *time.Time, interval time.Duration, now time.Time) time.Duration {
	if last == nil {
		return 0
	}
	return max(last.Add(interval).Sub(now), 0)
}

// Sponsored tells whether the task is on, and whether the player may do one now (the interval since the last paid one has passed).
func (s *Service) Sponsored(ctx context.Context, playerID int64) (*SponsoredStatus, error) {
	block := strings.TrimSpace(s.settings.StrOr(ctx, settings.KeyAdsTaskBlock, ""))
	st := &SponsoredStatus{Enabled: s.settings.BoolOr(ctx, settings.KeyAdsTaskEnabled, false) && block != "" && s.ProviderOn(ctx, "adsgram", FormatTask), BlockID: block}
	if st.Enabled && s.PlayerAdsOff(ctx, s.pool, playerID) {
		st.Enabled = false
	}
	if !st.Enabled {
		return st, nil
	}
	var err error
	if st.Coins, err = s.settings.Int(ctx, settings.KeyAdsTaskCoins); err != nil {
		return nil, err
	}
	if st.IntervalMinutes, err = s.settings.Int(ctx, settings.KeyAdsTaskInterval); err != nil {
		return nil, err
	}
	last, err := s.lastPaid(ctx, s.pool, playerID)
	if err != nil {
		return nil, err
	}
	wait := waitFor(last, time.Duration(st.IntervalMinutes)*time.Minute, s.now())
	st.Available = wait == 0
	st.NextInSeconds = int64((wait + time.Second - 1) / time.Second)
	return st, nil
}

// blockNumber lets "task-123" and "123" mean the same block (Adsgram's callback and its web component do not agree on the form).
func blockNumber(id string) string { return strings.TrimPrefix(strings.TrimSpace(id), "task-") }

// SponsoredDone is called by Adsgram's reward callback of the Task block: the player completed a sponsored task.
// It records one claimable completion. Unknown users, repeats and switched-off tasks are not errors (the callback is answered 200).
func (s *Service) SponsoredDone(ctx context.Context, telegramID int64, blockID string) (bool, error) {
	block := strings.TrimSpace(s.settings.StrOr(ctx, settings.KeyAdsTaskBlock, ""))
	if !s.settings.BoolOr(ctx, settings.KeyAdsTaskEnabled, false) || block == "" || !s.ProviderOn(ctx, "adsgram", FormatTask) {
		return false, nil
	}
	if blockID != "" && blockNumber(blockID) != blockNumber(block) {
		return false, nil
	}
	interval, _ := s.settings.Int(ctx, settings.KeyAdsTaskInterval)
	now := s.now()
	recorded := false
	err := db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		recorded = false
		var playerID int64
		if err := tx.QueryRow(ctx, `SELECT id FROM players WHERE telegram_id=$1 FOR UPDATE`, telegramID).Scan(&playerID); err != nil {
			return err
		}
		// a completion during the wait could be claimed the moment the wait ends: do not keep it
		if last, err := s.lastPaid(ctx, tx, playerID); err != nil {
			return err
		} else if waitFor(last, time.Duration(interval)*time.Minute, now) > 0 {
			return nil
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
	Coins         int64 `json:"coins"`
	NewBalance    int64 `json:"newBalance"`
	NextInSeconds int64 `json:"nextInSeconds"` // when the next one can be done
}

// ClaimSponsored pays the coins for one completed sponsored task: it needs a completion recorded by Adsgram's callback
// (otherwise AD_NOT_COMPLETED: the game asks again for a few seconds), and only when the interval since the last paid one has passed.
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
		interval := time.Duration(st.IntervalMinutes) * time.Minute
		last, err := s.lastPaid(ctx, tx, playerID)
		if err != nil {
			return err
		}
		if waitFor(last, interval, now) > 0 {
			return apperr.AdTaskWait
		}
		var id string
		err = tx.QueryRow(ctx, `UPDATE ad_views SET status='USED', used_at=$3, used_for='sponsored_task' WHERE id = (
			SELECT id FROM ad_views WHERE player_id=$1 AND placement=$2 AND status='COMPLETED' AND expires_at > $3
			ORDER BY created_at LIMIT 1 FOR UPDATE SKIP LOCKED) RETURNING id`, playerID, SponsoredTask, now).Scan(&id)
		if db.IsNoRows(err) {
			return apperr.AdNotCompleted
		}
		if err != nil {
			return err
		}
		bal, err := ledger.Apply(ctx, tx, ledger.Entry{PlayerID: playerID, Currency: ledger.Coin, Amount: st.Coins, Reason: ledger.ReasonSponsoredTask,
			RefType: "ad", RefID: id, IdemKey: "sponsored:" + id, Earned: true})
		if err != nil {
			return err
		}
		res = &SponsoredResult{Coins: st.Coins, NewBalance: bal, NextInSeconds: int64(interval / time.Second)}
		return nil
	})
	return res, err
}
