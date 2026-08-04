-- +goose Up
ALTER TABLE proposals ADD COLUMN phash                    TEXT NOT NULL DEFAULT '';
ALTER TABLE proposals ADD COLUMN duration_seconds         INTEGER NOT NULL DEFAULT 0;
ALTER TABLE proposals ADD COLUMN give_back_box            TEXT NOT NULL DEFAULT '';
ALTER TABLE proposals ADD COLUMN give_back_scene_id       TEXT NOT NULL DEFAULT '';
ALTER TABLE proposals ADD COLUMN fingerprint_submitted_at TEXT;

-- +goose Down
ALTER TABLE proposals DROP COLUMN phash;
ALTER TABLE proposals DROP COLUMN duration_seconds;
ALTER TABLE proposals DROP COLUMN give_back_box;
ALTER TABLE proposals DROP COLUMN give_back_scene_id;
ALTER TABLE proposals DROP COLUMN fingerprint_submitted_at;
