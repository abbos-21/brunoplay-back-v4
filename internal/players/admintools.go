package players

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"brunoplay/internal/apperr"
	"brunoplay/internal/db"
	"brunoplay/internal/ledger"
)

// Admin tools for one player: set the balances, reset the player, switch ads off.

const maxBalance = 1_000_000_000_000

func refused(format string, a ...any) error {
	return apperr.AdminRefused.With("why", fmt.Sprintf(format, a...))
}

// SetBalances sets the coins and/or the stars to the given amounts (nil = leave as it is). The difference is written to the
// ledger as an "admin_adjust" entry that names the admin and the reason, so the money trail stays complete. It returns the new balances.
func (s *Store) SetBalances(ctx context.Context, playerID int64, coins, stars *int64, adminID int64, reason string) (newCoins, newStars int64, err error) {
	for _, v := range []*int64{coins, stars} {
		if v != nil && (*v < 0 || *v > maxBalance) {
			return 0, 0, refused("The amount must be between 0 and %d", int64(maxBalance))
		}
	}
	err = db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		var c, st int64
		if err := tx.QueryRow(ctx, `SELECT coins, stars FROM players WHERE id=$1 AND deleted_at IS NULL FOR UPDATE`, playerID).Scan(&c, &st); err != nil {
			if db.IsNoRows(err) {
				return apperr.NotFound
			}
			return err
		}
		newCoins, newStars = c, st
		meta := map[string]any{"admin": adminID, "reason": reason}
		apply := func(cur int64, want *int64, cur_ ledger.Currency) (int64, error) {
			if want == nil || *want == cur {
				return cur, nil
			}
			// stars may be below zero after a refund: the admin can lift them to any amount, and lower them to zero
			return ledger.Apply(ctx, tx, ledger.Entry{PlayerID: playerID, Currency: cur_, Amount: *want - cur, Reason: ledger.ReasonAdminAdjust,
				RefType: "admin", RefID: ledger.RefID(adminID), Meta: meta, AllowNegative: cur_ == ledger.Star})
		}
		var e error
		if newCoins, e = apply(c, coins, ledger.Coin); e != nil {
			return e
		}
		newStars, e = apply(st, stars, ledger.Star)
		return e
	})
	return newCoins, newStars, err
}

// SetAdsDisabled switches the ads off (or on again) for one player: every action that asks for an ad goes ahead without one.
func (s *Store) SetAdsDisabled(ctx context.Context, playerID int64, off bool) error {
	ct, err := s.pool.Exec(ctx, `UPDATE players SET ads_disabled=$2 WHERE id=$1 AND deleted_at IS NULL`, playerID, off)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return apperr.NotFound
	}
	return nil
}

// Reset starts the player over. The old record, with its whole money trail (ledger, withdrawals, top-ups), is archived
// under the same id; its coins and stars are brought to zero through the ledger, and the Telegram account is freed. The next time
// the person opens the game they sign up as a brand-new player: level 1, no coins, no stars, no cards, no tasks done.
// It is refused while money is still on its way (open withdrawal, gift claim, unfinished top-up) and for a blocked player
// (a reset must not be a way out of a block).
func (s *Store) Reset(ctx context.Context, playerID, adminID int64, reason string) (archivedTelegramID int64, err error) {
	err = db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		var coins, stars int64
		var blocked bool
		if err := tx.QueryRow(ctx, `SELECT telegram_id, coins, stars, is_blocked FROM players WHERE id=$1 AND deleted_at IS NULL FOR UPDATE`, playerID).
			Scan(&archivedTelegramID, &coins, &stars, &blocked); err != nil {
			if db.IsNoRows(err) {
				return apperr.NotFound
			}
			return err
		}
		if blocked {
			return refused("The player is blocked: unblock first (a reset must not lift a block)")
		}
		var wd, gifts, topups int
		if err := tx.QueryRow(ctx, `SELECT
			(SELECT count(*) FROM withdrawals WHERE player_id=$1 AND status IN ('PENDING','PROCESSING')),
			(SELECT count(*) FROM player_gifts WHERE player_id=$1 AND status='CLAIM_REQUESTED'),
			(SELECT count(*) FROM topup_orders WHERE player_id=$1 AND status='PENDING')`, playerID).Scan(&wd, &gifts, &topups); err != nil {
			return err
		}
		if wd > 0 || gifts > 0 || topups > 0 {
			return refused("The player has money on its way (%d withdrawal, %d gift claim, %d top-up not finished): finish or cancel them first", wd, gifts, topups)
		}
		meta := map[string]any{"admin": adminID, "reason": reason, "reset": true}
		if coins != 0 {
			if _, err := ledger.Apply(ctx, tx, ledger.Entry{PlayerID: playerID, Currency: ledger.Coin, Amount: -coins, Reason: ledger.ReasonAdminAdjust,
				RefType: "admin", RefID: ledger.RefID(adminID), Meta: meta}); err != nil {
				return err
			}
		}
		if stars != 0 {
			if _, err := ledger.Apply(ctx, tx, ledger.Entry{PlayerID: playerID, Currency: ledger.Star, Amount: -stars, Reason: ledger.ReasonAdminAdjust,
				RefType: "admin", RefID: ledger.RefID(adminID), Meta: meta, AllowNegative: true}); err != nil {
				return err
			}
		}
		for _, q := range []string{
			`DELETE FROM notifications WHERE player_id=$1`,
			`DELETE FROM idempotency_keys WHERE player_id=$1`,
			`DELETE FROM pending_referrals WHERE telegram_id=(SELECT telegram_id FROM players WHERE id=$1)`,
		} {
			if _, err := tx.Exec(ctx, q, playerID); err != nil {
				return err
			}
		}
		// archived like an erased player (the Telegram id is freed, so the same person signs up again as a new one), but nothing is anonymised
		_, err := tx.Exec(ctx, `UPDATE players SET telegram_id = -id, deleted_at = now(), reset_at = now(), reset_telegram_id = $2, bot_started = FALSE WHERE id=$1`,
			playerID, archivedTelegramID)
		return err
	})
	return archivedTelegramID, err
}
