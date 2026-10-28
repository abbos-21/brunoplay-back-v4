-- +goose Up
-- A provider that reports an ad when it STARTS (TADS) gets a wait: the proof is COMPLETED at once but can be spent only from usable_at on.
ALTER TABLE ad_views ADD COLUMN usable_at TIMESTAMPTZ;

-- +goose Down
ALTER TABLE ad_views DROP COLUMN usable_at;
