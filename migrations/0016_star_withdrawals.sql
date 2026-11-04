-- +goose Up
-- A player asks to take game stars out as real Telegram Stars. The stars leave the balance at once; an admin sends the Stars by hand
-- and marks the request DELIVERED, or CANCELLED (the stars return to the player with the reason).
CREATE TABLE star_withdrawals (
    id            BIGSERIAL PRIMARY KEY,
    player_id     BIGINT      NOT NULL REFERENCES players(id),
    stars         BIGINT      NOT NULL CHECK (stars > 0),
    status        TEXT        NOT NULL DEFAULT 'PENDING' CHECK (status IN ('PENDING','DELIVERED','CANCELLED')),
    reject_reason TEXT        NOT NULL DEFAULT '',
    admin_id      BIGINT,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    processed_at  TIMESTAMPTZ
);
CREATE INDEX star_withdrawals_status_idx ON star_withdrawals (status, id);
CREATE INDEX star_withdrawals_player_idx ON star_withdrawals (player_id, id);
-- one open request per player, enforced by the database too
CREATE UNIQUE INDEX star_withdrawals_one_open_uidx ON star_withdrawals (player_id) WHERE status = 'PENDING';

-- +goose Down
DROP TABLE star_withdrawals;
