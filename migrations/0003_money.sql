-- +goose Up
-- Stars top-up (Telegram Stars, TON) and withdrawals (coins -> TON).

CREATE TABLE topup_orders (
    id            TEXT        PRIMARY KEY,
    player_id     BIGINT      NOT NULL REFERENCES players(id),
    method        TEXT        NOT NULL CHECK (method IN ('TG_STARS','TON')),
    status        TEXT        NOT NULL CHECK (status IN ('PENDING','PAID','FAILED','EXPIRED','REFUNDED')),
    pack_stars    BIGINT      NOT NULL,                  -- stars bought
    bonus_stars   BIGINT      NOT NULL DEFAULT 0,        -- bonus on top (admin %, stored at creation)
    bonus_percent NUMERIC     NOT NULL DEFAULT 0,
    price         BIGINT      NOT NULL,                  -- Telegram Stars (XTR) or nanoTON to pay
    ton_comment   TEXT,                                  -- unique memo the player's wallet must send
    ton_wallet    TEXT        NOT NULL DEFAULT '',
    invoice_link  TEXT        NOT NULL DEFAULT '',
    tg_charge_id  TEXT,
    ton_tx_hash   TEXT,
    paid_amount   BIGINT      NOT NULL DEFAULT 0,        -- what actually arrived (nanoTON / XTR)
    note          TEXT        NOT NULL DEFAULT '',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at    TIMESTAMPTZ NOT NULL,
    paid_at       TIMESTAMPTZ
);
CREATE INDEX topup_player_idx ON topup_orders (player_id, created_at DESC);
CREATE INDEX topup_status_idx ON topup_orders (status, created_at);
CREATE UNIQUE INDEX topup_comment_uidx ON topup_orders (ton_comment) WHERE ton_comment IS NOT NULL;
CREATE UNIQUE INDEX topup_charge_uidx ON topup_orders (tg_charge_id) WHERE tg_charge_id IS NOT NULL;
CREATE UNIQUE INDEX topup_txhash_uidx ON topup_orders (ton_tx_hash) WHERE ton_tx_hash IS NOT NULL;

CREATE TABLE withdrawals (
    id               BIGSERIAL PRIMARY KEY,
    player_id        BIGINT      NOT NULL REFERENCES players(id),
    amount_coins     BIGINT      NOT NULL CHECK (amount_coins > 0),
    coins_per_ton    NUMERIC     NOT NULL,               -- rate valid when the request was made
    fee_nano         BIGINT      NOT NULL,               -- fee valid when the request was made
    amount_nano      BIGINT      NOT NULL CHECK (amount_nano > 0), -- what the player receives
    address          TEXT        NOT NULL,
    status           TEXT        NOT NULL CHECK (status IN ('PENDING','PROCESSING','COMPLETED','FAILED','CANCELLED')),
    auto             BOOLEAN     NOT NULL DEFAULT FALSE,   -- eligible for the automatic payout job
    tx_hash          TEXT,
    payout_id        TEXT        UNIQUE,                  -- memo of the outgoing transfer, set BEFORE sending
    fail_reason      TEXT        NOT NULL DEFAULT '',
    needs_review     BOOLEAN     NOT NULL DEFAULT FALSE,   -- an automatic payout ended unclear: never resend, an admin decides
    admin_id         BIGINT,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    payout_started_at TIMESTAMPTZ,
    processed_at     TIMESTAMPTZ
);
CREATE INDEX withdrawals_player_idx ON withdrawals (player_id, id DESC);
CREATE INDEX withdrawals_status_idx ON withdrawals (status, id);
CREATE UNIQUE INDEX withdrawals_one_open ON withdrawals (player_id) WHERE status IN ('PENDING','PROCESSING');

-- +goose Down
DROP TABLE withdrawals;
DROP TABLE topup_orders;
