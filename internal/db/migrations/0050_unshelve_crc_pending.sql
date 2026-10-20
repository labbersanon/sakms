-- +goose Up
-- Claude 2026-10-20: unshelve complete hash.rar/sfv so first-volume FILE_CRC can run.
-- Reason: #226/#227 pickBodyTargets filled namefixBodiesPerSet with vol PAR2,
--   then ShelveBodyAttempts set name_attempts=max. DuePendingNames never
--   retried part01; name_source=srrdb stayed 0.
-- Troubleshooting: pending95 rar with attempts>=max and no namefix crc log
--   after this deploy → migration not applied. Review if: crc_sampled column
--   replaces this one-shot reset.
-- Related files: internal/usenetsearch/namefix.go pickBodyTargets

UPDATE usenet_nntp_releases
SET name_attempts=0, next_name_at=0, updated_at=EXTRACT(EPOCH FROM NOW())::BIGINT
WHERE state='pending_name'
  AND name_attempts>=3
  AND EXISTS (
    SELECT 1 FROM usenet_nntp_headers h
    WHERE h.group_name=usenet_nntp_releases.group_name
      AND h.release_name=usenet_nntp_releases.release_key
      AND (
        LOWER(h.filename) LIKE '%.rar%' OR LOWER(h.filename) LIKE '%.sfv%'
        OR LOWER(h.filename) LIKE '%.zip%' OR LOWER(h.filename) LIKE '%.7z%'
        OR LOWER(h.subject) LIKE '%.rar%' OR LOWER(h.subject) LIKE '%.sfv%'
        OR LOWER(h.subject) LIKE '%.zip%' OR LOWER(h.subject) LIKE '%.7z%'
      )
  );

-- +goose Down
-- Cannot re-shelf; the reset exists so CRC BODY can run once.
UPDATE usenet_nntp_releases SET name_attempts=name_attempts WHERE FALSE;
