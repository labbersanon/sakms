-- +goose Up
-- Claude 2026-10-18: persistent skip stubs for settled incomplete NNTP sets.
-- Reason: SweepIncomplete deleted headers and forgot the key, so backfill
--   re-admitted the same 0% scraps into the 1.3GB index. Searchable stays
--   named+≥95%; in-window and ≥80% sets keep headers.
-- Troubleshooting: crawl re-upserts a swept release → this table missed the
--   key or ingest did not call DropSkipped.
-- Review if: skip rows are pruned by age separately from headers.
-- Related files: internal/usenetsearch/skip.go, ingest.go, releases.go

CREATE TABLE usenet_nntp_skips (
    group_name  TEXT   NOT NULL,
    release_key TEXT   NOT NULL,
    reason      TEXT   NOT NULL DEFAULT 'incomplete',
    first_msg   BIGINT NOT NULL DEFAULT 0,
    last_msg    BIGINT NOT NULL DEFAULT 0,
    skipped_at  BIGINT NOT NULL DEFAULT 0,
    PRIMARY KEY (group_name, release_key)
);

-- +goose Down
DROP TABLE IF EXISTS usenet_nntp_skips;
