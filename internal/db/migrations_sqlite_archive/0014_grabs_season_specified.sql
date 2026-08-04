-- +goose Up
ALTER TABLE grabs ADD COLUMN season_specified INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE grabs DROP COLUMN season_specified;
