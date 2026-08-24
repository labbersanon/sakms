-- +goose Up
-- Claude 2026-08-24: track which Adult performers/studios the operator wants
--   monitored (auto-grabbed when new matching releases surface via Prowlarr).
-- Reason: Adult Discover feature — periodic Prowlarr search per monitored entity,
--   auto-grab new matches, show Monitored Discover row.
-- Troubleshooting: entity_id is the OPAQUE catalog id (TPDB/StashDB/FansDB), never
--   the display name; monitor_entity_key on grabs is the origin marker for cleanup.
-- Review if: monitoring moves to a separate service or catalog ids change format.

CREATE TABLE adult_monitored_entities (
    id              bigserial PRIMARY KEY,
    kind            text NOT NULL,
    entity_source   text NOT NULL,
    entity_id       text NOT NULL,
    entity_name     text NOT NULL,
    entity_image    text NOT NULL DEFAULT '',
    monitored       integer NOT NULL DEFAULT 0,
    monitored_since text NOT NULL DEFAULT '',
    next_poll_at    text NOT NULL DEFAULT '',
    empty_polls     integer NOT NULL DEFAULT 0,
    last_polled_at  text NOT NULL DEFAULT '',
    created_at      text NOT NULL DEFAULT sakms_now(),
    updated_at      text NOT NULL DEFAULT sakms_now(),
    UNIQUE (kind, entity_source, entity_id)
);

CREATE INDEX idx_adult_monitored_entities_poll ON adult_monitored_entities (monitored, next_poll_at);

-- monitor_entity_key is the origin marker for Adult monitor-originated grabs.
-- Format: kind + U+001F (ASCII unit separator) + entity_source + U+001F + entity_id.
-- Non-empty ONLY for grabs dispatched by the monitor cycle (never operator-clicked).
-- Never cleared after dispatch — provenance outlives the grab.
ALTER TABLE grabs ADD COLUMN monitor_entity_key text NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE grabs DROP COLUMN IF EXISTS monitor_entity_key;
DROP TABLE IF EXISTS adult_monitored_entities;
