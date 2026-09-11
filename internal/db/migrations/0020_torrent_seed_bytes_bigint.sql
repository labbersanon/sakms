-- +goose Up
-- Claude 2026-09-11: torrent seed byte columns must be bigint.
-- Reason: 0019 originally used INTEGER (int4); torrents >2GiB made SaveSeed fail
--   with int4 overflow so ratio/duration never persisted across boots.
-- Troubleshooting: downloadstate SaveSeed "greater than maximum value for int4"
-- Review if: no environment ever applied the INTEGER form of 0019 (ALTER is still safe)

ALTER TABLE torrent_seed_state
    ALTER COLUMN seed_baseline_up TYPE bigint,
    ALTER COLUMN seed_total_bytes TYPE bigint;

-- +goose Down
ALTER TABLE torrent_seed_state
    ALTER COLUMN seed_baseline_up TYPE integer,
    ALTER COLUMN seed_total_bytes TYPE integer;
