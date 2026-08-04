-- +goose Up
CREATE TABLE connections (
    service           TEXT PRIMARY KEY,
    url               TEXT NOT NULL,
    api_key_encrypted TEXT NOT NULL DEFAULT '',
    created_at        TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at        TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

-- +goose Down
DROP TABLE connections;
