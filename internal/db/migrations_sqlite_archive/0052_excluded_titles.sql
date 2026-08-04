-- +goose Up
-- Excluded titles: the persisted "never surface this title in Requests again"
-- list. Requests itself is a pure derive-on-read aggregation over the library
-- + in-flight grabs (internal/api/requests.go) with no per-request row, so a
-- "remove" needs its own suppression table rather than a delete of an existing
-- row.
--
-- exclude_key is the mode-scoped identity a Requests row is deduped by
-- (excludes.Key / api.requestKey): "movies:tmdb:123" when a TMDB id is present,
-- else "adult:title:<lowercased title>" for Adult scenes (no TMDB id). It is
-- UNIQUE so re-excluding the same title is idempotent (INSERT ... ON CONFLICT
-- DO NOTHING). mode/tmdb_id/title are kept alongside it for display and so the
-- key can be recomputed if its derivation ever changes.
CREATE TABLE excluded_titles (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    exclude_key TEXT NOT NULL UNIQUE,
    mode        TEXT NOT NULL,
    tmdb_id     INTEGER NOT NULL DEFAULT 0,
    title       TEXT NOT NULL DEFAULT '',
    excluded_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

-- +goose Down
DROP TABLE excluded_titles;
