-- +goose Up
-- availability_watch is the watchlist the background recheck job iterates (see
-- internal/recheck) — the set of picks an operator has flagged "keep checking
-- whether a release exists for this." It is a DELIBERATE, opt-in exception to
-- this project's "manual by default, no background pollers" convention, added
-- at explicit user request (Stage 8 of the Search/indexer-inversion plan). The
-- table is its own, shared with no workflow package.
--
-- Columns are explicit and typed rather than a collapsed opaque key pair, the
-- same house preference already applied to library_scenes' (box, scene_id):
-- Movies/Series probe by (tmdb_id [+ season/episode]); Adult — which has no
-- tmdb/imdb/tvdb id — probes by (studio, title). The UNIQUE tuple over every
-- key column keeps a given pick registered exactly once regardless of mode.
CREATE TABLE availability_watch (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    mode            TEXT NOT NULL,
    tmdb_id         INTEGER NOT NULL DEFAULT 0,  -- Movies/Series identity
    season          INTEGER NOT NULL DEFAULT 0,  -- Series scoping (0 = whole show)
    episode         INTEGER NOT NULL DEFAULT 0,  -- Series scoping (0 = whole season)
    studio          TEXT NOT NULL DEFAULT '',    -- Adult identity
    title           TEXT NOT NULL DEFAULT '',    -- Adult identity
    added_at        TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    last_checked_at TEXT NOT NULL DEFAULT '',    -- '' = never checked → always due
    last_available  INTEGER NOT NULL DEFAULT 0,  -- last probe's result, as 0/1
    UNIQUE (mode, tmdb_id, season, episode, studio, title)
);

CREATE INDEX idx_availability_watch_last_checked ON availability_watch (last_checked_at);

-- +goose Down
DROP TABLE availability_watch;
