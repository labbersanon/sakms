-- +goose Up
ALTER TABLE proposals ADD COLUMN candidates_json TEXT NOT NULL DEFAULT '[]';

-- +goose Down
ALTER TABLE proposals DROP COLUMN candidates_json;
