-- +goose Up
-- Daily streak and combo, tasks, referrals.

ALTER TABLE players ADD COLUMN referral_counted_at TIMESTAMPTZ;

CREATE TABLE daily_streaks (
    player_id     BIGINT      PRIMARY KEY REFERENCES players(id),
    current       INT         NOT NULL DEFAULT 0,
    best          INT         NOT NULL DEFAULT 0,
    last_claim_day DATE
);

CREATE TABLE combo_images (
    id       SMALLINT    PRIMARY KEY CHECK (id BETWEEN 1 AND 8),
    name_en  TEXT        NOT NULL,
    name_ru  TEXT        NOT NULL,
    icon     TEXT        NOT NULL,                  -- image URL or an emoji
    active   BOOLEAN     NOT NULL DEFAULT TRUE
);
INSERT INTO combo_images (id, name_en, name_ru, icon) VALUES
    (1,'Apple','Яблоко','🍎'), (2,'Banana','Банан','🍌'), (3,'Pineapple','Ананас','🍍'), (4,'Orange','Апельсин','🍊'),
    (5,'Grapes','Виноград','🍇'), (6,'Cherry','Вишня','🍒'), (7,'Lemon','Лимон','🍋'), (8,'Watermelon','Арбуз','🍉');

CREATE TABLE combo_patterns (
    day        DATE        PRIMARY KEY,
    items      SMALLINT[]  NOT NULL,
    source     TEXT        NOT NULL CHECK (source IN ('admin','auto')),
    created_by BIGINT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- one row per player per game day: attempts, the best result and the claim
CREATE TABLE combo_days (
    player_id     BIGINT   NOT NULL REFERENCES players(id),
    day           DATE     NOT NULL,
    attempts      INT      NOT NULL DEFAULT 0,
    extra_used    INT      NOT NULL DEFAULT 0,
    best_correct  INT      NOT NULL DEFAULT 0,
    last_items    SMALLINT[],
    last_correct  BOOLEAN[],
    claimed       BOOLEAN  NOT NULL DEFAULT FALSE,
    claimed_coins BIGINT   NOT NULL DEFAULT 0,
    doubled       BOOLEAN  NOT NULL DEFAULT FALSE,
    PRIMARY KEY (player_id, day)
);

CREATE TABLE combo_attempts (
    id            BIGSERIAL   PRIMARY KEY,
    player_id     BIGINT      NOT NULL REFERENCES players(id),
    day           DATE        NOT NULL,
    attempt_no    INT         NOT NULL,
    items         SMALLINT[]  NOT NULL,
    correct       BOOLEAN[]   NOT NULL,
    correct_count INT         NOT NULL,
    ad_view_id    TEXT,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (player_id, day, attempt_no)
);

CREATE TABLE tasks (
    id          BIGSERIAL   PRIMARY KEY,
    category    TEXT        NOT NULL CHECK (category IN ('MAIN','OTHER')),
    type        TEXT        NOT NULL CHECK (type IN ('CHANNEL_JOIN','INVITE_FRIENDS','OPEN_LINK','WATCH_ADS')),
    title_en    TEXT        NOT NULL,
    title_ru    TEXT        NOT NULL,
    desc_en     TEXT        NOT NULL DEFAULT '',
    desc_ru     TEXT        NOT NULL DEFAULT '',
    url         TEXT        NOT NULL DEFAULT '',
    channel     TEXT        NOT NULL DEFAULT '',          -- @username or numeric chat id for CHANNEL_JOIN
    required    INT         NOT NULL DEFAULT 1,           -- friends to invite / ads to watch
    reward_coins BIGINT     NOT NULL DEFAULT 0,
    reward_stars BIGINT     NOT NULL DEFAULT 0,
    repeat_mode TEXT        NOT NULL DEFAULT 'ONCE' CHECK (repeat_mode IN ('ONCE','DAILY','WEEKLY')),
    starts_at   TIMESTAMPTZ,
    ends_at     TIMESTAMPTZ,
    active      BOOLEAN     NOT NULL DEFAULT TRUE,
    sort_order  INT         NOT NULL DEFAULT 0,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX tasks_active_idx ON tasks (active, category, sort_order);

CREATE TABLE task_progress (
    player_id    BIGINT      NOT NULL REFERENCES players(id),
    task_id      BIGINT      NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    period_key   TEXT        NOT NULL,                    -- 'once', '2026-10-24', '2026-W43'
    progress     INT         NOT NULL DEFAULT 0,
    completed    BOOLEAN     NOT NULL DEFAULT FALSE,
    opened_at    TIMESTAMPTZ,
    completed_at TIMESTAMPTZ,
    PRIMARY KEY (player_id, task_id, period_key)
);
CREATE INDEX task_progress_task_idx ON task_progress (task_id, completed);

CREATE TABLE task_seen (
    player_id    BIGINT      PRIMARY KEY REFERENCES players(id),
    last_seen_at TIMESTAMPTZ NOT NULL
);

CREATE TABLE referral_payments (
    id          BIGSERIAL   PRIMARY KEY,
    inviter_id  BIGINT      NOT NULL REFERENCES players(id),
    friend_id   BIGINT      NOT NULL REFERENCES players(id),
    kind        TEXT        NOT NULL CHECK (kind IN ('level','mining')),
    level       INT,
    coins       BIGINT      NOT NULL,
    ref         TEXT        NOT NULL UNIQUE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX referral_payments_inviter_idx ON referral_payments (inviter_id, created_at DESC);
CREATE INDEX referral_payments_friend_idx ON referral_payments (friend_id, kind);

-- a player who tapped the bot's invite link before ever opening the app
CREATE TABLE pending_referrals (
    telegram_id BIGINT      PRIMARY KEY,
    inviter_id  BIGINT      NOT NULL REFERENCES players(id),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE pending_referrals;
DROP TABLE referral_payments;
DROP TABLE task_seen;
DROP TABLE task_progress;
DROP TABLE tasks;
DROP TABLE combo_attempts;
DROP TABLE combo_days;
DROP TABLE combo_patterns;
DROP TABLE combo_images;
DROP TABLE daily_streaks;
ALTER TABLE players DROP COLUMN referral_counted_at;
