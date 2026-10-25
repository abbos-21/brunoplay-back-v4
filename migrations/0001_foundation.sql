-- +goose Up
-- Foundation: players, ledger, settings, admins, audit, idempotency.

CREATE TABLE players (
    id               BIGSERIAL PRIMARY KEY,
    telegram_id      BIGINT      NOT NULL UNIQUE,
    username         TEXT        NOT NULL DEFAULT '',
    first_name       TEXT        NOT NULL DEFAULT '',
    last_name        TEXT        NOT NULL DEFAULT '',
    photo_url        TEXT        NOT NULL DEFAULT '',
    language         TEXT        NOT NULL DEFAULT 'EN' CHECK (language IN ('EN','RU')),
    is_premium       BOOLEAN     NOT NULL DEFAULT FALSE,

    coins            BIGINT      NOT NULL DEFAULT 0 CHECK (coins >= 0),
    stars            BIGINT      NOT NULL DEFAULT 0,          -- may go negative after a refund (flagged for admin)
    total_coins_earned BIGINT    NOT NULL DEFAULT 0,

    level            INT         NOT NULL DEFAULT 1,
    mining_level     INT         NOT NULL DEFAULT 1,
    capacity_level   INT         NOT NULL DEFAULT 1,
    best_ride_score  BIGINT      NOT NULL DEFAULT 0,

    is_blocked       BOOLEAN     NOT NULL DEFAULT FALSE,
    block_reason     TEXT        NOT NULL DEFAULT '',
    hold_withdrawals BOOLEAN     NOT NULL DEFAULT FALSE,      -- automatic or manual hold (risk)
    hold_rewards     BOOLEAN     NOT NULL DEFAULT FALSE,
    risk_score       INT         NOT NULL DEFAULT 0,
    flags            JSONB       NOT NULL DEFAULT '[]'::jsonb,
    admin_note       TEXT        NOT NULL DEFAULT '',

    referred_by      BIGINT      REFERENCES players(id),
    referral_counted BOOLEAN     NOT NULL DEFAULT FALSE,      -- counts as an invited friend (reached level 2)
    invite_bonus_coins BIGINT,                                -- per-inviter override of the friend join bonus (NULL = default)
    friend_bonus_coins BIGINT    NOT NULL DEFAULT 0,          -- snapshot of the bonus promised to this player at join

    country          TEXT        NOT NULL DEFAULT '',
    last_ip          TEXT        NOT NULL DEFAULT '',
    terms_accepted_at TIMESTAMPTZ,

    bot_started      BOOLEAN     NOT NULL DEFAULT FALSE,
    bot_blocked      BOOLEAN     NOT NULL DEFAULT FALSE,
    notif_off        JSONB       NOT NULL DEFAULT '[]'::jsonb, -- categories the player switched off
    notif_all_off    BOOLEAN     NOT NULL DEFAULT FALSE,
    active_hours     INT[]       NOT NULL DEFAULT '{}',        -- learned UTC hours the player usually opens the app

    deleted_at       TIMESTAMPTZ,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX players_referred_by_idx ON players (referred_by) WHERE referred_by IS NOT NULL;
CREATE INDEX players_created_idx ON players (created_at);
CREATE INDEX players_last_seen_idx ON players (last_seen_at);
CREATE INDEX players_username_idx ON players (lower(username)) WHERE username <> '';

-- Append-only ledger: every change of coins/stars is a row.
CREATE TABLE ledger_entries (
    id            BIGSERIAL PRIMARY KEY,
    player_id     BIGINT      NOT NULL REFERENCES players(id),
    currency      TEXT        NOT NULL CHECK (currency IN ('COIN','STAR')),
    amount        BIGINT      NOT NULL CHECK (amount <> 0),   -- signed
    balance_after BIGINT      NOT NULL,
    reason        TEXT        NOT NULL,
    ref_type      TEXT        NOT NULL DEFAULT '',
    ref_id        TEXT        NOT NULL DEFAULT '',
    meta          JSONB       NOT NULL DEFAULT '{}'::jsonb,
    idem_key      TEXT,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX ledger_player_idx ON ledger_entries (player_id, id DESC);
CREATE INDEX ledger_reason_idx ON ledger_entries (reason, created_at);
CREATE UNIQUE INDEX ledger_idem_uidx ON ledger_entries (idem_key) WHERE idem_key IS NOT NULL;

-- Append-only: forbid UPDATE and DELETE on the ledger.
-- +goose StatementBegin
CREATE FUNCTION ledger_immutable() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION 'ledger_entries is append-only';
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd
CREATE TRIGGER ledger_no_update BEFORE UPDATE OR DELETE ON ledger_entries
    FOR EACH ROW EXECUTE FUNCTION ledger_immutable();

CREATE TABLE settings (
    key        TEXT PRIMARY KEY,
    value      JSONB       NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_by BIGINT
);
CREATE TABLE settings_history (
    id         BIGSERIAL PRIMARY KEY,
    key        TEXT        NOT NULL,
    old_value  JSONB,
    new_value  JSONB       NOT NULL,
    changed_by BIGINT,
    changed_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX settings_history_key_idx ON settings_history (key, id DESC);

CREATE TABLE admins (
    id            BIGSERIAL PRIMARY KEY,
    email         TEXT        NOT NULL UNIQUE,
    name          TEXT        NOT NULL DEFAULT '',
    password_hash TEXT        NOT NULL,
    role          TEXT        NOT NULL CHECK (role IN ('super','finance','support','content')),
    is_active     BOOLEAN     NOT NULL DEFAULT TRUE,
    failed_logins INT         NOT NULL DEFAULT 0,
    locked_until  TIMESTAMPTZ,
    last_login_at TIMESTAMPTZ,
    last_login_ip TEXT        NOT NULL DEFAULT '',
    telegram_id   BIGINT,                                     -- optional: receive login alerts in the bot
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE admin_sessions (
    id          BIGSERIAL PRIMARY KEY,
    admin_id    BIGINT      NOT NULL REFERENCES admins(id) ON DELETE CASCADE,
    token_hash  TEXT        NOT NULL UNIQUE,                  -- sha256 of the refresh token
    ip          TEXT        NOT NULL DEFAULT '',
    user_agent  TEXT        NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at  TIMESTAMPTZ NOT NULL,
    revoked_at  TIMESTAMPTZ
);

CREATE TABLE audit_log (
    id          BIGSERIAL PRIMARY KEY,
    admin_id    BIGINT,
    admin_email TEXT        NOT NULL DEFAULT '',
    action      TEXT        NOT NULL,
    target_type TEXT        NOT NULL DEFAULT '',
    target_id   TEXT        NOT NULL DEFAULT '',
    before      JSONB,
    after       JSONB,
    ip          TEXT        NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX audit_created_idx ON audit_log (id DESC);
CREATE INDEX audit_target_idx ON audit_log (target_type, target_id);

CREATE TABLE idempotency_keys (
    key         TEXT        NOT NULL,
    player_id   BIGINT      NOT NULL,
    endpoint    TEXT        NOT NULL,
    status      INT         NOT NULL DEFAULT 0,               -- 0 = still running
    response    BYTEA,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (player_id, key)
);
CREATE INDEX idempotency_created_idx ON idempotency_keys (created_at);

-- +goose Down
DROP TABLE idempotency_keys;
DROP TABLE audit_log;
DROP TABLE admin_sessions;
DROP TABLE admins;
DROP TABLE settings_history;
DROP TABLE settings;
DROP TRIGGER ledger_no_update ON ledger_entries;
DROP FUNCTION ledger_immutable();
DROP TABLE ledger_entries;
DROP TABLE players;
