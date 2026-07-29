-- +goose Up
-- Additive durable index for the on-disk Adult Discover image cache
-- (internal/imageproxy's durable tier, populated by internal/adultmergecache's
-- precompute). Bytes live on disk under <DataDir>/imagecache; this table is the
-- restart-durable index only — never the bytes themselves (no-BLOB precedent;
-- SetMaxOpenConns(1) keeps binary off the sole connection).
CREATE TABLE adult_image_cache (
    url_sha256   TEXT    NOT NULL PRIMARY KEY, -- sha256 of the post-validation url string
    source_url   TEXT    NOT NULL,             -- the normalized url, for debuggability/GC audit
    content_type TEXT    NOT NULL,             -- served back verbatim as the response header
    byte_size    INTEGER NOT NULL DEFAULT 0,   -- for the disk-cap circuit breaker
    row_type     TEXT    NOT NULL DEFAULT '',  -- 'performers'|'studios' provenance (diagnostics/GC scoping)
    fetched_at   TEXT    NOT NULL              -- RFC3339; LRU key for PruneToCap
);
CREATE INDEX adult_image_cache_fetched_at ON adult_image_cache (fetched_at);

-- +goose Down
DROP TABLE adult_image_cache;
