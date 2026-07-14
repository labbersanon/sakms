-- +goose Up
-- Trakt is a single reusable connection (app credentials + one linked
-- account's OAuth tokens), same "one operator, one config" shape as
-- everything else in this single-tenant app — not a per-service row in
-- connections (service TEXT PRIMARY KEY, url, username, api_key_encrypted)
-- because Trakt needs five fields (client_id, client_secret, access_token,
-- refresh_token, token expiry) where connections' fixed shape only has room
-- for one secret. id is pinned to 1 (CHECK) so the table is always exactly
-- zero or one row, upserted via ON CONFLICT(id), the same singleton
-- convention this table's own migration borrows conceptually from
-- internal/auth's OIDC config (settings-store keys, not a table, since OIDC
-- didn't need typed columns or the connections list to show it).
CREATE TABLE trakt_connection (
    id                      INTEGER PRIMARY KEY CHECK (id = 1),
    client_id               TEXT NOT NULL DEFAULT '',
    client_secret_encrypted TEXT NOT NULL DEFAULT '',
    access_token_encrypted  TEXT NOT NULL DEFAULT '',
    refresh_token_encrypted TEXT NOT NULL DEFAULT '',
    token_expires_at        TEXT NOT NULL DEFAULT '',
    created_at              TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at              TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

-- +goose Down
DROP TABLE trakt_connection;
