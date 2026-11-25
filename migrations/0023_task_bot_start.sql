-- +goose Up
-- A new task type: start the bot (the player presses Start in the bot; the bot's webhook sets players.bot_started, the task checks it).
ALTER TABLE tasks DROP CONSTRAINT tasks_type_check;
ALTER TABLE tasks ADD CONSTRAINT tasks_type_check CHECK (type IN ('CHANNEL_JOIN','CHANNEL_BOOST','INVITE_FRIENDS','OPEN_LINK','WATCH_ADS','BOT_START'));

-- +goose Down
DELETE FROM task_progress WHERE task_id IN (SELECT id FROM tasks WHERE type = 'BOT_START');
DELETE FROM tasks WHERE type = 'BOT_START';
ALTER TABLE tasks DROP CONSTRAINT tasks_type_check;
ALTER TABLE tasks ADD CONSTRAINT tasks_type_check CHECK (type IN ('CHANNEL_JOIN','CHANNEL_BOOST','INVITE_FRIENDS','OPEN_LINK','WATCH_ADS'));
