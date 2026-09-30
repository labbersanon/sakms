-- +goose Up
-- Claude 2026-09-29: backdrop_url on library_items / library_series
-- Reason: TMDB BackdropPath was fetched with posters but never stored, so
--   DetailPopup had no fanart wash. Persist absolute w1280 URL fill-if-empty
--   from the same MovieDetails/TVDetails call as the poster chain.
-- Troubleshooting: header wash stays blank after /poster succeeds → column
--   empty or GET /tracked omitted BackdropURL.
-- Review if: Adult scenes gain a backdrop column, or local fanart.jpg is
--   picked up (explicitly not this change).
-- Related files: internal/library/library_poster.go, internal/api/poster.go

ALTER TABLE library_items
    ADD COLUMN backdrop_url text NOT NULL DEFAULT '';

ALTER TABLE library_series
    ADD COLUMN backdrop_url text NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE library_items DROP COLUMN IF EXISTS backdrop_url;
ALTER TABLE library_series DROP COLUMN IF EXISTS backdrop_url;
