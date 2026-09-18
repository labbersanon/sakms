-- +goose Up
-- Claude 2026-09-18: native NNTP header index tables (Postgres).
-- Reason: cmd/sakms must not import modernc.org/sqlite after Postgres cutover
--   (TestCmdSakms_DoesNotImportModerncSQLite). The index still lives in dedicated
--   tables, pruned by window/size settings — not a sidecar SQLite file.
-- Troubleshooting: native search Ready=degraded "index empty" until first crawl.
-- Review if: index is split to a separate Postgres database for size isolation.
CREATE TABLE usenet_nntp_headers (
    group_name   TEXT NOT NULL,
    msg_num      BIGINT NOT NULL,
    msgid        TEXT NOT NULL,
    subject      TEXT NOT NULL DEFAULT '',
    from_addr    TEXT NOT NULL DEFAULT '',
    posted_at    BIGINT NOT NULL DEFAULT 0,
    bytes        BIGINT NOT NULL DEFAULT 0,
    release_name TEXT NOT NULL DEFAULT '',
    part_n       INTEGER NOT NULL DEFAULT 0,
    part_m       INTEGER NOT NULL DEFAULT 0,
    filename     TEXT NOT NULL DEFAULT '',
    yenc_size    BIGINT NOT NULL DEFAULT 0,
    obfuscated   BOOLEAN NOT NULL DEFAULT FALSE,
    is_meta      BOOLEAN NOT NULL DEFAULT FALSE,
    ingested_at  BIGINT NOT NULL DEFAULT 0,
    PRIMARY KEY (group_name, msgid)
);
CREATE INDEX idx_usenet_nntp_headers_release ON usenet_nntp_headers (group_name, release_name);
CREATE INDEX idx_usenet_nntp_headers_posted ON usenet_nntp_headers (posted_at);
CREATE INDEX idx_usenet_nntp_headers_ingested ON usenet_nntp_headers (ingested_at, posted_at);

CREATE TABLE usenet_nntp_watermarks (
    group_name TEXT PRIMARY KEY,
    high       BIGINT NOT NULL DEFAULT 0,
    updated_at BIGINT NOT NULL DEFAULT 0
);

-- +goose Down
DROP TABLE IF EXISTS usenet_nntp_watermarks;
DROP TABLE IF EXISTS usenet_nntp_headers;
