-- +goose Up
-- Claude 2026-10-23: On Demand quality is implied bitrate, not the release title.
-- Reason: remux/2160p tokens are not quality. Skip lossless by Mbps; picker labels Mbps.
-- Troubleshooting: qualities still 2160p → goose < 57.
-- Review if: Play probe replaces TMDB runtime for bitrate.
-- Related files: internal/newznabondemand/{store,shelf,crawl}.go

ALTER TABLE newznab_ondemand
    ADD COLUMN payload_bytes BIGINT NOT NULL DEFAULT 0;
ALTER TABLE newznab_ondemand
    ADD COLUMN bitrate_mbps DOUBLE PRECISION NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE newznab_ondemand DROP COLUMN IF EXISTS bitrate_mbps;
ALTER TABLE newznab_ondemand DROP COLUMN IF EXISTS payload_bytes;
