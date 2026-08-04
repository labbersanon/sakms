-- +goose Up
ALTER TABLE library_items    ADD COLUMN size         INTEGER NOT NULL DEFAULT 0;
ALTER TABLE library_items    ADD COLUMN quality_tier TEXT    NOT NULL DEFAULT '';
ALTER TABLE library_episodes ADD COLUMN size         INTEGER NOT NULL DEFAULT 0;
ALTER TABLE library_episodes ADD COLUMN quality_tier TEXT    NOT NULL DEFAULT '';
ALTER TABLE library_scenes   ADD COLUMN size         INTEGER NOT NULL DEFAULT 0;
ALTER TABLE library_scenes   ADD COLUMN quality_tier TEXT    NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE library_scenes   DROP COLUMN quality_tier;
ALTER TABLE library_scenes   DROP COLUMN size;
ALTER TABLE library_episodes DROP COLUMN quality_tier;
ALTER TABLE library_episodes DROP COLUMN size;
ALTER TABLE library_items    DROP COLUMN quality_tier;
ALTER TABLE library_items    DROP COLUMN size;
