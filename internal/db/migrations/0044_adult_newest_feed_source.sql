-- +goose Up
-- Extend the adult_newest_releases cache so it can hold FEED-sourced entities
-- (internal/adultnewest's new feed pass) alongside the existing cat-6000 browse
-- pass's entities, and decouple two orthogonal facts per row:
--
--   * browse_confirmed — the cat-6000 browse pass currently confirms this
--     entity (independent of every feed). Drives VISIBILITY.
--   * download_url/download_protocol/size_bytes/feed_id/feed_item_key — which
--     feed (if any) offers a direct-grab enclosure for this entity, and the
--     enclosure itself. feed_id = 0 means "no feed source." Drives GRAB-PATH,
--     gated at read time against that feed's health + a TTL.
--
-- last_confirmed_seen is a Unix timestamp (seconds); 0 = never confirmed present
-- in a poll (correct for browse-only rows, which never depend on it). It is the
-- persisted, restart-durable TTL clock the feed-freshness gate reads.
--
-- Same additive-ALTER pattern as migrations 0025-0027; the trailing one-time
-- UPDATE mirrors how those backfill a new column's known value for existing rows.
ALTER TABLE adult_newest_releases ADD COLUMN download_url         TEXT    NOT NULL DEFAULT '';
ALTER TABLE adult_newest_releases ADD COLUMN download_protocol    TEXT    NOT NULL DEFAULT '';
ALTER TABLE adult_newest_releases ADD COLUMN size_bytes           INTEGER NOT NULL DEFAULT 0;
ALTER TABLE adult_newest_releases ADD COLUMN browse_confirmed     INTEGER NOT NULL DEFAULT 0;
ALTER TABLE adult_newest_releases ADD COLUMN feed_id              INTEGER NOT NULL DEFAULT 0;
ALTER TABLE adult_newest_releases ADD COLUMN feed_item_key        TEXT    NOT NULL DEFAULT '';
ALTER TABLE adult_newest_releases ADD COLUMN last_confirmed_seen  INTEGER NOT NULL DEFAULT 0;
-- Every row that exists before this migration was created by the cat-6000
-- browse pass (feeds did not source the cache until now), so mark them all
-- browse-confirmed. The column defaults to 0 (fail-closed for any future insert
-- that forgets to set it); this one-time UPDATE asserts the known-true
-- provenance of the pre-existing rows so they stay visible after deploy.
UPDATE adult_newest_releases SET browse_confirmed = 1;

-- +goose Down
ALTER TABLE adult_newest_releases DROP COLUMN last_confirmed_seen;
ALTER TABLE adult_newest_releases DROP COLUMN feed_item_key;
ALTER TABLE adult_newest_releases DROP COLUMN feed_id;
ALTER TABLE adult_newest_releases DROP COLUMN browse_confirmed;
ALTER TABLE adult_newest_releases DROP COLUMN size_bytes;
ALTER TABLE adult_newest_releases DROP COLUMN download_protocol;
ALTER TABLE adult_newest_releases DROP COLUMN download_url;
