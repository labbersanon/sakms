-- +goose Up
-- The shared multi-connection registry: Usenet subscriptions and media players,
-- the only two service classes an operator can configure more than one of.
--
-- Deliberately NOT named `connections`. internal/connections and its
-- `connections` table both survive for the ~7 singleton services (TMDB, TVDB,
-- StashDB, FansDB, TPDB, Trakt, AI) whose PRIMARY KEY(service) shape is still
-- correct — exactly TWO rows move out of it here, nntp and jellyfin. Two
-- same-named tables would be a permanent readability trap.
CREATE TABLE service_connections (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    kind             TEXT NOT NULL,                 -- 'usenet' | 'player'
    provider         TEXT NOT NULL,                 -- 'nntp' | 'jellyfin' | 'emby' | 'plex'
    label            TEXT NOT NULL DEFAULT '',
    url              TEXT NOT NULL DEFAULT '',      -- player-shaped
    host             TEXT NOT NULL DEFAULT '',      -- usenet-shaped
    port             INTEGER NOT NULL DEFAULT 0,
    tls              INTEGER NOT NULL DEFAULT 0,
    username         TEXT NOT NULL DEFAULT '',
    max_conns        INTEGER NOT NULL DEFAULT 0,
    secret_encrypted TEXT NOT NULL DEFAULT '',
    enabled          INTEGER NOT NULL DEFAULT 1,
    sort_order       INTEGER NOT NULL DEFAULT 0,
    created_at       TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at       TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);
CREATE INDEX idx_service_connections_kind ON service_connections (kind, enabled);

-- Mode assignment is many-to-many with no exclusivity: one player may serve any
-- subset of the three modes. ON DELETE CASCADE below is DOCUMENTATION OF INTENT
-- ONLY — internal/db's shared Open never runs `PRAGMA foreign_keys = ON`, so
-- SQLite does not enforce it. serviceconn.Store.Delete does the two-statement
-- delete explicitly, the same workaround library.Store.Delete already documents.
CREATE TABLE service_connection_modes (
    connection_id INTEGER NOT NULL REFERENCES service_connections(id) ON DELETE CASCADE,
    mode          TEXT NOT NULL,                    -- 'movies' | 'series' | 'adult'
    PRIMARY KEY (connection_id, mode)
);

-- Move the two multi-capable rows out of `connections`. api_key_encrypted is
-- copied VERBATIM: same internal/secrets key, so no decrypt/re-encrypt step and
-- no Go backfill is needed for the ciphertext itself. host/port/tls stay zeroed
-- here because SQLite cannot parse the legacy nntp:// URL — serviceconn's
-- BackfillUsenetURL does that at the next boot (see internal/serviceconn/backfill.go).
INSERT INTO service_connections (kind, provider, label, url, username, secret_encrypted, enabled)
SELECT 'usenet', 'nntp', 'Usenet', url, username, api_key_encrypted, 1
  FROM connections WHERE service = 'nntp';

INSERT INTO service_connections (kind, provider, label, url, username, secret_encrypted, enabled)
SELECT 'player', 'jellyfin', 'Jellyfin', url, username, api_key_encrypted, 1
  FROM connections WHERE service = 'jellyfin';

-- Preserve today's hardcoded Jellyfin = Movies/Series scoping exactly.
INSERT INTO service_connection_modes (connection_id, mode)
SELECT id, 'movies' FROM service_connections WHERE provider = 'jellyfin';
INSERT INTO service_connection_modes (connection_id, mode)
SELECT id, 'series' FROM service_connections WHERE provider = 'jellyfin';

DELETE FROM connections WHERE service IN ('nntp', 'jellyfin');

-- +goose Down
-- DELIBERATELY LOSSY, and it has to be: connections.service is TEXT PRIMARY KEY
-- (0002_connections.sql), so at most ONE row per provider can be restored. The
-- entire point of this feature is multiple jellyfin/nntp rows, which the old
-- schema cannot represent. Restore the lowest-id row per provider and drop the
-- rest. Reconstruct the nntp URL from host/port/tls, because BackfillUsenetURL
-- blanks url — restoring an empty url would make usenet.ParseURL fail and leave
-- a rolled-back install non-functional.
INSERT INTO connections (service, url, username, api_key_encrypted)
SELECT 'nntp',
       CASE WHEN url <> '' THEN url
            ELSE (CASE WHEN tls = 1 THEN 'nntps://' ELSE 'nntp://' END)
                 || host || ':' || CAST(port AS TEXT) END,
       username, secret_encrypted
  FROM service_connections
 WHERE provider = 'nntp'
   AND id = (SELECT MIN(id) FROM service_connections WHERE provider = 'nntp');

INSERT INTO connections (service, url, username, api_key_encrypted)
SELECT 'jellyfin', url, username, secret_encrypted
  FROM service_connections
 WHERE provider = 'jellyfin'
   AND id = (SELECT MIN(id) FROM service_connections WHERE provider = 'jellyfin');

DROP TABLE service_connection_modes;
DROP TABLE service_connections;
