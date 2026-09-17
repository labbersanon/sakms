-- +goose Up
-- Claude 2026-09-17: transport_retry_count column for short-backoff park ladder.
-- Reason: a transport park (dropped socket) must not advance the multi-day
--   retry_count ladder — that counter is for genuine "no match found" attempts.
--   The short ladder (2m/5m/15m/30m, max 4 tries) needs its own resettable
--   counter. Separate because any non-transport park (SetPendingRetry,
--   SetPendingRetryWithScope, Relaunch) resets it to 0, ending the episode.
-- Review if: the ladder shape gains a settings-UI control.
ALTER TABLE grabs ADD COLUMN transport_retry_count INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE grabs DROP COLUMN transport_retry_count;
