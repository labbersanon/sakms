-- +goose Up
-- Claude 2026-10-15: last_demote_gid for per-GID demote dedupe.
-- Reason: live onError + sweep can both see one failure; one GID → one demote.
-- Troubleshooting: grade jumps +2 for one failure — check last_demote_gid.
-- Review if: demote rate limit (10m) is enough alone.
-- Related files: internal/usenetsearch/grades.go DemoteGroup
ALTER TABLE usenet_nntp_group_grades
    ADD COLUMN last_demote_gid TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE usenet_nntp_group_grades DROP COLUMN IF EXISTS last_demote_gid;
