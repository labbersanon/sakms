-- +goose Up
-- verification_status/verified_at back the security-hardening addendum's
-- mapping-verification safeguard: 'verified' (a live listing comparison ran
-- and passed), 'unverified_bootstrap' (a live comparison ran but one or
-- both listings were empty — nothing to compare, not confirmed correct),
-- or 'unverified_approval' (an approve-time row that structurally cannot
-- go through the live comparison at all — a pending node has no
-- authenticated channel yet). SQLite's ALTER TABLE ADD COLUMN can't attach
-- a CHECK constraint retroactively, so the enum is enforced in Go
-- (internal/nodesettings), not the schema.
ALTER TABLE node_path_mappings ADD COLUMN verification_status TEXT NOT NULL DEFAULT 'unverified_bootstrap';
ALTER TABLE node_path_mappings ADD COLUMN verified_at TEXT;

-- +goose Down
ALTER TABLE node_path_mappings DROP COLUMN verified_at;
ALTER TABLE node_path_mappings DROP COLUMN verification_status;
