-- +goose Up
-- Claude 2026-09-29: numbered 0036 (was 0034 on poster-edit branch)
-- Reason: unfinished-goals kept 0034; artwork backdrop is 0035.
-- Troubleshooting: two 0034 files would stall goose on deploy.
-- Review if: this is the last colliding 0034 from the four-way merge.

ALTER TABLE library_scenes
    ADD COLUMN poster_source text NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE library_scenes DROP COLUMN IF EXISTS poster_source;
