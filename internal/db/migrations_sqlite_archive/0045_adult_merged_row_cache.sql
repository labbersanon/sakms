-- +goose Up
-- Additive read-cache for Adult Discover's merged Performers/Studios rows
-- (internal/adultmergecache's background precompute job). Each row holds one
-- precomputed page's PRE-availability-filter merged card list as an opaque JSON
-- blob, plus the pre-filter has_more flag and the compute timestamp:
--
--   * row_type   — 'performers' or 'studios'; the discriminator that lets one
--     table back both merged rows (same key shape, same opaque payload, same
--     upsert), mirroring adult_newest_releases' own row_type precedent.
--   * page/per_page — the precomputed page (1..K) and its page size. per_page is
--     in the key even though only 20 is used today, so a future variable perPage
--     is a non-breaking extension rather than a schema change.
--   * payload    — the merged card list serialized whole; never queried into
--     (a page's blob is fetched and returned as one unit), so a blob column is
--     correct here, not a typed-column violation.
--   * has_more   — the pre-filter "more pages may exist" signal the handler
--     computes today, stored so it can never be re-derived from a post-filter
--     count.
--   * computed_at — RFC3339 timestamp of the precompute that wrote this row.
--
-- Purely additive: no existing table is altered or dropped. The read handlers
-- fall back to today's live merge on any miss, so this table being empty (cold
-- start) is not a regression.
CREATE TABLE adult_merged_row_cache (
    row_type    TEXT    NOT NULL,
    page        INTEGER NOT NULL,
    per_page    INTEGER NOT NULL DEFAULT 20,
    payload     TEXT    NOT NULL,
    has_more    INTEGER NOT NULL DEFAULT 0,
    computed_at TEXT    NOT NULL,
    PRIMARY KEY (row_type, page, per_page)
);

-- +goose Down
DROP TABLE adult_merged_row_cache;
