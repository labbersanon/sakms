-- +goose Up
-- pause_dispatch is the operator-controlled dispatch-exclusion bit per node
-- (server-owned pause), stored alongside max_jobs in node_max_jobs. It is
-- written only by the column-scoped nodesettings.Store.SetPauseDispatch upsert
-- (never by the max_jobs writer), so a MaxJobs/PathMap save can never reset it
-- and a pause toggle can never reset max_jobs — the same parallel-write footgun
-- already closed for max_jobs, applied here at the storage layer.
ALTER TABLE node_max_jobs ADD COLUMN pause_dispatch INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE node_max_jobs DROP COLUMN pause_dispatch;
