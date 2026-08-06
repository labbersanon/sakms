-- +goose Up
-- Claude 2026-08-06: unique index on live proposals by source_path.
-- Reason: ReplacePending now upserts by source_path instead of delete-all+insert,
--   so IDs are stable across rescans and apply-batch lookups can't race with scan.
--   The index enforces the invariant: no two live (pending/unmatched) rows for the
--   same (mode, workflow, source_path), preventing duplicate-path confusion at scan.
-- Troubleshooting: INSERT of a duplicate source_path in the live queue will now
--   fail with a unique-constraint error instead of silently adding a stale twin.
-- Review if: source_path uniqueness is relaxed (e.g. multi-file proposals).
CREATE UNIQUE INDEX proposals_live_source_path_uidx
  ON proposals (mode, workflow, source_path)
  WHERE status IN ('pending', 'unmatched') AND source_path <> '';

-- +goose Down
DROP INDEX IF EXISTS proposals_live_source_path_uidx;
