-- +goose Up
-- cpu_cap_percent is the operator-controlled max-CPU governor per node
-- ("% of total CPU" slider, 0 = unlimited), stored alongside max_jobs in
-- node_max_jobs. Like max_jobs (and UNLIKE pause_dispatch), it is operator-
-- owned and rides the shared nodesettings.Store.Set write path — both are
-- operator knobs with no parallel-write conflict, so they share one writer.
-- Stage 2 (backend persistence + wire path) of the node-resource-governor
-- plan; the node daemon that actually enforces the cap lands in Stage 3.
ALTER TABLE node_max_jobs ADD COLUMN cpu_cap_percent INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE node_max_jobs DROP COLUMN cpu_cap_percent;
