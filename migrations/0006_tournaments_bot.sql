-- +goose Up
-- Tournaments, the notification queue, templates, broadcasts, bot users.

ALTER TABLE players ADD COLUMN pref_hour SMALLINT;                       -- the UTC hour the player usually opens the app
CREATE INDEX players_pref_hour_idx ON players (pref_hour) WHERE bot_started AND NOT bot_blocked;

CREATE TABLE tournaments (
    id            BIGSERIAL   PRIMARY KEY,
    name_en       TEXT        NOT NULL,
    name_ru       TEXT        NOT NULL,
    desc_en       TEXT        NOT NULL DEFAULT '',
    desc_ru       TEXT        NOT NULL DEFAULT '',
    metric        TEXT        NOT NULL CHECK (metric IN ('STARS_SPENT','RIDE_BEST','COINS_MINED','FRIENDS_INVITED')),
    starts_at     TIMESTAMPTZ NOT NULL,
    ends_at       TIMESTAMPTZ NOT NULL,
    entry_fee     BIGINT      NOT NULL DEFAULT 0 CHECK (entry_fee >= 0),
    prizes        JSONB       NOT NULL DEFAULT '[]'::jsonb,              -- [{place, coins, stars, gift}]
    created_by    BIGINT,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    cancelled_at  TIMESTAMPTZ,
    paid_at       TIMESTAMPTZ,
    start_notified_at  TIMESTAMPTZ,
    ending_notified_at TIMESTAMPTZ,
    CHECK (ends_at > starts_at)
);
CREATE INDEX tournaments_time_idx ON tournaments (ends_at, starts_at);

CREATE TABLE tournament_entries (
    tournament_id BIGINT      NOT NULL REFERENCES tournaments(id) ON DELETE CASCADE,
    player_id     BIGINT      NOT NULL REFERENCES players(id),
    joined_at     TIMESTAMPTZ NOT NULL,
    score         BIGINT      NOT NULL DEFAULT 0,
    score_at      TIMESTAMPTZ NOT NULL,                  -- when the current score was reached (tie-break: earlier wins)
    disqualified  BOOLEAN     NOT NULL DEFAULT FALSE,
    PRIMARY KEY (tournament_id, player_id)
);
CREATE INDEX tournament_rank_idx ON tournament_entries (tournament_id, score DESC, score_at ASC) WHERE NOT disqualified;
CREATE INDEX tournament_player_idx ON tournament_entries (player_id);

CREATE TABLE tournament_payouts (
    tournament_id BIGINT      NOT NULL REFERENCES tournaments(id),
    player_id     BIGINT      NOT NULL REFERENCES players(id),
    place         INT         NOT NULL,
    score         BIGINT      NOT NULL,
    coins         BIGINT      NOT NULL DEFAULT 0,
    stars         BIGINT      NOT NULL DEFAULT 0,
    gift          TEXT        NOT NULL DEFAULT '',
    paid_at       TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (tournament_id, player_id)
);

-- the bot sends everything through this queue
CREATE TABLE notifications (
    id          BIGSERIAL   PRIMARY KEY,
    player_id   BIGINT      NOT NULL REFERENCES players(id),
    type        TEXT        NOT NULL,
    params      JSONB       NOT NULL DEFAULT '{}'::jsonb,
    notify_at   TIMESTAMPTZ NOT NULL,
    dedupe_key  TEXT        UNIQUE,
    status      TEXT        NOT NULL DEFAULT 'PENDING' CHECK (status IN ('PENDING','SENDING','SENT','SKIPPED','FAILED','CANCELLED')),
    reason      TEXT        NOT NULL DEFAULT '',
    attempts    INT         NOT NULL DEFAULT 0,
    broadcast_id BIGINT,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    sent_at     TIMESTAMPTZ
);
CREATE INDEX notifications_due_idx ON notifications (notify_at) WHERE status = 'PENDING';
CREATE INDEX notifications_player_idx ON notifications (player_id, type, status);
CREATE INDEX notifications_sent_idx ON notifications (player_id, sent_at) WHERE status = 'SENT';
CREATE INDEX notifications_broadcast_idx ON notifications (broadcast_id) WHERE broadcast_id IS NOT NULL;

-- texts the admin changed (the defaults live in the code)
CREATE TABLE notification_templates (
    type        TEXT        NOT NULL,
    lang        TEXT        NOT NULL CHECK (lang IN ('EN','RU')),
    text        TEXT        NOT NULL,
    button      TEXT        NOT NULL DEFAULT '',
    updated_by  BIGINT,
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (type, lang)
);

CREATE TABLE broadcasts (
    id            BIGSERIAL   PRIMARY KEY,
    title         TEXT        NOT NULL,
    text_en       TEXT        NOT NULL,
    text_ru       TEXT        NOT NULL,
    button_en     TEXT        NOT NULL DEFAULT '',
    button_ru     TEXT        NOT NULL DEFAULT '',
    button_path   TEXT        NOT NULL DEFAULT '',
    photo_url     TEXT        NOT NULL DEFAULT '',
    segment       JSONB       NOT NULL DEFAULT '{}'::jsonb,
    status        TEXT        NOT NULL DEFAULT 'DRAFT' CHECK (status IN ('DRAFT','SCHEDULED','SENDING','DONE','CANCELLED')),
    scheduled_at  TIMESTAMPTZ,
    test_sent_at  TIMESTAMPTZ,
    queued        BIGINT      NOT NULL DEFAULT 0,
    created_by    BIGINT,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    started_at    TIMESTAMPTZ
);

-- people who pressed /start in the bot (also before they ever opened the app)
CREATE TABLE bot_users (
    telegram_id BIGINT      PRIMARY KEY,
    started_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    lang_code   TEXT        NOT NULL DEFAULT ''
);

-- run-once markers of the scheduled batches (so a batch for one hour runs once even with several workers)
CREATE TABLE job_marks (
    name TEXT        NOT NULL,
    key  TEXT        NOT NULL,
    at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (name, key)
);

-- +goose Down
DROP TABLE job_marks;
DROP TABLE bot_users;
DROP TABLE broadcasts;
DROP TABLE notification_templates;
DROP TABLE notifications;
DROP TABLE tournament_payouts;
DROP TABLE tournament_entries;
DROP TABLE tournaments;
DROP INDEX players_pref_hour_idx;
ALTER TABLE players DROP COLUMN pref_hour;
