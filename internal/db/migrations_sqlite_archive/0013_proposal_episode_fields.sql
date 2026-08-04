-- +goose Up
ALTER TABLE proposals ADD COLUMN season_number INTEGER NOT NULL DEFAULT 0;
ALTER TABLE proposals ADD COLUMN episode_number INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE proposals DROP COLUMN season_number;
ALTER TABLE proposals DROP COLUMN episode_number;
