-- +goose Up
-- Posts to the public payouts channel. A row is created in the same transaction that completes a payout, and the worker sends it
-- (so a payout is never posted twice and a failed send is retried). One row per payout: (kind, ref_id) is unique.
CREATE TABLE payout_posts (
    id              BIGSERIAL   PRIMARY KEY,
    kind            TEXT        NOT NULL CHECK (kind IN ('ton', 'stars')),
    ref_id          BIGINT      NOT NULL,
    payload         JSONB       NOT NULL,
    status          TEXT        NOT NULL DEFAULT 'PENDING' CHECK (status IN ('PENDING', 'SENT', 'FAILED', 'SKIPPED')),
    attempts        INT         NOT NULL DEFAULT 0,
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_error      TEXT        NOT NULL DEFAULT '',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    sent_at         TIMESTAMPTZ,
    UNIQUE (kind, ref_id)
);
CREATE INDEX payout_posts_due_idx ON payout_posts (next_attempt_at) WHERE status = 'PENDING';

-- +goose Down
DROP TABLE payout_posts;
