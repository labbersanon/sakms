-- +goose Up
-- Claude 2026-09-11: durable mirrors for usenet segment resume + torrent seed baselines.
-- Reason: Phase-2 ARR-parity — staging sidecar is SoT for segments; DB mirror is
--   for UI/debug. Seed ratio/duration windows were in-memory only and reset on
--   every process restart, so seed limits never accumulated across boots.
-- Troubleshooting: usenet_resume_state / torrent_seed_state rows keyed by download_gid;
--   cleared on import (usenet) and seed-stop/cancel (torrent).
-- Review if: resume state is folded into grabs or seed state gains per-file paths.

CREATE TABLE IF NOT EXISTS usenet_resume_state (
    download_gid TEXT PRIMARY KEY NOT NULL,
    state_json   TEXT NOT NULL DEFAULT '',
    updated_at   TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS torrent_seed_state (
    download_gid      TEXT PRIMARY KEY NOT NULL,
    seed_started_at   TEXT NOT NULL DEFAULT '',
    seed_baseline_up  INTEGER NOT NULL DEFAULT 0,
    seed_total_bytes  INTEGER NOT NULL DEFAULT 0,
    updated_at        TEXT NOT NULL DEFAULT ''
);

-- +goose Down
DROP TABLE IF EXISTS torrent_seed_state;
DROP TABLE IF EXISTS usenet_resume_state;
