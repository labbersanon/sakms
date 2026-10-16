-- +goose Up
-- Claude 2026-10-15: NNTP gap fill (approved plan .omc/plans/nntp-gap-fill.md).
-- Reason: split the grouping key (headers.release_name, immutable) from the
--   searchable display name; carry strict-completeness + name-recovery state
--   on a release-level row; queue OVER gaps for repair; keep a local PreDB;
--   add a backfill cursor (watermarks.low) separate from the forward `high`.
-- Troubleshooting: search empty right after this migration is expected until
--   the recompute pass drains dirty=TRUE rows (log: "completeness recompute
--   ... backlog=N"). Undo a rename: UPDATE usenet_nntp_releases SET
--   display_name='', state='pending_name', name_source='', dirty=TRUE WHERE ...
-- Review if: the release table moves off the app Postgres or headers gain a
--   surrogate release_id.
-- Related files: internal/usenetsearch/releases.go, completeness.go, repair.go,
--   namefix.go, backfill.go, predb/

CREATE TABLE usenet_nntp_releases (
    group_name      TEXT    NOT NULL,
    release_key     TEXT    NOT NULL,
    display_name    TEXT    NOT NULL DEFAULT '',
    name_source     TEXT    NOT NULL DEFAULT '',
    name_confidence TEXT    NOT NULL DEFAULT '',
    state           TEXT    NOT NULL DEFAULT 'named',
    name_attempts   INTEGER NOT NULL DEFAULT 0,
    next_name_at    BIGINT  NOT NULL DEFAULT 0,
    declared_segs   INTEGER NOT NULL DEFAULT 0,
    seen_segs       INTEGER NOT NULL DEFAULT 0,
    completeness_bp INTEGER NOT NULL DEFAULT 0,
    gate_reason     TEXT    NOT NULL DEFAULT '',
    searchable      BOOLEAN NOT NULL DEFAULT FALSE,
    first_msg       BIGINT  NOT NULL DEFAULT 0,
    last_msg        BIGINT  NOT NULL DEFAULT 0,
    posted_at       BIGINT  NOT NULL DEFAULT 0,
    payload_bytes   BIGINT  NOT NULL DEFAULT 0,
    has_par2        BOOLEAN NOT NULL DEFAULT FALSE,
    dirty           BOOLEAN NOT NULL DEFAULT FALSE,
    span_requeued   BOOLEAN NOT NULL DEFAULT FALSE,
    ingested_at     BIGINT  NOT NULL DEFAULT 0,
    updated_at      BIGINT  NOT NULL DEFAULT 0,
    PRIMARY KEY (group_name, release_key)
);
CREATE INDEX idx_usenet_nntp_releases_search ON usenet_nntp_releases (searchable, group_name);
CREATE INDEX idx_usenet_nntp_releases_state ON usenet_nntp_releases (state, next_name_at);
CREATE INDEX idx_usenet_nntp_releases_dirty ON usenet_nntp_releases (updated_at) WHERE dirty;

CREATE TABLE usenet_nntp_missed_ranges (
    group_name  TEXT    NOT NULL,
    lo          BIGINT  NOT NULL,
    hi          BIGINT  NOT NULL,
    reason      TEXT    NOT NULL DEFAULT 'gap',
    attempts    INTEGER NOT NULL DEFAULT 0,
    next_try_at BIGINT  NOT NULL DEFAULT 0,
    PRIMARY KEY (group_name, lo, hi)
);
CREATE INDEX idx_usenet_nntp_missed_ranges_next ON usenet_nntp_missed_ranges (next_try_at);

CREATE TABLE usenet_nntp_predb (
    title      TEXT   PRIMARY KEY,
    title_md5  TEXT   NOT NULL DEFAULT '',
    title_sha1 TEXT   NOT NULL DEFAULT '',
    size_bytes BIGINT NOT NULL DEFAULT 0,
    files      INTEGER NOT NULL DEFAULT 0,
    filename   TEXT   NOT NULL DEFAULT '',
    category   TEXT   NOT NULL DEFAULT '',
    pre_at     BIGINT NOT NULL DEFAULT 0,
    nuked      BOOLEAN NOT NULL DEFAULT FALSE,
    source     TEXT   NOT NULL DEFAULT 'http',
    fetched_at BIGINT NOT NULL DEFAULT 0
);
CREATE INDEX idx_usenet_nntp_predb_md5 ON usenet_nntp_predb (title_md5) WHERE title_md5 <> '';
CREATE INDEX idx_usenet_nntp_predb_sha1 ON usenet_nntp_predb (title_sha1) WHERE title_sha1 <> '';
CREATE INDEX idx_usenet_nntp_predb_filename ON usenet_nntp_predb (lower(filename)) WHERE filename <> '';

ALTER TABLE usenet_nntp_headers ADD COLUMN file_n INTEGER NOT NULL DEFAULT 0;
ALTER TABLE usenet_nntp_headers ADD COLUMN file_m INTEGER NOT NULL DEFAULT 0;
-- Backs PruneOldest's oldest-post-first ORDER BY (plan D6).
CREATE INDEX idx_usenet_nntp_headers_prune ON usenet_nntp_headers ((COALESCE(NULLIF(posted_at, 0), ingested_at)));

ALTER TABLE usenet_nntp_watermarks ADD COLUMN low BIGINT NOT NULL DEFAULT 0;
ALTER TABLE usenet_nntp_watermarks ADD COLUMN backfill_done BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE usenet_nntp_watermarks ADD COLUMN backfill_reason TEXT NOT NULL DEFAULT '';

INSERT INTO usenet_nntp_releases (group_name, release_key, display_name, name_source, name_confidence,
    state, first_msg, last_msg, posted_at, ingested_at, updated_at, dirty)
SELECT group_name, release_name, release_name, 'subject', 'subject',
       'named', MIN(msg_num), MAX(msg_num), MAX(posted_at), MIN(ingested_at), MAX(ingested_at), TRUE
FROM usenet_nntp_headers
WHERE obfuscated = FALSE AND release_name <> ''
GROUP BY group_name, release_name;

UPDATE usenet_nntp_watermarks w
SET low = COALESCE((SELECT MIN(h.msg_num) FROM usenet_nntp_headers h WHERE h.group_name = w.group_name), w.high);

-- +goose Down
ALTER TABLE usenet_nntp_watermarks DROP COLUMN IF EXISTS backfill_reason;
ALTER TABLE usenet_nntp_watermarks DROP COLUMN IF EXISTS backfill_done;
ALTER TABLE usenet_nntp_watermarks DROP COLUMN IF EXISTS low;
DROP INDEX IF EXISTS idx_usenet_nntp_headers_prune;
ALTER TABLE usenet_nntp_headers DROP COLUMN IF EXISTS file_m;
ALTER TABLE usenet_nntp_headers DROP COLUMN IF EXISTS file_n;
DROP TABLE IF EXISTS usenet_nntp_predb;
DROP TABLE IF EXISTS usenet_nntp_missed_ranges;
DROP TABLE IF EXISTS usenet_nntp_releases;
