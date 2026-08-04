-- +goose Up
CREATE TABLE settings (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

-- +goose Down
DROP TABLE settings;
