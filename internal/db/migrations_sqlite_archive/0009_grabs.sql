-- +goose Up
CREATE TABLE grabs (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    mode             TEXT NOT NULL,
    title            TEXT NOT NULL,
    tmdb_id          INTEGER NOT NULL DEFAULT 0,
    tvdb_id          INTEGER NOT NULL DEFAULT 0,
    quality_profile_id INTEGER NOT NULL DEFAULT 0,
    indexer          TEXT NOT NULL,
    protocol         TEXT NOT NULL,
    download_client  TEXT NOT NULL,
    client_ref       TEXT NOT NULL DEFAULT '',
    status           TEXT NOT NULL DEFAULT 'queued',
    root_folder_path TEXT NOT NULL,
    created_at       TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at       TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE INDEX idx_grabs_mode_status ON grabs (mode, status);

-- +goose Down
DROP TABLE grabs;
