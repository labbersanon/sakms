-- +goose Up
-- Admin-defined raw RSS 2.0 feed rows (NZBGeek saved-search style feeds) —
-- a per-row RSS feed URL fetched and parsed server-side (internal/rssfeed),
-- rendered as a one-click-grabbable Discover row. protocol is admin-set at
-- creation, not sniffed from the XML (enclosure MIME types are inconsistent
-- across indexers). Structurally mirrors 0023_discover_sliders.sql.
CREATE TABLE rss_feeds (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    title      TEXT NOT NULL,
    feed_url   TEXT NOT NULL,
    target     TEXT NOT NULL,
    protocol   TEXT NOT NULL,
    sort_order INTEGER NOT NULL DEFAULT 0,
    enabled    INTEGER NOT NULL DEFAULT 1,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE INDEX idx_rss_feeds_sort_order ON rss_feeds (sort_order);

-- +goose Down
DROP TABLE rss_feeds;
