-- +goose Up
-- Claude 2026-10-23: drop remaining native NNTP OVER index tables.
-- Reason: Searchable ~4500 after namefix/PreDB was not an indexer catalog.
--   Discover/grab is Newznab (NZBGeek + optional NZBFinder) then Prowlarr.
--   Eweka NNTP stays for AddNZB download and subscriptions. Do not scrape
--   indexer HTML. Do not VACUUM FULL.
-- Troubleshooting: relation usenet_nntp_headers does not exist after deploy
--   is expected. In-flight sakms-nntp: grab URLs cannot relaunch.
-- Review if: a future local OVER index is added (new tables, do not recreate
--   these names).
-- Related files: docs/usenet-nntp-native.md, internal/usenet (download)

DROP TABLE IF EXISTS usenet_nntp_par_hashes;
DROP TABLE IF EXISTS usenet_nntp_skips;
DROP TABLE IF EXISTS usenet_nntp_missed_ranges;
DROP TABLE IF EXISTS usenet_nntp_releases;
DROP TABLE IF EXISTS usenet_nntp_group_grades;
DROP TABLE IF EXISTS usenet_nntp_watermarks;
DROP TABLE IF EXISTS usenet_nntp_headers;
DELETE FROM settings WHERE key LIKE 'usenet_nntp_%';
DELETE FROM settings WHERE key IN (
    'movies_nntp_native_enabled',
    'series_nntp_native_enabled',
    'adult_nntp_native_enabled'
);

-- +goose Down
-- Recreate the last known schema (0026 + 0040–0042 + 0044 + 0046 + 0049)
-- so goose down of a later migration can reverse this one. Rows are not
-- restored. usenet_nntp_predb stays dropped (0052).
CREATE TABLE IF NOT EXISTS usenet_nntp_headers (
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
    file_n       INTEGER NOT NULL DEFAULT 0,
    file_m       INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (group_name, msgid)
);
CREATE INDEX IF NOT EXISTS idx_usenet_nntp_headers_release ON usenet_nntp_headers (group_name, release_name);
CREATE INDEX IF NOT EXISTS idx_usenet_nntp_headers_posted ON usenet_nntp_headers (posted_at);
CREATE INDEX IF NOT EXISTS idx_usenet_nntp_headers_ingested ON usenet_nntp_headers (ingested_at, posted_at);
CREATE INDEX IF NOT EXISTS idx_usenet_nntp_headers_prune ON usenet_nntp_headers ((COALESCE(NULLIF(posted_at, 0), ingested_at)));

CREATE TABLE IF NOT EXISTS usenet_nntp_watermarks (
    group_name       TEXT PRIMARY KEY,
    high             BIGINT NOT NULL DEFAULT 0,
    updated_at       BIGINT NOT NULL DEFAULT 0,
    low              BIGINT NOT NULL DEFAULT 0,
    backfill_done    BOOLEAN NOT NULL DEFAULT FALSE,
    backfill_reason  TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS usenet_nntp_group_grades (
    mode            TEXT NOT NULL,
    group_name      TEXT NOT NULL,
    grade           INTEGER NOT NULL DEFAULT 1,
    fail_count      BIGINT NOT NULL DEFAULT 0,
    last_failed_at  BIGINT NOT NULL DEFAULT 0,
    success_streak  INTEGER NOT NULL DEFAULT 0,
    last_success_at BIGINT NOT NULL DEFAULT 0,
    last_demote_gid TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (mode, group_name)
);
CREATE INDEX IF NOT EXISTS idx_usenet_nntp_group_grades_grade ON usenet_nntp_group_grades (mode, grade);

CREATE TABLE IF NOT EXISTS usenet_nntp_releases (
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
    parhash_sampled BOOLEAN NOT NULL DEFAULT FALSE,
    PRIMARY KEY (group_name, release_key)
);
CREATE INDEX IF NOT EXISTS idx_usenet_nntp_releases_search ON usenet_nntp_releases (searchable, group_name);
CREATE INDEX IF NOT EXISTS idx_usenet_nntp_releases_state ON usenet_nntp_releases (state, next_name_at);
CREATE INDEX IF NOT EXISTS idx_usenet_nntp_releases_dirty ON usenet_nntp_releases (updated_at) WHERE dirty;

CREATE TABLE IF NOT EXISTS usenet_nntp_missed_ranges (
    group_name  TEXT    NOT NULL,
    lo          BIGINT  NOT NULL,
    hi          BIGINT  NOT NULL,
    reason      TEXT    NOT NULL DEFAULT 'gap',
    attempts    INTEGER NOT NULL DEFAULT 0,
    next_try_at BIGINT  NOT NULL DEFAULT 0,
    PRIMARY KEY (group_name, lo, hi)
);
CREATE INDEX IF NOT EXISTS idx_usenet_nntp_missed_ranges_next ON usenet_nntp_missed_ranges (next_try_at);

CREATE TABLE IF NOT EXISTS usenet_nntp_skips (
    group_name  TEXT   NOT NULL,
    release_key TEXT   NOT NULL,
    reason      TEXT   NOT NULL DEFAULT 'incomplete',
    first_msg   BIGINT NOT NULL DEFAULT 0,
    last_msg    BIGINT NOT NULL DEFAULT 0,
    skipped_at  BIGINT NOT NULL DEFAULT 0,
    PRIMARY KEY (group_name, release_key)
);

CREATE TABLE IF NOT EXISTS usenet_nntp_par_hashes (
    hash        TEXT   NOT NULL,
    group_name  TEXT   NOT NULL,
    release_key TEXT   NOT NULL,
    file_length BIGINT NOT NULL DEFAULT 0,
    stored_at   BIGINT NOT NULL DEFAULT 0,
    PRIMARY KEY (hash, group_name, release_key)
);
CREATE INDEX IF NOT EXISTS idx_usenet_nntp_par_hashes_release ON usenet_nntp_par_hashes (group_name, release_key);
