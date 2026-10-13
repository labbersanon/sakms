-- +goose Up
-- Claude 2026-10-12: title quality ceiling, codec rank, feature-upgrade flag.
-- Reason: Settings/title now share a hard resolution band, contiguous quality
--   floor–ceiling, operator codec order, and opt-in HDR/channel upgrades.
-- Troubleshooting: library_quality_prefs max_resolution / codec_rank /
--   feature_upgrade; inherit when NULL / empty.
-- Review if: Adult titles store quality prefs in this table.
-- Related files: internal/library/quality_prefs.go; TitleQualityPrefs.tsx

ALTER TABLE library_quality_prefs
    ADD COLUMN max_resolution INTEGER;
ALTER TABLE library_quality_prefs
    ADD COLUMN codec_rank text NOT NULL DEFAULT '';
ALTER TABLE library_quality_prefs
    ADD COLUMN feature_upgrade INTEGER;

-- +goose Down
ALTER TABLE library_quality_prefs DROP COLUMN IF EXISTS max_resolution;
ALTER TABLE library_quality_prefs DROP COLUMN IF EXISTS codec_rank;
ALTER TABLE library_quality_prefs DROP COLUMN IF EXISTS feature_upgrade;
