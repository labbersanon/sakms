-- +goose Up
-- Claude 2026-10-23: local Newznab catalog for overlap (not OVER/usenet_nntp_*).
-- Reason: Lighthouse will later filter Mainstream to indexed titles and move
--   Adult off RSS. This pass stores t=search pages from newznab + newznab2
--   (movies/series/adult) so overlap can be counted. Do not scrape HTML.
--   Do not store enclosure URLs (they often embed apikeys).
-- Troubleshooting: relation newznab_catalog does not exist → goose < 55.
-- Review if: Lighthouse availability filter ships (read this table, do not
--   recrawl on every Discover render).
-- Related files: internal/newznabcatalog, internal/newznab/catalog.go

CREATE TABLE newznab_catalog (
    indexer     TEXT NOT NULL,
    guid        TEXT NOT NULL,
    title       TEXT NOT NULL DEFAULT '',
    size        BIGINT NOT NULL DEFAULT 0,
    category    INTEGER NOT NULL DEFAULT 0,
    kind        TEXT NOT NULL,
    imdb_id     TEXT NOT NULL DEFAULT '',
    tvdb_id     TEXT NOT NULL DEFAULT '',
    tmdb_id     TEXT NOT NULL DEFAULT '',
    year        INTEGER NOT NULL DEFAULT 0,
    overlap_key TEXT NOT NULL DEFAULT '',
    ingested_at BIGINT NOT NULL DEFAULT 0,
    PRIMARY KEY (indexer, guid)
);
CREATE INDEX idx_newznab_catalog_kind_key ON newznab_catalog (kind, overlap_key);
CREATE INDEX idx_newznab_catalog_indexer_kind ON newznab_catalog (indexer, kind);

CREATE TABLE newznab_catalog_cursors (
    indexer        TEXT NOT NULL,
    kind           TEXT NOT NULL,
    page_offset    INTEGER NOT NULL DEFAULT 0,
    complete       BOOLEAN NOT NULL DEFAULT FALSE,
    last_page_new  INTEGER NOT NULL DEFAULT 0,
    zero_streak    INTEGER NOT NULL DEFAULT 0,
    last_error     TEXT NOT NULL DEFAULT '',
    updated_at     BIGINT NOT NULL DEFAULT 0,
    PRIMARY KEY (indexer, kind)
);

-- +goose Down
DROP TABLE IF EXISTS newznab_catalog_cursors;
DROP TABLE IF EXISTS newznab_catalog;
