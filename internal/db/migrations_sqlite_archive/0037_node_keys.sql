-- +goose Up
CREATE TABLE node_keys (
    id          TEXT NOT NULL PRIMARY KEY,
    key_hash    TEXT NOT NULL,
    name        TEXT NOT NULL,
    approved_at TEXT NOT NULL
);

-- +goose Down
DROP TABLE node_keys;
