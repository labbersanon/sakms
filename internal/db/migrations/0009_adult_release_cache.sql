-- +goose Up
-- Claude 2026-08-11: raw Prowlarr release cache for Adult, with protocol TTL.
-- Reason: replaces "search twice" with "search once, persist, reuse" — see
-- .omc/plans/autopilot-impl-adult-release-persistence.md and
-- .omc/specs/deep-interview-adult-release-persistence.md.
-- Troubleshooting: a DetailPopup that still hits Prowlarr on re-open means either
-- zero rows linked to its scene_key, or every linked row is past its own protocol
-- window (usenet 2y / torrent 1w) — check persisted_at + protocol, not the handler.
-- Review if: a non-Adult mode ever needs release persistence (this is Adult-only by
-- construction — nothing writes it outside internal/api's Adult paths).

CREATE TABLE adult_release_cache (
    download_url  text PRIMARY KEY,
    guid          text   NOT NULL DEFAULT '',
    title         text   NOT NULL DEFAULT '',
    protocol      text   NOT NULL DEFAULT '',
    indexer       text   NOT NULL DEFAULT '',
    categories    text   NOT NULL DEFAULT '[]',
    indexer_flags text   NOT NULL DEFAULT '[]',
    size_bytes    bigint NOT NULL DEFAULT 0,
    seeders       bigint NOT NULL DEFAULT 0,
    publish_date  text   NOT NULL DEFAULT '',
    persisted_at  text   NOT NULL DEFAULT sakms_now(),
    last_query    text   NOT NULL DEFAULT ''
);

CREATE INDEX idx_adult_release_cache_protocol_persisted
    ON adult_release_cache (protocol, persisted_at);

CREATE TABLE adult_release_scene_links (
    scene_key    text NOT NULL,
    download_url text NOT NULL REFERENCES adult_release_cache(download_url) ON DELETE CASCADE,
    linked_at    text NOT NULL DEFAULT sakms_now(),
    PRIMARY KEY (scene_key, download_url)
);

CREATE INDEX idx_adult_release_scene_links_url
    ON adult_release_scene_links (download_url);

-- +goose Down
DROP TABLE IF EXISTS adult_release_scene_links CASCADE;
DROP TABLE IF EXISTS adult_release_cache CASCADE;
