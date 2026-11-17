-- +goose Up
-- How many times an automatic payout was handed to the sender. An unclear one is sent again only when the chain shows the first
-- transfer never happened (and its 3-minute validity is long over), and only once.
ALTER TABLE withdrawals ADD COLUMN auto_attempts INT NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE withdrawals DROP COLUMN auto_attempts;
