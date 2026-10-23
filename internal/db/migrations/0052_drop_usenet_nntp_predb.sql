-- +goose Up
-- Claude 2026-10-22: drop usenet_nntp_predb and PreDB/IRC settings.
-- Reason: dump/IRC reverse-lookup never named live ngPost random .mkv
--   stems (join key miss vs 6.9M dump rows). Namefix keeps PAR2/NFO/file/
--   yEnc/srrdb/NZB-password. Do not scrape a replacement catalog.
-- Troubleshooting: relation usenet_nntp_predb does not exist after deploy
--   is expected; leftover /data/predb on the volume is deleted on ship.
-- Review if: a future name source needs a local title corpus (new table,
--   do not recreate this one).
-- Related files: internal/usenetsearch/namefix.go, docs/usenet-nntp-native.md

DROP TABLE IF EXISTS usenet_nntp_predb;
DELETE FROM settings WHERE key LIKE 'usenet_nntp_predb%';

-- +goose Down
-- Recreate the last known schema (0044 + 0047 md5md5 + 0048 requestid) so
-- goose down of a later migration can reverse this one. Autovacuum
-- reloptions from 0051 and dump rows are not restored.
CREATE TABLE IF NOT EXISTS usenet_nntp_predb (
    title         TEXT    PRIMARY KEY,
    title_md5     TEXT    NOT NULL DEFAULT '',
    title_sha1    TEXT    NOT NULL DEFAULT '',
    size_bytes    BIGINT  NOT NULL DEFAULT 0,
    files         INTEGER NOT NULL DEFAULT 0,
    filename      TEXT    NOT NULL DEFAULT '',
    category      TEXT    NOT NULL DEFAULT '',
    pre_at        BIGINT  NOT NULL DEFAULT 0,
    nuked         BOOLEAN NOT NULL DEFAULT FALSE,
    source        TEXT    NOT NULL DEFAULT 'http',
    fetched_at    BIGINT  NOT NULL DEFAULT 0,
    title_md5_md5 TEXT    NOT NULL DEFAULT '',
    request_id    BIGINT  NOT NULL DEFAULT 0,
    group_name    TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_usenet_nntp_predb_md5 ON usenet_nntp_predb (title_md5) WHERE title_md5 <> '';
CREATE INDEX IF NOT EXISTS idx_usenet_nntp_predb_sha1 ON usenet_nntp_predb (title_sha1) WHERE title_sha1 <> '';
CREATE INDEX IF NOT EXISTS idx_usenet_nntp_predb_filename ON usenet_nntp_predb (lower(filename)) WHERE filename <> '';
CREATE INDEX IF NOT EXISTS idx_usenet_nntp_predb_md5md5 ON usenet_nntp_predb (title_md5_md5) WHERE title_md5_md5 <> '';
CREATE INDEX IF NOT EXISTS idx_usenet_nntp_predb_request ON usenet_nntp_predb (request_id) WHERE request_id > 0;
