-- +goose Up
-- Claude 2026-09-22: title resolution preference is a MINIMUM, not a soft max.
-- Reason: operators picking 1080p mean "1080 or better", not "prefer ≤1080".
-- Troubleshooting: requireMinResolution in titlequality.go; UI "Minimum resolution".
-- Review if: mode-level Settings max_resolution is also flipped to a minimum.
-- Related files: library/quality_prefs.go; TitleQualityPrefs.tsx

ALTER TABLE library_quality_prefs RENAME COLUMN max_resolution TO min_resolution;

-- +goose Down
ALTER TABLE library_quality_prefs RENAME COLUMN min_resolution TO max_resolution;
