-- +goose Up
-- Drop the TPDB/StashDB catalog-crawl caches (internal/adultmergecache, being deleted in a
-- separate story) and the durable image index (internal/imageproxy durable tier, being
-- deleted). Both were shipped earlier this session; the RSS-derived redesign makes them dead.
-- Deliberate destructive choice, owner-approved.
DROP TABLE IF EXISTS adult_merged_row_cache;   -- from 0045 (PK only, no separate index)
DROP TABLE IF EXISTS adult_image_cache;        -- from 0046 (drops its fetched_at index too)

-- +goose Down
-- Recreate empty schemas for goose-down reversibility only. Cached CONTENT is not restored
-- (it is a regenerable cache and both writers are gone) — the down path yields inert tables.
CREATE TABLE adult_merged_row_cache (
    row_type TEXT NOT NULL, page INTEGER NOT NULL, per_page INTEGER NOT NULL DEFAULT 20,
    payload TEXT NOT NULL, has_more INTEGER NOT NULL DEFAULT 0, computed_at TEXT NOT NULL,
    PRIMARY KEY (row_type, page, per_page)
);
CREATE TABLE adult_image_cache (
    url_sha256 TEXT NOT NULL PRIMARY KEY, source_url TEXT NOT NULL, content_type TEXT NOT NULL,
    byte_size INTEGER NOT NULL DEFAULT 0, row_type TEXT NOT NULL DEFAULT '', fetched_at TEXT NOT NULL
);
CREATE INDEX adult_image_cache_fetched_at ON adult_image_cache (fetched_at);
