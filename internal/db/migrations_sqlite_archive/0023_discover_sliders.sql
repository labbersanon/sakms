-- +goose Up
-- Admin-defined custom Discover sliders (Seerr's CreateSlider equivalent).
-- filter_value is only meaningful for the id/text-based filter types
-- (genre/keyword/studio/network); upcoming/trending/popular ignore it and
-- store '' — enforced in internal/discoversliders, not by the schema, since
-- SQLite has no per-row conditional CHECK against another column's value
-- worth the complexity here.
CREATE TABLE discover_sliders (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    title        TEXT NOT NULL,
    filter_type  TEXT NOT NULL,
    filter_value TEXT NOT NULL DEFAULT '',
    target       TEXT NOT NULL,
    sort_order   INTEGER NOT NULL DEFAULT 0,
    enabled      INTEGER NOT NULL DEFAULT 1,
    created_at   TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at   TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE INDEX idx_discover_sliders_sort_order ON discover_sliders (sort_order);

-- +goose Down
DROP TABLE discover_sliders;
