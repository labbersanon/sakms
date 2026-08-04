-- +goose Up
-- Additive Gender for RSS-derived performer rows. NULLABLE with NO default on
-- purpose: NULL == "matched before Gender existed, never resolved" and is the
-- work queue the one-time backfill (internal/adultnewest gender backfill) drains.
-- New inserts always write a concrete value ('female'/'male'/other) or '' (attempted-
-- but-unresolvable), so NULL only ever exists for pre-existing rows and is self-draining.
ALTER TABLE adult_newest_releases ADD COLUMN gender TEXT;

-- +goose Down
ALTER TABLE adult_newest_releases DROP COLUMN gender;
