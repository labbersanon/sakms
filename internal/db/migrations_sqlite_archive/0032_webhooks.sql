-- +goose Up
CREATE TABLE webhooks (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    url        TEXT    NOT NULL,
    secret_enc TEXT    NOT NULL DEFAULT '',
    events     TEXT    NOT NULL DEFAULT '[]',
    enabled    INTEGER NOT NULL DEFAULT 1,
    created_at TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
    updated_at TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now'))
);

-- +goose Down
DROP TABLE webhooks;
