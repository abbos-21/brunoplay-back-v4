-- +goose Up
-- The live TON price of stars (from Fragment), one row per quoted amount of stars (a pack plus its bonus).
CREATE TABLE ton_quotes (
    stars      BIGINT      PRIMARY KEY,
    price_nano BIGINT      NOT NULL CHECK (price_nano > 0),
    fetched_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE ton_quotes;
