-- +goose Up
-- Claude 2026-10-23: On Demand shelf is status=verified, no URL columns.
-- Reason: Discover ?view=ondemand reads this table; do not recrawl on render.
-- Troubleshooting: empty On Demand grid → crawler still fetching/STAT.
-- Review if: Play starts-while-downloading (still no enclosure/apikey columns).
-- Related files: internal/newznabondemand, internal/api/newznab_ondemand.go

CREATE TABLE newznab_ondemand (
    indexer          TEXT NOT NULL,
    guid             TEXT NOT NULL,
    kind             TEXT NOT NULL DEFAULT '',
    title            TEXT NOT NULL DEFAULT '',
    status           TEXT NOT NULL DEFAULT 'pending',
    streamable       BOOLEAN NOT NULL DEFAULT FALSE,
    reject_reason    TEXT NOT NULL DEFAULT '',
    nzb_relpath      TEXT NOT NULL DEFAULT '',
    payload_files    INTEGER NOT NULL DEFAULT 0,
    payload_segments INTEGER NOT NULL DEFAULT 0,
    stat_checked     INTEGER NOT NULL DEFAULT 0,
    stat_missing     INTEGER NOT NULL DEFAULT 0,
    plan_json        TEXT NOT NULL DEFAULT '',
    updated_at       BIGINT NOT NULL DEFAULT 0,
    verified_at      BIGINT NOT NULL DEFAULT 0,
    PRIMARY KEY (indexer, guid)
);
CREATE INDEX idx_newznab_ondemand_status ON newznab_ondemand (status);
CREATE INDEX idx_newznab_ondemand_kind_status ON newznab_ondemand (kind, status);

-- +goose Down
DROP TABLE IF EXISTS newznab_ondemand;
