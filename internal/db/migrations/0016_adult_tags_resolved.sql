-- Claude 2026-08-24: track whether a cached Adult scene/movie row's catalog
-- genres/performers have been resolved at least once.
-- Reason: rows cached before stash-box tag selection shipped carry genres='[]'
--   forever under first-writer-wins; the tag backfill must drain that legacy
--   queue without re-fetching rows already confirmed tagless every cycle.
-- Troubleshooting: Adult Discover detail popups showed no genre pills for most
--   stashdb-sourced pooled scenes even though the catalog carries tags.
-- Review if: Insert always writes tags on first match and this column is redundant.

ALTER TABLE adult_newest_releases ADD COLUMN tags_resolved INTEGER NOT NULL DEFAULT 0;

-- Rows that already carry genres do not need another catalog round-trip.
UPDATE adult_newest_releases
SET tags_resolved = 1
WHERE genres IS NOT NULL AND genres != '' AND genres != '[]';
