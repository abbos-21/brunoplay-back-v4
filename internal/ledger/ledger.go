// Package ledger is the only place where a player's coin or star balance changes.
// Every change is one atomic statement on the player row plus one append-only ledger row, inside the caller's transaction.
package ledger

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"

	"github.com/jackc/pgx/v5"

	"brunoplay/internal/apperr"
	"brunoplay/internal/db"
)

type Currency string

const (
	Coin Currency = "COIN"
	Star Currency = "STAR"
)

// Reasons written to the ledger (stable strings; the admin panel groups by them).
const (
	ReasonMining            = "mining_collect"
	ReasonAutocollect       = "autocollect"
	ReasonCardUpgrade       = "card_upgrade"
	ReasonBoosterBuy        = "booster_buy"
	ReasonLevelReward       = "level_reward"
	ReasonDailyStreak       = "daily_streak"
	ReasonDailyCombo        = "daily_combo"
	ReasonBonusAd           = "bonus_ad"
	ReasonTaskReward        = "task_reward"
	ReasonSponsoredTask     = "sponsored_task"
	ReasonReferralLevel     = "referral_level"
	ReasonReferralMining    = "referral_mining"
	ReasonFriendBonus       = "friend_join_bonus"
	ReasonReferralTopup     = "referral_topup"
	ReasonReferralTopupBack = "referral_topup_refund"
	ReasonTopup             = "topup"
	ReasonTopupBonus        = "topup_bonus"
	ReasonTopupRefund       = "topup_refund"
	ReasonSpinCharge        = "spin_charge"
	ReasonSpinPrize         = "spin_prize"
	ReasonChestCharge       = "chest_charge"
	ReasonChestPrize        = "chest_prize"
	ReasonRideCharge        = "ride_charge"
	ReasonRideReward        = "ride_reward"
	ReasonGiftSell          = "gift_sell"
	ReasonTournamentFee     = "tournament_fee"
	ReasonTournamentPrize   = "tournament_prize"
	ReasonTournamentRefund  = "tournament_refund"
	ReasonWithdrawal        = "withdrawal"
	ReasonWithdrawalRefund  = "withdrawal_refund"
	ReasonAdminAdjust       = "admin_adjust"
)

// Entry describes one balance change.
type Entry struct {
	PlayerID int64
	Currency Currency
	Amount   int64 // signed, never 0
	Reason   string
	RefType  string
	RefID    string
	Meta     map[string]any
	// IdemKey makes the entry apply at most once ever (e.g. "streak:42:2026-10-24"). A repeat returns ErrDuplicate.
	IdemKey string
	// Earned counts a positive coin amount towards the player's lifetime earnings.
	Earned bool
	// AllowNegative lets a debit take the balance below zero (refunds of already spent stars only).
	AllowNegative bool
}

var ErrDuplicate = errors.New("ledger: entry already applied")

// Apply changes the balance and appends the ledger row. It returns the new balance.
func Apply(ctx context.Context, tx db.Querier, e Entry) (int64, error) {
	if e.Amount == 0 {
		return 0, errors.New("ledger: zero amount")
	}
	if e.Currency != Coin && e.Currency != Star {
		return 0, errors.New("ledger: bad currency")
	}
	if e.IdemKey != "" {
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM ledger_entries WHERE idem_key=$1)`, e.IdemKey).Scan(&exists); err != nil {
			return 0, err
		}
		if exists {
			return 0, ErrDuplicate
		}
	}

	col := "coins"
	if e.Currency == Star {
		col = "stars"
	}
	earned := int64(0)
	if e.Currency == Coin && e.Earned && e.Amount > 0 {
		earned = e.Amount
	}

	var balance int64
	var q string
	if e.Amount < 0 && !e.AllowNegative {
		q = `UPDATE players SET ` + col + ` = ` + col + ` + $2 WHERE id=$1 AND ` + col + ` + $2 >= 0 RETURNING ` + col
	} else {
		q = `UPDATE players SET ` + col + ` = ` + col + ` + $2, total_coins_earned = total_coins_earned + $3 WHERE id=$1 RETURNING ` + col
	}
	var err error
	if e.Amount < 0 && !e.AllowNegative {
		err = tx.QueryRow(ctx, q, e.PlayerID, e.Amount).Scan(&balance)
	} else {
		err = tx.QueryRow(ctx, q, e.PlayerID, e.Amount, earned).Scan(&balance)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		var exists bool
		if qerr := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM players WHERE id=$1)`, e.PlayerID).Scan(&exists); qerr != nil {
			return 0, qerr
		}
		if !exists {
			return 0, apperr.NotFound
		}
		if e.Currency == Coin {
			return 0, apperr.InsufficientCoins
		}
		return 0, apperr.InsufficientStars
	}
	if err != nil {
		return 0, err
	}

	meta := []byte("{}")
	if len(e.Meta) > 0 {
		meta, _ = json.Marshal(e.Meta)
	}
	var idem any
	if e.IdemKey != "" {
		idem = e.IdemKey
	}
	_, err = tx.Exec(ctx, `INSERT INTO ledger_entries
		(player_id, currency, amount, balance_after, reason, ref_type, ref_id, meta, idem_key)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		e.PlayerID, string(e.Currency), e.Amount, balance, e.Reason, e.RefType, e.RefID, meta, idem)
	if err != nil {
		if db.IsUnique(err, "ledger_idem_uidx") {
			return 0, ErrDuplicate
		}
		return 0, err
	}
	return balance, nil
}

// RefID formats numeric ids for the ledger.
func RefID(id int64) string { return strconv.FormatInt(id, 10) }

// Balances returns both balances of a player.
func Balances(ctx context.Context, q db.Querier, playerID int64) (coins, stars int64, err error) {
	err = q.QueryRow(ctx, `SELECT coins, stars FROM players WHERE id=$1`, playerID).Scan(&coins, &stars)
	return
}
