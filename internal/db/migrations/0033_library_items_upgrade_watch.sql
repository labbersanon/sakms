-- +goose Up
-- Claude 2026-09-28: opt-in movie upgrade-watch flag on library_items.
-- Reason: ROADMAP "watch an owned movie for a better release". Default off;
--   Upsert must not write this column or a re-grab would wipe the operator
--   choice (same rule as rating). Hunting is gated by this flag plus the
--   existing usenet_autograb_enabled toggle, not by the derived Monitored chip.
-- Troubleshooting: GET/PUT .../library/tmdb/{tmdbId}/upgrade-watch; sixth
--   pass of runUsenetRetryCycle (monitorMovieUpgradeWatch).
-- Review if: Series episodes grow a matching per-file watch flag.
-- Related files: internal/library/library_upgrade_watch.go,
--   internal/api/movieupgradewatch.go

ALTER TABLE library_items
    ADD COLUMN upgrade_watch boolean NOT NULL DEFAULT false;

CREATE INDEX idx_library_items_upgrade_watch
    ON library_items (mode)
    WHERE upgrade_watch = true;

-- +goose Down
DROP INDEX IF EXISTS idx_library_items_upgrade_watch;
ALTER TABLE library_items DROP COLUMN IF EXISTS upgrade_watch;
