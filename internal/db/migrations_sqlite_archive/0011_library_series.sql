-- +goose Up
CREATE TABLE library_series (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    tmdb_id          INTEGER NOT NULL,
    tvdb_id          INTEGER NOT NULL DEFAULT 0,
    title            TEXT NOT NULL,
    year             INTEGER NOT NULL DEFAULT 0,
    root_folder_path TEXT NOT NULL,
    created_at       TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at       TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    UNIQUE (tmdb_id)
);

CREATE INDEX idx_library_series_tmdb ON library_series (tmdb_id);

CREATE TABLE library_episodes (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    series_id      INTEGER NOT NULL REFERENCES library_series (id),
    season_number  INTEGER NOT NULL,
    episode_number INTEGER NOT NULL,
    title          TEXT NOT NULL DEFAULT '',
    air_date       TEXT NOT NULL DEFAULT '',
    file_path      TEXT NOT NULL DEFAULT '',
    created_at     TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at     TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    UNIQUE (series_id, season_number, episode_number)
);

CREATE INDEX idx_library_episodes_series ON library_episodes (series_id);

CREATE TABLE library_series_tags (
    series_id INTEGER NOT NULL REFERENCES library_series (id),
    tag       TEXT NOT NULL COLLATE NOCASE,
    PRIMARY KEY (series_id, tag)
);

-- +goose Down
DROP TABLE library_series_tags;
DROP TABLE library_episodes;
DROP TABLE library_series;
