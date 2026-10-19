-- +goose Up
-- Claude 2026-10-18: store md5(md5(title)) for hash-obfuscated Usenet sets.
-- Reason: nZEDb reverse-lookup uses MD5(title), MD5(MD5(title)) (hex-of-hex),
--   and SHA1(title). Live subjects/filenames often carry the inner md5, not
--   md5(title). PHP md5(md5($title)) is md5 of the hex string, which is
--   Postgres md5(title_md5).
-- Troubleshooting: ByHash 32-char miss on a complete yield set → this column
--   empty (re-upsert) or dump not imported.
-- Review if: a third hash form (e.g. sha1(md5(title))) shows up in the wild.
-- Related files: internal/usenetsearch/predb/store.go, dump.go, namefix.go

ALTER TABLE usenet_nntp_predb ADD COLUMN title_md5_md5 TEXT NOT NULL DEFAULT '';
UPDATE usenet_nntp_predb SET title_md5_md5 = md5(title_md5) WHERE title_md5 <> '' AND length(title_md5) = 32;
CREATE INDEX idx_usenet_nntp_predb_md5md5 ON usenet_nntp_predb (title_md5_md5) WHERE title_md5_md5 <> '';

-- +goose Down
DROP INDEX IF EXISTS idx_usenet_nntp_predb_md5md5;
ALTER TABLE usenet_nntp_predb DROP COLUMN title_md5_md5;
