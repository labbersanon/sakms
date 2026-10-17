-- +goose Up
-- Claude 2026-10-16: recompute pending_name payload_bytes after per-file yEnc.
-- Reason: payload += yenc ran once per segment, so unnamed cap read TiB and
--   skip_obf_cap locked every core. dirty=TRUE drains through RecomputeDirty.
-- Troubleshooting: completeness recompute backlog=N after this migration is
--   expected until payload_bytes drops to one yEnc size per file.
-- Review if: payload_bytes is replaced by SUM(headers.bytes) for the cap.
-- Related files: internal/usenetsearch/completeness.go addFilePayload,
--   releases.go recomputeOne, group.go assembleFromRows

UPDATE usenet_nntp_releases SET dirty = TRUE WHERE state = 'pending_name';

-- +goose Down
-- No-op: the dirty flag only schedules a recompute; nothing to undo.
SELECT 1;
