-- +goose Up
-- Operations: daily activity, pre-aggregated statistics for the dashboard, risk bookkeeping.

CREATE TABLE player_days (
    player_id BIGINT NOT NULL,
    day       DATE   NOT NULL,
    PRIMARY KEY (day, player_id)
);

CREATE TABLE daily_stats (
    day    DATE    NOT NULL,
    metric TEXT    NOT NULL,
    dim    TEXT    NOT NULL DEFAULT '',
    value  NUMERIC NOT NULL DEFAULT 0,
    PRIMARY KEY (day, metric, dim)
);

ALTER TABLE players ADD COLUMN risk_cleared_at TIMESTAMPTZ;

-- +goose Down
ALTER TABLE players DROP COLUMN risk_cleared_at;
DROP TABLE daily_stats;
DROP TABLE player_days;
