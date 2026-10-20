-- +goose Up
-- Claude 2026-10-20: persist PAR2 FileDesc hash_16K for donor rename.
-- Reason: NNTmux fixNamesWithParHash matches MD5(first 16KiB) across already-
--   named releases when FileDesc names are also hashes (PreDB/IRC FN miss).
-- Troubleshooting: name_source=par2 FileDesc titles exist but hash.par2 stays
--   pending → this table empty (BODY never stored hashes) or donor size
--   outside ±5%. Review if: mediainfo UID / CRC32 donors are added.
-- Related files: internal/usenetsearch/par2names.go, parhash.go, namefix.go

CREATE TABLE usenet_nntp_par_hashes (
    hash        TEXT   NOT NULL,
    group_name  TEXT   NOT NULL,
    release_key TEXT   NOT NULL,
    file_length BIGINT NOT NULL DEFAULT 0,
    stored_at   BIGINT NOT NULL DEFAULT 0,
    PRIMARY KEY (hash, group_name, release_key)
);
CREATE INDEX idx_usenet_nntp_par_hashes_release ON usenet_nntp_par_hashes (group_name, release_key);

ALTER TABLE usenet_nntp_releases ADD COLUMN parhash_sampled BOOLEAN NOT NULL DEFAULT FALSE;

-- +goose Down
DROP INDEX IF EXISTS idx_usenet_nntp_par_hashes_release;
DROP TABLE IF EXISTS usenet_nntp_par_hashes;
ALTER TABLE usenet_nntp_releases DROP COLUMN parhash_sampled;
