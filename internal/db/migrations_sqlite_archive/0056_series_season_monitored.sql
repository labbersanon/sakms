-- +goose Up
-- Per-(series, season) monitored state. A TABLE, not a column on
-- library_episodes: a season can legitimately have ZERO episode rows (a season
-- discovered from TMDB before its episodes are synced, or a season nobody has
-- ever downloaded from), so there is no episode row to hang the flag on. A
-- per-episode column would also store N copies of one season-level fact, which
-- UpsertEpisode's per-row writes could leave divergent.
--
-- An ABSENT ROW MEANS UNMONITORED. There is no NULL/tri-state: detection reads
-- this as a plain "is this (series, season) present with monitored = 1".
CREATE TABLE library_season_monitored (
    series_id     INTEGER NOT NULL,
    season_number INTEGER NOT NULL,
    monitored     INTEGER NOT NULL DEFAULT 0,
    created_at    TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at    TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    PRIMARY KEY (series_id, season_number)
);

-- +goose Down
DROP TABLE library_season_monitored;
