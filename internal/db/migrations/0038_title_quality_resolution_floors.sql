-- +goose Up
-- Claude 2026-10-12: per-resolution bitrate floors on title quality overrides.
-- Reason: mode Settings now grades 480p vs 1080p separately; a title override
--   that still stored one floor applied Medium to DVDRips and 1080p alike.
-- Troubleshooting: library_quality_prefs.resolution_floors JSON
--   {"480":"low","720":"medium","1080":"medium","2160":"medium"}. Empty string
--   synthesizes UniformFloors(Lowest(quality_tiers)) for pre-column rows.
-- Review if: Inherited GET stops copying mode floors into the title card.
-- Related files: internal/library/quality_prefs.go; TitleQualityPrefs.tsx

ALTER TABLE library_quality_prefs
    ADD COLUMN resolution_floors text NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE library_quality_prefs DROP COLUMN IF EXISTS resolution_floors;
