-- +goose Up
ALTER TABLE library_items ADD COLUMN phash            TEXT    NOT NULL DEFAULT '';
ALTER TABLE library_items ADD COLUMN phash_file_size  INTEGER NOT NULL DEFAULT 0;
ALTER TABLE library_items ADD COLUMN phash_file_mtime TEXT    NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE library_items DROP COLUMN phash;
ALTER TABLE library_items DROP COLUMN phash_file_size;
ALTER TABLE library_items DROP COLUMN phash_file_mtime;
