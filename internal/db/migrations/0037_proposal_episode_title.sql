-- +goose Up
-- Claude 2026-10-01: persist Scan-time episode title on proposals.
-- Reason: Jellyfin dest names need the cartoon/episode title; year-season
--   Looney shorts already corroborated it from TVDB but only put it in Reason.
-- Troubleshooting: Rename proposed name is "Looney Tunes S1947E05.mp4"
--   without "A Hare Grows in Manhattan".
-- Review if: Scan fills episode_title for ordinary TMDB SxxExx too.

ALTER TABLE proposals
    ADD COLUMN episode_title text NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE proposals DROP COLUMN IF EXISTS episode_title;
