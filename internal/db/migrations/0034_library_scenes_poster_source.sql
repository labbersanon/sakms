-- +goose Up
-- Claude 2026-09-29: poster_source on library_scenes
-- Reason: operator catalog-poster picks must survive later fill-if-empty
--   backfill (UpsertScene / ListScenesNeedingPoster). 'operator' is set
--   only by the Adult poster edit PUT.
-- Troubleshooting: empty poster_url + operator must not be refilled.
-- Review if: artwork backfill lands and must skip poster_source='operator'.

ALTER TABLE library_scenes
    ADD COLUMN poster_source text NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE library_scenes DROP COLUMN IF EXISTS poster_source;
