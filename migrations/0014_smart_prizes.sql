-- +goose Up
-- The smart prize system (settings smart.*) remembers, per player and game, how many plays were made, how many welcome plays were used
-- and how many stars the player has spent and won in total. A game is 'spin' or 'chest'.
CREATE TABLE smart_state (
    player_id     BIGINT           NOT NULL REFERENCES players(id),
    game          TEXT             NOT NULL CHECK (game IN ('spin','chest')),
    plays         BIGINT           NOT NULL DEFAULT 0,
    welcome_used  BIGINT           NOT NULL DEFAULT 0,
    welcome_done  BOOLEAN          NOT NULL DEFAULT FALSE,   -- a player who already played before the system existed gets no welcome
    spent         DOUBLE PRECISION NOT NULL DEFAULT 0,       -- stars paid for plays since the system started counting
    paid          DOUBLE PRECISION NOT NULL DEFAULT 0,       -- stars' worth of prizes paid out
    updated_at    TIMESTAMPTZ      NOT NULL DEFAULT now(),
    PRIMARY KEY (player_id, game)
);

-- +goose Down
DROP TABLE smart_state;
