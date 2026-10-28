-- +goose Up
-- Which ad providers a proof offered, in priority order ("adsgram,tads"). A provider's reward callback can only complete a proof that offered it.
ALTER TABLE ad_views ADD COLUMN providers TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE ad_views DROP COLUMN providers;
