-- +goose Up
CREATE TABLE library_scenes (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    box              TEXT NOT NULL,
    scene_id         TEXT NOT NULL,
    title            TEXT NOT NULL DEFAULT '',
    studio           TEXT NOT NULL DEFAULT '',
    date             TEXT NOT NULL DEFAULT '',
    file_path        TEXT NOT NULL DEFAULT '',
    root_folder_path TEXT NOT NULL,
    phash            TEXT NOT NULL DEFAULT '',
    phash_file_size  INTEGER NOT NULL DEFAULT 0,
    phash_file_mtime TEXT NOT NULL DEFAULT '',
    created_at       TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at       TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    UNIQUE (box, scene_id)
);

CREATE INDEX idx_library_scenes_box_scene ON library_scenes (box, scene_id);

CREATE TABLE library_scene_tags (
    scene_id INTEGER NOT NULL REFERENCES library_scenes (id),
    tag      TEXT NOT NULL COLLATE NOCASE,
    PRIMARY KEY (scene_id, tag)
);

-- +goose Down
DROP TABLE library_scene_tags;
DROP TABLE library_scenes;
