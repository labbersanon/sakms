-- +goose Up
-- Claude 2026-08-14: poster_url on library_scenes
-- Reason: Adult Library cards had only a video still; Discover already has
--   MatchResult.Image at grab. Store that URL (imageproxy-validated) so
--   GET /tracked can expose it. No BrowseMovies. No backfill — existing
--   rows keep ''.
-- Troubleshooting: GET /tracked must not probe or fetch catalog art.
-- Review if: a later backfill measures poster_aspect_class from this URL.

ALTER TABLE library_scenes
    ADD COLUMN poster_url text NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE library_scenes DROP COLUMN IF EXISTS poster_url;
