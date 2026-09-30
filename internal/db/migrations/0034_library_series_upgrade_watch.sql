-- +goose Up
-- Claude 2026-09-29: opt-in series upgrade-watch flag on library_series.
-- Reason: movie upgrade-watch analog for owned episodes below the title floor.
--   Default off; UpsertSeries must not write this column.
-- Troubleshooting: GET/PUT .../series/library/.../upgrade-watch; sixth retry-cycle pass.
-- Review if: per-episode watch flags replace the series-level switch.
-- Related files: internal/library/library_series.go, internal/api/seriesupgradewatch.go

ALTER TABLE library_series
    ADD COLUMN upgrade_watch boolean NOT NULL DEFAULT false;

CREATE INDEX idx_library_series_upgrade_watch
    ON library_series (id)
    WHERE upgrade_watch = true;

-- +goose Down
DROP INDEX IF EXISTS idx_library_series_upgrade_watch;
ALTER TABLE library_series DROP COLUMN IF EXISTS upgrade_watch;
