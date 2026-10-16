-- +goose Up
-- Claude 2026-10-15: success streak for passive NNTP group promotion.
-- Reason: demote on article failure; promote one tier after 2 successful imports.
-- Troubleshooting: success_streak resets on demote; PromoteForSuccess logs promote.
-- Review if: promote threshold becomes a Settings knob.
-- Related files: internal/usenetsearch/grades.go, import.go UsenetCompleteImporter
ALTER TABLE usenet_nntp_group_grades
    ADD COLUMN success_streak INTEGER NOT NULL DEFAULT 0;
ALTER TABLE usenet_nntp_group_grades
    ADD COLUMN last_success_at BIGINT NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE usenet_nntp_group_grades DROP COLUMN IF EXISTS last_success_at;
ALTER TABLE usenet_nntp_group_grades DROP COLUMN IF EXISTS success_streak;
