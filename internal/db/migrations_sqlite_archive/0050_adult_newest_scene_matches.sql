-- +goose Up
-- Release-level cache for the Adult "newest" drill-down's on-demand Show More
-- (page>1) Prowlarr path. DISTINCT from adult_newest_releases, which cannot
-- serve this role: that table is keyed by (row_type, entity_source, entity_id)
-- — ONE row per matched ENTITY, with its download_url/feed_item_key locked to
-- whichever release first discovered that entity and never overwritten — so it
-- structurally cannot hold one row per matched RELEASE. This table is keyed by
-- download_url (a release's stable identity), one row per confidently-matched
-- Prowlarr release, so a repeated Show More for the same entity reuses a prior
-- match instead of re-fetching + re-matching that release against the box
-- catalog. Stores exactly the enriched display fields needed to reconstruct an
-- AdultDiscoverItem's presentation (the grab-bearing fields
-- ReleaseTitle/DownloadURL/Protocol/SizeBytes always come from the live raw
-- Prowlarr result at request time, never from this cache). genres/performers
-- are JSON-encoded TEXT arrays, same convention as adult_newest_releases.
CREATE TABLE adult_newest_scene_matches (
    download_url     TEXT PRIMARY KEY,
    title            TEXT    NOT NULL DEFAULT '',
    studio           TEXT    NOT NULL DEFAULT '',
    image            TEXT    NOT NULL DEFAULT '',
    date             TEXT    NOT NULL DEFAULT '',
    genres           TEXT    NOT NULL DEFAULT '[]',
    performers       TEXT    NOT NULL DEFAULT '[]',
    duration_seconds INTEGER NOT NULL DEFAULT 0,
    box              TEXT    NOT NULL DEFAULT '',
    matched_at       TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

-- +goose Down
DROP TABLE adult_newest_scene_matches;
