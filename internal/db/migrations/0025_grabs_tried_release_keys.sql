-- +goose Up
-- Claude 2026-09-17: tried_release_keys column for alternate-NZB exclusion.
-- Reason: when a release fails due to bad content (PAR2/unpack/no-video), the
--   grab is parked due-now and the NEXT attempt should exclude releases already
--   tried. tried_release_keys stores hashed (sha256, 16 hex chars) URL + title
--   keys, newline-separated, so no plaintext credential leaks. Attempt count
--   = number of "u:" entries. Cleared by SetPendingRetry/SetPendingRetryWithScope
--   (days-ladder fallback); preserved by Relaunch (alternate dispatch in flight).
-- Review if: the column grows beyond 6 entries per row (cap is MaxAlternateReleaseAttempts=3,
--   so at most 6 entries; TEXT is plenty).
ALTER TABLE grabs ADD COLUMN tried_release_keys TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE grabs DROP COLUMN tried_release_keys;
