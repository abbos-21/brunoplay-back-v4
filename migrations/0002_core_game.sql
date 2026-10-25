-- +goose Up
-- Core game: ad proofs, fishing sessions, boosters, per-day counters.

CREATE TABLE ad_views (
    id           TEXT        PRIMARY KEY,
    player_id    BIGINT      NOT NULL REFERENCES players(id),
    placement    TEXT        NOT NULL,
    status       TEXT        NOT NULL CHECK (status IN ('PENDING','COMPLETED','USED','EXPIRED')),
    block_id     TEXT        NOT NULL DEFAULT '',
    fallback     BOOLEAN     NOT NULL DEFAULT FALSE,   -- allowed without a real ad (no-fill fallback)
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at TIMESTAMPTZ,
    used_at      TIMESTAMPTZ,
    expires_at   TIMESTAMPTZ NOT NULL,
    used_for     TEXT        NOT NULL DEFAULT ''
);
CREATE INDEX ad_views_player_idx ON ad_views (player_id, status, created_at DESC);
CREATE INDEX ad_views_created_idx ON ad_views (created_at);

CREATE TABLE ad_failures (
    player_id  BIGINT      NOT NULL REFERENCES players(id),
    placement  TEXT        NOT NULL,
    count      INT         NOT NULL DEFAULT 0,
    last_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (player_id, placement)
);

CREATE TABLE fishing_sessions (
    id              BIGSERIAL PRIMARY KEY,
    player_id       BIGINT      NOT NULL REFERENCES players(id),
    status          TEXT        NOT NULL CHECK (status IN ('RUNNING','COLLECTED')),
    started_at      TIMESTAMPTZ NOT NULL,
    settled_micro   BIGINT      NOT NULL DEFAULT 0,        -- tank content at settled_at (micro-coins)
    settled_at      TIMESTAMPTZ NOT NULL,
    base_rate_micro BIGINT      NOT NULL,                  -- micro-coins per second without a booster
    capacity_micro  BIGINT      NOT NULL,
    full_at         TIMESTAMPTZ,                           -- when the tank is/was full (NULL = never)
    start_ad_id     TEXT,
    collect_ad_id   TEXT,
    collected_coins BIGINT,
    collected_at    TIMESTAMPTZ,
    autocollected   BOOLEAN     NOT NULL DEFAULT FALSE
);
CREATE UNIQUE INDEX fishing_one_running ON fishing_sessions (player_id) WHERE status = 'RUNNING';
CREATE INDEX fishing_full_idx ON fishing_sessions (full_at) WHERE status = 'RUNNING';
CREATE INDEX fishing_player_idx ON fishing_sessions (player_id, id DESC);
CREATE UNIQUE INDEX fishing_collect_ad_uidx ON fishing_sessions (collect_ad_id) WHERE collect_ad_id IS NOT NULL;
CREATE UNIQUE INDEX fishing_start_ad_uidx ON fishing_sessions (start_ad_id) WHERE start_ad_id IS NOT NULL;

CREATE TABLE player_boosters (
    id          BIGSERIAL PRIMARY KEY,
    player_id   BIGINT      NOT NULL REFERENCES players(id),
    type        TEXT        NOT NULL CHECK (type IN ('MINING_BOOST_1_5X','AUTO_MINING')),
    starts_at   TIMESTAMPTZ NOT NULL,
    expires_at  TIMESTAMPTZ NOT NULL,
    price_stars BIGINT      NOT NULL DEFAULT 0,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX boosters_player_idx ON player_boosters (player_id, type, expires_at DESC);
CREATE INDEX boosters_expiry_idx ON player_boosters (expires_at);

-- generic per-player, per-game-day counters (bonus ads, extra combo attempts, free Ride runs, Ride coins...)
CREATE TABLE daily_counters (
    player_id BIGINT NOT NULL REFERENCES players(id),
    day       DATE   NOT NULL,
    key       TEXT   NOT NULL,
    value     BIGINT NOT NULL DEFAULT 0,
    PRIMARY KEY (player_id, day, key)
);

-- +goose Down
DROP TABLE daily_counters;
DROP TABLE player_boosters;
DROP TABLE fishing_sessions;
DROP TABLE ad_failures;
DROP TABLE ad_views;
