-- +goose Up
ALTER TABLE proposals ADD COLUMN year INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE proposals DROP COLUMN year;
