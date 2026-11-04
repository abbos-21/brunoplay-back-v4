-- Reset the game: remove every PLAYER and everything that belongs to players, keep everything the admins built.
-- Run it through deploy/reset-game.sh (it takes a backup first and stops the app while this runs). The whole file is ONE transaction:
-- if anything fails (for example a withdrawal is still in progress) nothing is changed.
--
-- REMOVED
--   players and every table that points at a player (balances, the ledger, fishing, boosters, daily streak, combo attempts, task progress,
--   referrals, gifts, spins, chests, rides, ad proofs, top-up orders, withdrawals, star withdrawals, notifications, tournament entries and payouts)
--   all tournaments
--   the live winners list, idempotency keys
-- KEPT
--   settings and their history, admin accounts, sessions and the audit log
--   tasks, daily combo images and patterns, notification templates, broadcasts, gift pictures
--   dashboard statistics (daily_stats, player_days) and payout history (payout_stats, guard_events)
--   bot_users (who started the bot: they really did, so the bot can still reach them)
-- Player ids are NOT restarted: new players get new ids, so a kept row that names an old player never points at a new one.

-- money in flight must be settled first (the admin panel: Withdrawals, Star withdrawals, Gift claims, Top-ups)
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM withdrawals WHERE status IN ('PENDING', 'PROCESSING')) THEN
        RAISE EXCEPTION 'There are withdrawals in progress: finish or reject them in the admin panel first';
    END IF;
    IF EXISTS (SELECT 1 FROM star_withdrawals WHERE status = 'PENDING') THEN
        RAISE EXCEPTION 'There are star withdrawals waiting: deliver or cancel them in the admin panel first';
    END IF;
    IF EXISTS (SELECT 1 FROM player_gifts WHERE status = 'CLAIM_REQUESTED') THEN
        RAISE EXCEPTION 'There are gift claims waiting: deliver or reject them in the admin panel first';
    END IF;
    IF EXISTS (SELECT 1 FROM topup_orders WHERE status = 'PENDING' AND expires_at > now()) THEN
        RAISE EXCEPTION 'There are top-up orders waiting for a payment: wait until they expire (or are paid) first';
    END IF;
END $$;

-- CASCADE follows the foreign keys: every table that points at a player (or at a tournament) is emptied with it
TRUNCATE players, tournaments CASCADE;

-- player data that has no foreign key
TRUNCATE winners_feed, idempotency_keys;
