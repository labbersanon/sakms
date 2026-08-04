-- Claude 2026-08-04: new migration for the admin-configurable stash-box
-- database registry (Stage 5).
-- Reason: StashDB/FansDB were hardcoded singleton `connections` rows with
-- hardcoded endpoints, so an operator could never add a third stash-box-
-- protocol instance, reorder the cascade, or disable one. This table is the
-- registry that replaces those literals.
-- Troubleshooting: n/a — new feature, no prior bug.
-- Review if: OQ7 lands (the seeded secrets get migrated inline and secret_ref
-- is dropped), or the cap moves off 5.
-- Context: .omc/plans/ralplan-adult-identify-configurable-databases.md §2.2,
-- .omc/plans/autopilot-impl-stage5-stashboxdb-ui.md Wave 1.
--
-- +goose Up
-- Admin-configurable stash-box-protocol databases (StashDB, FansDB, and up to
-- 3 more operator-added instances that speak the same open protocol). Every
-- row is a peer — fully editable, deletable, no reserved tier. METADATA ONLY
-- for the two SEEDED rows: their secret stays in `connections` (untouched,
-- pointed at by secret_ref), so nothing that resolves a key by the literal
-- service name has to change and no operator ever re-enters a key.
-- Operator-added rows store endpoint + encrypted key inline (secret_ref = '').
CREATE TABLE stashbox_databases (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    name              TEXT    NOT NULL UNIQUE,     -- throttle key / MatchResult.Box / give-back key / library_scenes.box
    endpoint          TEXT    NOT NULL,            -- GraphQL endpoint URL (editable for every row)
    api_key_encrypted TEXT    NOT NULL DEFAULT '', -- inline key for secret_ref='' rows; '' when the key lives in connections
    priority          INTEGER NOT NULL DEFAULT 0,  -- ascending cascade order (0 = consulted first)
    enabled           INTEGER NOT NULL DEFAULT 1,
    fansite_only      INTEGER NOT NULL DEFAULT 0,  -- gate FUZZY text search behind a fansite hint (editable)
    secret_ref        TEXT    NOT NULL DEFAULT '', -- internal secret-source handle: '' = inline; else the
                                                   -- `connections` service the key lives under (seeds only).
                                                   -- NOT a user tier — invisible to the UI, zero matching effect.
    created_at        TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at        TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE INDEX idx_stashbox_databases_priority ON stashbox_databases (enabled, priority);

-- Seed the two well-known instances as ordinary (peer) rows. The endpoints are
-- the known public constants (stashbox.StashDBURL / FansDBURL) — StashDB and
-- FansDB never collected a user URL, so seeding them here is faithful; the
-- operator may edit or delete them like any other row. No key is copied: the
-- seeded secret stays in `connections`, pointed at by secret_ref. fansdb seeds
-- the fansite-only gate identify.go applies to it today (still editable
-- afterward). Priorities 0/1 reproduce today's hardcoded ["stashdb","fansdb"]
-- cascade order byte-for-byte on a default install.
INSERT INTO stashbox_databases (name, endpoint, priority, enabled, fansite_only, secret_ref) VALUES
  ('stashdb', 'https://stashdb.org/graphql', 0, 1, 0, 'stashdb'),
  ('fansdb',  'https://fansdb.cc/graphql',   1, 1, 1, 'fansdb');

-- +goose Down
DROP TABLE stashbox_databases;
