-- +goose Up
CREATE TABLE IF NOT EXISTS orphan_phashes (
    path             TEXT    NOT NULL PRIMARY KEY,
    phash            TEXT    NOT NULL DEFAULT '',
    phash_file_size  INTEGER NOT NULL DEFAULT 0,
    phash_file_mtime TEXT    NOT NULL DEFAULT ''
);

-- +goose Down
DROP TABLE IF EXISTS orphan_phashes;
