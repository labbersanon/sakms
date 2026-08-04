-- +goose Up
-- Structured genre and cast enrichment: populated at Scan time from TMDB,
-- carried through proposals, and persisted on library rows at Apply time.
ALTER TABLE proposals      ADD COLUMN genres TEXT NOT NULL DEFAULT '[]';
ALTER TABLE proposals      ADD COLUMN cast   TEXT NOT NULL DEFAULT '[]';
ALTER TABLE library_items  ADD COLUMN genres TEXT NOT NULL DEFAULT '[]';
ALTER TABLE library_items  ADD COLUMN cast   TEXT NOT NULL DEFAULT '[]';
ALTER TABLE library_series ADD COLUMN genres TEXT NOT NULL DEFAULT '[]';
ALTER TABLE library_series ADD COLUMN cast   TEXT NOT NULL DEFAULT '[]';

-- +goose Down
ALTER TABLE library_series DROP COLUMN cast;
ALTER TABLE library_series DROP COLUMN genres;
ALTER TABLE library_items  DROP COLUMN cast;
ALTER TABLE library_items  DROP COLUMN genres;
ALTER TABLE proposals      DROP COLUMN cast;
ALTER TABLE proposals      DROP COLUMN genres;
