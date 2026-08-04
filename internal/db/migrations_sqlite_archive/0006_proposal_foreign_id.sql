-- +goose Up
ALTER TABLE proposals ADD COLUMN foreign_id TEXT NOT NULL DEFAULT '';
ALTER TABLE proposals ADD COLUMN item_type  TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE proposals DROP COLUMN foreign_id;
ALTER TABLE proposals DROP COLUMN item_type;
