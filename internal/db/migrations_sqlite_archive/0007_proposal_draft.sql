-- +goose Up
ALTER TABLE proposals ADD COLUMN studio             TEXT NOT NULL DEFAULT '';
ALTER TABLE proposals ADD COLUMN scene_date         TEXT NOT NULL DEFAULT '';
ALTER TABLE proposals ADD COLUMN draft_id           TEXT NOT NULL DEFAULT '';
ALTER TABLE proposals ADD COLUMN draft_submitted_at TEXT;

-- +goose Down
ALTER TABLE proposals DROP COLUMN studio;
ALTER TABLE proposals DROP COLUMN scene_date;
ALTER TABLE proposals DROP COLUMN draft_id;
ALTER TABLE proposals DROP COLUMN draft_submitted_at;
