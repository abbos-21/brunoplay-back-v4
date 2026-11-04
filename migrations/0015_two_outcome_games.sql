-- +goose Up
-- The smart prize system is gone (its settings and code were removed).
DROP TABLE IF EXISTS smart_state;

-- A chest round of the new game has 3 chests; one of them holds the prize and the player opens exactly one.
-- Rounds that were open before this version (slot_mode = false) are opened in full by the worker with the old rules.
ALTER TABLE chest_rounds ADD COLUMN slot_mode BOOLEAN NOT NULL DEFAULT FALSE;

-- +goose Down
ALTER TABLE chest_rounds DROP COLUMN slot_mode;
CREATE TABLE smart_state (
    player_id     BIGINT           NOT NULL REFERENCES players(id),
    game          TEXT             NOT NULL CHECK (game IN ('spin','chest')),
    plays         BIGINT           NOT NULL DEFAULT 0,
    welcome_used  BIGINT           NOT NULL DEFAULT 0,
    welcome_done  BOOLEAN          NOT NULL DEFAULT FALSE,
    spent         DOUBLE PRECISION NOT NULL DEFAULT 0,
    paid          DOUBLE PRECISION NOT NULL DEFAULT 0,
    updated_at    TIMESTAMPTZ      NOT NULL DEFAULT now(),
    PRIMARY KEY (player_id, game)
);
