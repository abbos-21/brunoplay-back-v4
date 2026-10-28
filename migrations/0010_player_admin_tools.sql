-- +goose Up
-- Admin tools for one player: ads switched off for them, and "reset" (the old record is archived, the person starts again as a new player).
ALTER TABLE players ADD COLUMN ads_disabled BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE players ADD COLUMN reset_at TIMESTAMPTZ;
ALTER TABLE players ADD COLUMN reset_telegram_id BIGINT; -- the Telegram id the archived record had before the reset

-- +goose Down
ALTER TABLE players DROP COLUMN reset_telegram_id;
ALTER TABLE players DROP COLUMN reset_at;
ALTER TABLE players DROP COLUMN ads_disabled;
