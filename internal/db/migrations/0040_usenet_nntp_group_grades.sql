-- +goose Up
-- Claude 2026-10-15: per-mode NNTP group reliability grades (passive demotion).
-- Reason: groups start at grade 1 (most reliable); each Usenet failure +1 up to 5.
--   Crawl/search only when grade ≤ slider ceiling; beyond ceiling deletes index.
-- Troubleshooting: usenet_nntp_group_grades; DemoteGroup / EligibleGroups.
-- Review if: grades become global per group_name (shared multimedia).
-- Related files: internal/usenetsearch/grades.go, crawler.go, usenetretry.go
CREATE TABLE usenet_nntp_group_grades (
    mode           TEXT NOT NULL,
    group_name     TEXT NOT NULL,
    grade          INTEGER NOT NULL DEFAULT 1,
    fail_count     BIGINT NOT NULL DEFAULT 0,
    last_failed_at BIGINT NOT NULL DEFAULT 0,
    PRIMARY KEY (mode, group_name)
);
CREATE INDEX idx_usenet_nntp_group_grades_grade ON usenet_nntp_group_grades (mode, grade);

-- +goose Down
DROP TABLE IF EXISTS usenet_nntp_group_grades;
