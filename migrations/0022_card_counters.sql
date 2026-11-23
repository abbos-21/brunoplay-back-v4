-- +goose Up
-- Card requirements that count what the player does ("collect the tank 10 times", "finish 3 rides in Bruno's Ride").
-- player_counters: how many times the player did it (counted from now on). card_req_baselines: the counter's value at the moment a
-- requirement started to apply to the player (the player reached the level before it), so only what they do after that counts.
CREATE TABLE player_counters (
    player_id BIGINT NOT NULL REFERENCES players(id) ON DELETE CASCADE,
    kind      TEXT   NOT NULL,
    total     BIGINT NOT NULL DEFAULT 0,
    PRIMARY KEY (player_id, kind)
);

CREATE TABLE card_req_baselines (
    player_id BIGINT NOT NULL REFERENCES players(id) ON DELETE CASCADE,
    card      TEXT   NOT NULL,
    level     INT    NOT NULL,           -- the level that has the requirement (the one the player is working towards)
    req_type  TEXT   NOT NULL,
    base      BIGINT NOT NULL DEFAULT 0,
    PRIMARY KEY (player_id, card, level, req_type)
);

-- +goose Down
DROP TABLE card_req_baselines;
DROP TABLE player_counters;
