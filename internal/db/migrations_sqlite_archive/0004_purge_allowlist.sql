-- +goose Up
CREATE TABLE purge_allowlist (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    mode       TEXT NOT NULL,
    tag        TEXT NOT NULL,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

-- COLLATE NOCASE matches internal/purge.MatchesAny's case-insensitive
-- comparison — "BDSM" and "bdsm" are the same rule, not two rules.
CREATE UNIQUE INDEX idx_purge_allowlist_mode_tag ON purge_allowlist (mode, tag COLLATE NOCASE);

-- +goose Down
DROP TABLE purge_allowlist;
