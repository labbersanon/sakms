-- +goose Up
CREATE TABLE node_path_mappings (
    node_id         TEXT NOT NULL,
    library_path_key TEXT NOT NULL,
    node_path       TEXT NOT NULL,
    updated_at      TEXT NOT NULL,
    PRIMARY KEY (node_id, library_path_key)
);

-- node_max_jobs persists the operator-set concurrency cap per node,
-- alongside node_path_mappings above. Both are needed together for the
-- authoritative reconnect re-push (nodeStreamHandler, on Connect): the wire
-- NodeSettings the node applies carries PathMap and MaxJobs together, and
-- the node's EventSettings handler assigns MaxJobs directly (no merge
-- semantics, unlike PathMap) — pushing an unpersisted MaxJobs as 0 on every
-- automatic reconnect would silently reset an operator's real concurrency
-- limit to "unlimited", the same class of bug already fixed for PathMap.
CREATE TABLE node_max_jobs (
    node_id    TEXT NOT NULL PRIMARY KEY,
    max_jobs   INTEGER NOT NULL,
    updated_at TEXT NOT NULL
);

-- +goose Down
DROP TABLE node_max_jobs;
DROP TABLE node_path_mappings;
