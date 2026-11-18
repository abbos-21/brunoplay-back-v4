-- +goose Up
-- The daily combo posts to the ambassadors channel: one row per game day, so a day is posted once even if the worker restarts.
-- message_id 0 = claimed but not sent yet (it is claimed again after a few minutes if the send never finished).
CREATE TABLE combo_posts (
    day        DATE        PRIMARY KEY,
    chat       TEXT        NOT NULL,
    message_id BIGINT      NOT NULL DEFAULT 0,
    attempts   INT         NOT NULL DEFAULT 0,
    claimed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    posted_at  TIMESTAMPTZ
);

-- +goose Down
DROP TABLE combo_posts;
