-- +goose Up
-- Claude 2026-09-17: origin column for provenance tagging.
-- Reason: retry_reason is operator-facing copy rendered on the Requests screen.
--   The repo has a documented HIGH-severity history (airdatemonitor.go's
--   airDateShaped doc, migration 0022) of reason-string inference causing
--   misclassification — a *destructive* reap must never key off copy. A separate
--   column is the established pattern (see hold_until, monitor_entity_key,
--   next_search_scope).
-- Allowed values: '' (production), 'e2e' (test/verification debris).
-- Review if: a third origin value is ever needed (add it here, not to retry_reason).
ALTER TABLE grabs ADD COLUMN origin TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE grabs DROP COLUMN origin;
