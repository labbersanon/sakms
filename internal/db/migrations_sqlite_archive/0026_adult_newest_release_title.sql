-- +goose Up
-- The raw Prowlarr release title that first matched this entity
-- (prowlarr.Release.Title, scan.go's matchRelease) — captured because
-- reconstructing a search query from TPDB's own studio+title metadata
-- includes tokens (e.g. TPDB's "S6:E10" episode notation) real indexer
-- release filenames never contain, hurting Grab-time search recall. Storing
-- the exact text that already matched once and reusing it as the Grab-time
-- query is strictly more reliable. Empty for Studio/Performer rows (no
-- associated Grab) and for entities matched before this column existed.
ALTER TABLE adult_newest_releases ADD COLUMN first_seen_release_title TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE adult_newest_releases DROP COLUMN first_seen_release_title;
