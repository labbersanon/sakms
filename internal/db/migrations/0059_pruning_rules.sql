-- Claude 2026-08-03: new migration for propose-only pruning rules.
-- Reason: operators need a way to author "delete items older than N days /
-- larger than N bytes / at or below tier X" rules that feed Purge's existing
-- propose phase, without ever bypassing the staged-for-approval Apply step.
-- Troubleshooting: n/a — new feature, no prior bug.
-- Review if: a fourth condition type is ever added (see below — that is a
-- one-line ADD COLUMN, not a table redesign).
-- Context: see .omc/plans/autopilot-impl-pruning-rules.md (§1, §13.3)
--
-- +goose Up
-- Operator-authored propose-only pruning rules for the Purge workflow.
-- A rule is a NAMED set of AND'd conditions scoped to ONE mode; an item
-- matching ANY enabled rule for its mode is staged as a Purge proposal.
-- Nothing here ever deletes: evaluation runs in purge.ScanLibrary*'s propose
-- phase only, and every match still requires a human Apply.
--
-- The three condition columns are deliberately three columns on this row
-- rather than a pruning_rule_conditions child table: the condition set is
-- closed at exactly 3, each has a different natural type, and "at least one
-- condition required" is a struct check rather than a cross-row query. Same
-- one-table + sentinel-default tradeoff discover_sliders (0023) already made;
-- like that table, the conditional validity ("at least one of the three must
-- be set") is enforced in Go (internal/pruning), not by a SQLite CHECK.
--
-- Sentinels for "condition not configured": 0 for age_days/size_bytes,
-- '' for quality_tier_floor. There are no NULLs, so no sql.Null* wrappers.
CREATE TABLE pruning_rules (
    id                 INTEGER PRIMARY KEY AUTOINCREMENT,
    name               TEXT    NOT NULL,
    mode               TEXT    NOT NULL,
    age_days           INTEGER NOT NULL DEFAULT 0,
    size_bytes         INTEGER NOT NULL DEFAULT 0,
    quality_tier_floor TEXT    NOT NULL DEFAULT '',
    enabled            INTEGER NOT NULL DEFAULT 1,
    created_at         TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at         TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE INDEX idx_pruning_rules_mode ON pruning_rules (mode, enabled);

-- +goose Down
DROP TABLE pruning_rules;
