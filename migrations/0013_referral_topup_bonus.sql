-- +goose Up
-- The inviter earns a share (in stars) of what a friend tops up with Telegram Stars. Those payments sit in referral_payments next to the
-- coin ones, with their own stars column (a refund of the top-up is a second row with a negative amount).
ALTER TABLE referral_payments DROP CONSTRAINT referral_payments_kind_check;
ALTER TABLE referral_payments ADD CONSTRAINT referral_payments_kind_check CHECK (kind IN ('level','mining','topup'));
ALTER TABLE referral_payments ADD COLUMN stars BIGINT NOT NULL DEFAULT 0;

-- +goose Down
DELETE FROM referral_payments WHERE kind = 'topup';
ALTER TABLE referral_payments DROP COLUMN stars;
ALTER TABLE referral_payments DROP CONSTRAINT referral_payments_kind_check;
ALTER TABLE referral_payments ADD CONSTRAINT referral_payments_kind_check CHECK (kind IN ('level','mining'));
