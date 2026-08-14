-- +goose Up
-- Claude 2026-08-14: operator 1–5 star rating on every library row, plus
--   pruning_rules.min_rating as a fifth AND'd Clean-up condition.
-- Reason: ratings are operator-assigned (not TMDB/TPDB catalog scores). 0 is
--   unset. Upsert paths must not write this column or a re-grab would wipe it.
--   min_rating 0 = condition not configured; 1–5 = match when 0 < rating < min.
-- Troubleshooting: unrated rows must never match a min-rating rule (fail-closed,
--   same as tags). Nested star buttons cannot live inside MediaCardShell.
-- Review if: half-stars, a 10-point scale, or catalog ratings are added.

ALTER TABLE library_items
    ADD COLUMN rating integer NOT NULL DEFAULT 0
    CONSTRAINT library_items_rating_range CHECK (rating BETWEEN 0 AND 5);

ALTER TABLE library_scenes
    ADD COLUMN rating integer NOT NULL DEFAULT 0
    CONSTRAINT library_scenes_rating_range CHECK (rating BETWEEN 0 AND 5);

ALTER TABLE library_series
    ADD COLUMN rating integer NOT NULL DEFAULT 0
    CONSTRAINT library_series_rating_range CHECK (rating BETWEEN 0 AND 5);

ALTER TABLE pruning_rules
    ADD COLUMN min_rating integer NOT NULL DEFAULT 0
    CONSTRAINT pruning_rules_min_rating_range CHECK (min_rating BETWEEN 0 AND 5);

-- +goose Down
ALTER TABLE pruning_rules DROP COLUMN IF EXISTS min_rating;
ALTER TABLE library_series DROP COLUMN IF EXISTS rating;
ALTER TABLE library_scenes DROP COLUMN IF EXISTS rating;
ALTER TABLE library_items DROP COLUMN IF EXISTS rating;
