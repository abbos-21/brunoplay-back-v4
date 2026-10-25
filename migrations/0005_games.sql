-- +goose Up
-- Games: gifts collection, spin draws, chest rounds, Ride runs, payout statistics and the winners feed.

CREATE TABLE player_gifts (
    id                 BIGSERIAL   PRIMARY KEY,
    player_id          BIGINT      NOT NULL REFERENCES players(id),
    gift_key           TEXT        NOT NULL,
    star_value         BIGINT      NOT NULL,                -- value when it was won (sell price)
    status             TEXT        NOT NULL CHECK (status IN ('OWNED','SOLD','CLAIM_REQUESTED','DELIVERED')),
    source             TEXT        NOT NULL,                -- spin | chest | tournament
    source_ref         TEXT        NOT NULL DEFAULT '',
    reject_reason      TEXT        NOT NULL DEFAULT '',
    admin_id           BIGINT,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    sold_at            TIMESTAMPTZ,
    claim_requested_at TIMESTAMPTZ,
    delivered_at       TIMESTAMPTZ
);
CREATE INDEX player_gifts_player_idx ON player_gifts (player_id, id DESC);
CREATE INDEX player_gifts_status_idx ON player_gifts (status, claim_requested_at) WHERE status = 'CLAIM_REQUESTED';

CREATE TABLE spin_draws (
    id          BIGSERIAL   PRIMARY KEY,
    player_id   BIGINT      NOT NULL REFERENCES players(id),
    tier        TEXT        NOT NULL,
    batch       TEXT        NOT NULL,                      -- one id per purchase (1x, 3x, 5x, 10x)
    gift_key    TEXT        NOT NULL,
    star_value  BIGINT      NOT NULL,
    price       BIGINT      NOT NULL,
    drawn_key   TEXT        NOT NULL,                      -- what the odds gave before the payout guard
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX spin_draws_player_idx ON spin_draws (player_id, id DESC);

CREATE TABLE chest_rounds (
    id           BIGSERIAL   PRIMARY KEY,
    player_id    BIGINT      NOT NULL REFERENCES players(id),
    status       TEXT        NOT NULL CHECK (status IN ('OPEN','DONE')),
    cost         BIGINT      NOT NULL,
    picks        INT         NOT NULL,
    prizes       JSONB       NOT NULL,                      -- every prize, drawn when the round was paid
    opened       JSONB       NOT NULL DEFAULT '{}'::jsonb,  -- slot -> index of the prize it revealed
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at  TIMESTAMPTZ,
    reminded_at  TIMESTAMPTZ
);
CREATE UNIQUE INDEX chest_one_open ON chest_rounds (player_id) WHERE status = 'OPEN';
CREATE INDEX chest_open_idx ON chest_rounds (created_at) WHERE status = 'OPEN';

CREATE TABLE ride_runs (
    id            BIGSERIAL   PRIMARY KEY,
    player_id     BIGINT      NOT NULL REFERENCES players(id),
    status        TEXT        NOT NULL CHECK (status IN ('STARTED','FINISHED','REJECTED','ABANDONED')),
    paid_with     TEXT        NOT NULL CHECK (paid_with IN ('STARS','AD','FREE')),
    ad_view_id    TEXT,
    started_at    TIMESTAMPTZ NOT NULL,
    expires_at    TIMESTAMPTZ NOT NULL,
    finished_at   TIMESTAMPTZ,
    claimed_coins INT,
    claimed_score BIGINT,
    paid_coins    BIGINT,
    duration_ms   BIGINT,
    reject_reason TEXT        NOT NULL DEFAULT ''
);
CREATE INDEX ride_runs_player_idx ON ride_runs (player_id, id DESC);

-- stars spent and value paid out per game and tier, per hour (the payout guard sums a rolling window)
CREATE TABLE payout_stats (
    game  TEXT        NOT NULL,
    tier  TEXT        NOT NULL,
    hour  TIMESTAMPTZ NOT NULL,
    spent BIGINT      NOT NULL DEFAULT 0,
    paid  BIGINT      NOT NULL DEFAULT 0,
    PRIMARY KEY (game, tier, hour)
);

CREATE TABLE guard_events (
    id          BIGSERIAL   PRIMARY KEY,
    player_id   BIGINT      NOT NULL,
    game        TEXT        NOT NULL,
    tier        TEXT        NOT NULL,
    from_key    TEXT        NOT NULL,
    to_key      TEXT        NOT NULL,
    from_value  BIGINT      NOT NULL,
    to_value    BIGINT      NOT NULL,
    ratio_after NUMERIC     NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX guard_events_idx ON guard_events (id DESC);

CREATE TABLE winners_feed (
    id          BIGSERIAL   PRIMARY KEY,
    player_id   BIGINT      NOT NULL,
    username    TEXT        NOT NULL DEFAULT '',
    game        TEXT        NOT NULL,
    prize_type  TEXT        NOT NULL,
    prize_value BIGINT      NOT NULL,
    gift_key    TEXT        NOT NULL DEFAULT '',
    spent_stars BIGINT      NOT NULL DEFAULT 0,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE winners_feed;
DROP TABLE guard_events;
DROP TABLE payout_stats;
DROP TABLE ride_runs;
DROP TABLE chest_rounds;
DROP TABLE spin_draws;
DROP TABLE player_gifts;
