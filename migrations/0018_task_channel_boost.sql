-- +goose Up
-- A new task type: boost a channel (the player has to boost the channel; the bot checks it, for any channel the owner names).
ALTER TABLE tasks DROP CONSTRAINT tasks_type_check;
ALTER TABLE tasks ADD CONSTRAINT tasks_type_check CHECK (type IN ('CHANNEL_JOIN','CHANNEL_BOOST','INVITE_FRIENDS','OPEN_LINK','WATCH_ADS'));

-- +goose Down
DELETE FROM task_progress WHERE task_id IN (SELECT id FROM tasks WHERE type = 'CHANNEL_BOOST');
DELETE FROM tasks WHERE type = 'CHANNEL_BOOST';
ALTER TABLE tasks DROP CONSTRAINT tasks_type_check;
ALTER TABLE tasks ADD CONSTRAINT tasks_type_check CHECK (type IN ('CHANNEL_JOIN','INVITE_FRIENDS','OPEN_LINK','WATCH_ADS'));
