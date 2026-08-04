-- +goose Up
CREATE TABLE proposals (
    id                  INTEGER PRIMARY KEY AUTOINCREMENT,
    mode                TEXT NOT NULL,
    workflow            TEXT NOT NULL,
    status              TEXT NOT NULL DEFAULT 'pending',
    source_name         TEXT NOT NULL,
    source_path         TEXT NOT NULL,
    root_folder_path    TEXT NOT NULL,
    title               TEXT NOT NULL DEFAULT '',
    tvdb_id             INTEGER NOT NULL DEFAULT 0,
    tmdb_id             INTEGER NOT NULL DEFAULT 0,
    quality_profile_id  INTEGER NOT NULL DEFAULT 0,
    reason              TEXT NOT NULL DEFAULT '',
    tracked_id          INTEGER NOT NULL DEFAULT 0,
    created_at          TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    applied_at          TEXT
);

CREATE INDEX idx_proposals_mode_workflow_status ON proposals (mode, workflow, status);

-- +goose Down
DROP TABLE proposals;
