-- +goose Up
ALTER TABLE connections ADD COLUMN username TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE connections DROP COLUMN username;
