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

// The popup task is Monetag's Rewarded Popup: a card in the task list ("open a partner offer"). The tap opens the advertiser's page
// outside the game. The browser only allows that inside a tap, so the game asks for the proof BEFORE the tap (Prepare) and then calls
// Monetag's SDK at once with the proof id as the ymid. Monetag's postback (server to server, see CompleteView) completes the proof and
// only after that, and after the wait that rewarded Monetag ads use too, can the game claim the coins. A claim without a postback
// behind it gets nothing. It reuses the ad_views table like the sponsored task does.
const (
	// PopupTask is the placement name of those rows. It is deliberately NOT in Placements: the app cannot request it as an ad.
	PopupTask = "POPUP_TASK"

	popupKeep = 15 * time.Minute // how long a prepared proof (and a completed one waiting to be claimed) stays valid
)

// PopupStatus is what the Tasks screen needs to show (or hide) the popup task card.
type PopupStatus struct {
	Enabled         bool   `json:"enabled"`
	Provider        string `json:"provider"`
	ZoneID          string `json:"zoneId"`
	Coins           int64  `json:"coins"`
	IntervalMinutes int64  `json:"intervalMinutes"`
	// WaitSeconds is how long after Monetag confirms the open the coins can be claimed (the game shows it as a countdown)
	WaitSeconds   int64 `json:"waitSeconds"`
	Available     bool  `json:"available"`
	NextInSeconds int64 `json:"nextInSeconds"`
}

// PopupProof is the proof the game holds until the tap.
type PopupProof struct {
	AdViewID  string    `json:"adViewId"`
	ExpiresAt time.Time `json:"expiresAt"`
	// Completed: Monetag already confirmed this proof (the player was away and came back): the game only has to claim it.
	Completed bool `json:"completed"`
}

// PopupResult is what the claim returns.
type PopupResult struct {
	Coins         int64 `json:"coins"`
	NewBalance    int64 `json:"newBalance"`
	NextInSeconds int64 `json:"nextInSeconds"` // when the next one can be done
}

// Popup tells whether the task is on, and whether the player may do one now (the interval since the last paid one has passed).
func (s *Service) Popup(ctx context.Context, playerID int64) (*PopupStatus, error) {
	st := &PopupStatus{Provider: "monetag"}
	zone := strings.TrimSpace(s.settings.StrOr(ctx, settings.KeyAdsMonetagZone, ""))
	st.ZoneID = zone
	st.Enabled = s.settings.BoolOr(ctx, settings.KeyAdsMonetagTask, false) && widgetRe.MatchString(zone) && s.ProviderOn(ctx, "monetag", FormatTask)
	if st.Enabled && s.PlayerAdsOff(ctx, s.pool, playerID) {
		st.Enabled = false
	}
	if !st.Enabled {
		return st, nil
	}
	var err error
	if st.Coins, err = s.settings.Int(ctx, settings.KeyAdsMonetagTaskCoins); err != nil {
		return nil, err
	}
	if st.IntervalMinutes, err = s.settings.Int(ctx, settings.KeyAdsMonetagTaskInterval); err != nil {
		return nil, err
	}
	st.WaitSeconds = s.waitSeconds(ctx, "monetag")
	last, err := s.lastPaid(ctx, s.pool, playerID, PopupTask)
	if err != nil {
		return nil, err
	}
	wait := waitFor(last, time.Duration(st.IntervalMinutes)*time.Minute, s.now())
	st.Available = wait == 0
	st.NextInSeconds = int64((wait + time.Second - 1) / time.Second)
	return st, nil
}

// PopupPrepare hands the game a proof for the next popup. An open one is handed out again (the card can be shown many times without
// piling up rows); a completed one that waits to be claimed is reported as such.
func (s *Service) PopupPrepare(ctx context.Context, playerID int64) (*PopupProof, error) {
	st, err := s.Popup(ctx, playerID)
	if err != nil {
		return nil, err
	}
	if !st.Enabled {
		return nil, apperr.AdDisabled
	}
	if !st.Available {
		return nil, apperr.AdTaskWait
	}
	now := s.now()
	var out *PopupProof
	err = db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		out = nil
		if _, err := tx.Exec(ctx, `SELECT 1 FROM players WHERE id=$1 FOR UPDATE`, playerID); err != nil {
			return err
		}
		var p PopupProof
		var status string
		err := tx.QueryRow(ctx, `SELECT id, expires_at, status FROM ad_views WHERE player_id=$1 AND placement=$2 AND status IN ('PENDING','COMPLETED')
			AND expires_at > $3 ORDER BY (status='COMPLETED') DESC, created_at DESC LIMIT 1`, playerID, PopupTask, now).Scan(&p.AdViewID, &p.ExpiresAt, &status)
		if err == nil {
			p.Completed = status == "COMPLETED"
			out = &p
			return nil
		}
		if !db.IsNoRows(err) {
			return err
		}
		p = PopupProof{AdViewID: newID(), ExpiresAt: now.Add(popupKeep)}
		_, err = tx.Exec(ctx, `INSERT INTO ad_views (id, player_id, placement, status, providers, created_at, expires_at)
			VALUES ($1,$2,$3,'PENDING','monetag',$4,$5)`, p.AdViewID, playerID, PopupTask, now, p.ExpiresAt)
		out = &p
		return err
	})
	return out, err
}

// ClaimPopup pays the coins for one popup that Monetag's postback confirmed (AD_NOT_COMPLETED until it did, and until the wait is over;
// the game asks again for a while), and only when the interval since the last paid one has passed.
func (s *Service) ClaimPopup(ctx context.Context, playerID int64, adViewID string) (*PopupResult, error) {
	st, err := s.Popup(ctx, playerID)
	if err != nil {
		return nil, err
	}
	if !st.Enabled {
		return nil, apperr.AdDisabled
	}
	var res *PopupResult
	err = db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT 1 FROM players WHERE id=$1 FOR UPDATE`, playerID); err != nil {
			return err
		}
		interval := time.Duration(st.IntervalMinutes) * time.Minute
		last, err := s.lastPaid(ctx, tx, playerID, PopupTask)
		if err != nil {
			return err
		}
		if waitFor(last, interval, s.now()) > 0 {
			return apperr.AdTaskWait
		}
		if err := s.Consume(ctx, tx, playerID, adViewID, PopupTask, "popup_task"); err != nil {
			return err
		}
		bal, err := ledger.Apply(ctx, tx, ledger.Entry{PlayerID: playerID, Currency: ledger.Coin, Amount: st.Coins, Reason: ledger.ReasonSponsoredTask,
			RefType: "ad", RefID: adViewID, IdemKey: "popuptask:" + adViewID, Earned: true})
		if err != nil {
			return err
		}
		res = &PopupResult{Coins: st.Coins, NewBalance: bal, NextInSeconds: int64(interval / time.Second)}
		return nil
	})
	return res, err
}
