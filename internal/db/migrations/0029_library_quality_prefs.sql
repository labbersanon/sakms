-- +goose Up
-- Claude 2026-09-22: per-title quality + resolution overrides for series/movies.
-- Reason: monitor UI must persist unattended-grab prefs (mode settings alone
--   forced every title through the same floor — e.g. 480p after HD STAT miss).
-- Troubleshooting: SeasonsPanel / movie track quality pills; drain uses these
--   before {mode}_quality_tiers. Empty quality_tiers = inherit mode defaults.
-- Review if: adult titles grow the same override surface.
-- Related files: internal/library/quality_prefs.go, internal/api/titlequality.go

CREATE TABLE library_quality_prefs (
    mode text NOT NULL,
    tmdb_id bigint NOT NULL,
    quality_tiers text NOT NULL DEFAULT '',
    max_resolution bigint,
    created_at text NOT NULL DEFAULT sakms_now(),
    updated_at text NOT NULL DEFAULT sakms_now(),
    PRIMARY KEY (mode, tmdb_id)
);

-- +goose Down
DROP TABLE library_quality_prefs;
