-- +goose Up
-- Entity cache for DB-first filename parsing: studios and performers loaded
-- from TPDB, StashDB, FansDB, and local Stash on a background schedule.
-- name_norm is the normalized form (lowercase, alphanumeric only) used at
-- both write and query time so "Bang Bros", "bangbros.com", and "bang-bros"
-- all resolve to the same row ("bangbros").
CREATE TABLE parse_studios (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    name       TEXT NOT NULL,
    name_norm  TEXT NOT NULL,
    source     TEXT NOT NULL,
    ext_id     TEXT,
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);
CREATE UNIQUE INDEX parse_studios_norm ON parse_studios (name_norm);

CREATE TABLE parse_performers (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    name       TEXT NOT NULL,
    name_norm  TEXT NOT NULL,
    source     TEXT NOT NULL,
    ext_id     TEXT,
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);
CREATE UNIQUE INDEX parse_performers_norm ON parse_performers (name_norm);

-- Aliases: "wow girls" → name_norm "wowgirls" → studio_id N
-- Allows matching alternate spellings/domain-formats without duplicating rows.
CREATE TABLE parse_studio_aliases (
    alias_norm TEXT PRIMARY KEY,
    studio_id  INTEGER NOT NULL REFERENCES parse_studios (id) ON DELETE CASCADE
);
CREATE TABLE parse_performer_aliases (
    alias_norm   TEXT PRIMARY KEY,
    performer_id INTEGER NOT NULL REFERENCES parse_performers (id) ON DELETE CASCADE
);

-- Sync state: one row per source tracks the last successful sync cursor and
-- timestamp so incremental syncs can resume where they left off.
CREATE TABLE parse_entity_sync (
    source    TEXT PRIMARY KEY,
    cursor    TEXT,
    synced_at TEXT
);

-- +goose Down
DROP TABLE IF EXISTS parse_studio_aliases;
DROP TABLE IF EXISTS parse_performer_aliases;
DROP TABLE IF EXISTS parse_studios;
DROP TABLE IF EXISTS parse_performers;
DROP TABLE IF EXISTS parse_entity_sync;
