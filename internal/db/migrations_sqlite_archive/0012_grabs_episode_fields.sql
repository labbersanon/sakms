-- +goose Up
ALTER TABLE grabs ADD COLUMN season_number INTEGER NOT NULL DEFAULT 0;
ALTER TABLE grabs ADD COLUMN episode_number INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE grabs DROP COLUMN season_number;
ALTER TABLE grabs DROP COLUMN episode_number;
