-- +goose Up
-- The picture of a gift, uploaded in the admin panel. It lives in the database (not on disk), so the hourly backup keeps it.
-- A gift without a row here shows the artwork bundled in the app.
CREATE TABLE gift_images (
    gift_key     text PRIMARY KEY,
    content_type text        NOT NULL,
    data         bytea       NOT NULL,
    updated_at   timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE gift_images;
