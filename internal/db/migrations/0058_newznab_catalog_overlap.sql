-- +goose Up
-- Claude 2026-10-24: Dock overlap is a maintained summary, not a live GROUP BY.
-- Reason: GET /api/admin/newznab-catalog ran the overlap CTE on newznab_catalog
--   (~1.3M rows). That query exceeded 120s; Traefik logged 499 and the card
--   stayed on "Loading NZB catalog…". COUNT(*) GROUP BY indexer,kind is ~140ms.
-- Troubleshooting: empty Same-NZBs lines after ship → RebuildOverlap did not
--   run (catalog rows exist, overlap table empty) or InsertHits skipped the bump.
-- Review if: Lighthouse reads overlap_key directly (keep this table for the Dock).
-- Related files: internal/newznabcatalog/store.go, internal/api/newznab_catalog.go

CREATE TABLE newznab_catalog_overlap (
    kind        TEXT PRIMARY KEY,
    host1_nzbs  INTEGER NOT NULL DEFAULT 0,
    host2_nzbs  INTEGER NOT NULL DEFAULT 0,
    both        INTEGER NOT NULL DEFAULT 0,
    only_host1  INTEGER NOT NULL DEFAULT 0,
    only_host2  INTEGER NOT NULL DEFAULT 0
);

-- +goose Down
DROP TABLE IF EXISTS newznab_catalog_overlap;
