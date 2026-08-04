-- +goose Up
CREATE TABLE library_items (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    mode             TEXT NOT NULL,
    tmdb_id          INTEGER NOT NULL,
    title            TEXT NOT NULL,
    year             INTEGER NOT NULL DEFAULT 0,
    file_path        TEXT NOT NULL DEFAULT '',
    root_folder_path TEXT NOT NULL,
    created_at       TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at       TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    UNIQUE (mode, tmdb_id)
);

CREATE INDEX idx_library_items_mode ON library_items (mode);

CREATE TABLE library_tags (
    item_id INTEGER NOT NULL REFERENCES library_items (id) ON DELETE CASCADE,
    tag     TEXT NOT NULL COLLATE NOCASE,
    PRIMARY KEY (item_id, tag)
);

-- +goose Down
DROP TABLE library_tags;
DROP TABLE library_items;
