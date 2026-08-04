-- +goose Up
-- Cache table for the internal/adultnewest background scan job: one row per
-- MATCHED ENTITY (a scene/movie/studio/performer), not one row per Prowlarr
-- release — a studio or a specific scene can legitimately be surfaced by
-- several different releases (re-rips, different quality tiers), and those
-- must collapse to one Discover card, not duplicate. The unique key is
-- therefore (row_type, entity_source, entity_id): whichever release first
-- surfaces a given entity wins that cache row; later releases surfacing the
-- same entity are silently deduped away via INSERT ... ON CONFLICT DO
-- NOTHING, never re-checked/updated in place. Discover only ever reads this
-- table, never Prowlarr or the identify pipeline directly (see CLAUDE.md's
-- "Discover never queries Prowlarr" rule and this package's own doc comment
-- for why).
CREATE TABLE adult_newest_releases (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    row_type      TEXT NOT NULL,
    entity_id     TEXT NOT NULL,
    entity_source TEXT NOT NULL,
    entity_title  TEXT NOT NULL,
    entity_studio TEXT NOT NULL DEFAULT '',
    entity_image  TEXT NOT NULL DEFAULT '',
    entity_date   TEXT NOT NULL DEFAULT '',
    genres        TEXT NOT NULL DEFAULT '[]',
    first_seen_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    UNIQUE (row_type, entity_source, entity_id)
);

CREATE INDEX idx_adult_newest_releases_row_type ON adult_newest_releases (row_type);

-- Tracks which Prowlarr releases have already been run through the identify
-- pipeline, independent of whether they matched anything — separate from the
-- matched-entity cache above (which is keyed by entity, not release) so an
-- unmatched release is never retried (and its AI-pipeline cost never paid
-- again) on every subsequent cycle just because it produced no cache row.
CREATE TABLE adult_newest_seen (
    release_guid TEXT PRIMARY KEY,
    seen_at      TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

-- Admin-configurable Discover row definitions, same shape/purpose as
-- discover_sliders but for this Prowlarr-backed pipeline instead of
-- TMDB — see internal/adultnewest's package doc for why this is a sibling
-- package rather than a shared code path with discoversliders.
CREATE TABLE adult_newest_rows (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    title        TEXT NOT NULL,
    row_type     TEXT NOT NULL,
    genre_filter TEXT NOT NULL DEFAULT '',
    sort_order   INTEGER NOT NULL DEFAULT 0,
    enabled      INTEGER NOT NULL DEFAULT 1,
    created_at   TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at   TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE INDEX idx_adult_newest_rows_sort_order ON adult_newest_rows (sort_order);

-- Seed the 4 default rows the operator asked for — Movie/Scene/Performer/
-- Studio, no genre filter, enabled.
INSERT INTO adult_newest_rows (title, row_type, sort_order) VALUES
    ('Newest Movies', 'movie', 0),
    ('Newest Scenes', 'scene', 1),
    ('New Performers', 'performer', 2),
    ('New Studios', 'studio', 3);

-- +goose Down
DROP TABLE adult_newest_rows;
DROP TABLE adult_newest_seen;
DROP TABLE adult_newest_releases;
