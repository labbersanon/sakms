-- +goose Up
-- Claude 2026-08-13: poster_aspect_class on library_scenes
-- Reason: Adult Library Movies vs Scenes tabs filter the same table by catalog
--   poster ratio stored once at grab/import (deep-interview-adult-movies).
-- Troubleshooting: do not classify on GET /api/modes/adult/tracked — that path
--   must stay a SQL filter. Existing rows have no stored poster URL, so they
--   keep the DEFAULT 'horizontal' (Scenes tab) rather than a catalog backfill.
-- Review if: library_scenes starts storing a poster URL that a later backfill
--   could measure without live catalog lookups.

ALTER TABLE library_scenes
    ADD COLUMN poster_aspect_class text NOT NULL DEFAULT 'horizontal';

ALTER TABLE library_scenes
    ADD CONSTRAINT library_scenes_poster_aspect_class_check
    CHECK (poster_aspect_class IN ('vertical', 'horizontal'));

-- +goose Down
ALTER TABLE library_scenes DROP CONSTRAINT IF EXISTS library_scenes_poster_aspect_class_check;
ALTER TABLE library_scenes DROP COLUMN IF EXISTS poster_aspect_class;
