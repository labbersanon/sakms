-- +goose Up
-- Claude 2026-09-22: poster_url + poster_source on library_items / library_series
-- Reason: Library cards N+1 GET /poster with no cached art; TMDB-empty titles
--   stay on letter tiles. Persist absolute https poster URL + source
--   (tmdb|tvdb|ai) after the resolve chain fills it so later /poster and
--   GET /tracked can reuse without re-hitting TMDB/TVDB/AI.
-- Troubleshooting: letter tiles that never resolve; AI/TVDB re-running every
--   Library load. Upsert must NOT wipe these columns (same rule as rating).
-- Review if: Discover payloads start carrying posters and /poster is retired.
-- Related files: internal/library/library_poster.go, internal/api/discover.go

ALTER TABLE library_items
    ADD COLUMN poster_url text NOT NULL DEFAULT '',
    ADD COLUMN poster_source text NOT NULL DEFAULT '';

ALTER TABLE library_series
    ADD COLUMN poster_url text NOT NULL DEFAULT '',
    ADD COLUMN poster_source text NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE library_items DROP COLUMN IF EXISTS poster_url;
ALTER TABLE library_items DROP COLUMN IF EXISTS poster_source;
ALTER TABLE library_series DROP COLUMN IF EXISTS poster_url;
ALTER TABLE library_series DROP COLUMN IF EXISTS poster_source;
