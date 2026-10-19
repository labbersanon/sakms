-- +goose Up
-- Claude 2026-10-19: store nZEDb requestid + groupname for wrapper posts.
-- Reason: EFNet/Bin-Req subjects carry a numeric request id, not a hash;
--   namefix can join that id to a dump/IRC [RQ:] row after the wrapper is
--   stripped (or when the title bracket is empty and the set is staged).
-- Troubleshooting: ByRequestID miss on a req: skeleton set → this column is
--   0 (dump had requestid=0 / IRC [RQ: N/A]) or group_name does not match.
-- Review if: a dump format stores request ids under a different header.
-- Related files: internal/usenetsearch/predb/store.go, dump.go, parse.go, namefix.go

ALTER TABLE usenet_nntp_predb ADD COLUMN request_id BIGINT NOT NULL DEFAULT 0;
ALTER TABLE usenet_nntp_predb ADD COLUMN group_name TEXT NOT NULL DEFAULT '';
CREATE INDEX idx_usenet_nntp_predb_request ON usenet_nntp_predb (request_id) WHERE request_id > 0;

-- +goose Down
DROP INDEX IF EXISTS idx_usenet_nntp_predb_request;
ALTER TABLE usenet_nntp_predb DROP COLUMN request_id;
ALTER TABLE usenet_nntp_predb DROP COLUMN group_name;
