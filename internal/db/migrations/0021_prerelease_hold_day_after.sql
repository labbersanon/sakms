-- +goose Up
-- Claude 2026-09-16: advance still-held movie pre-release hold_until by one day.
-- Reason: day-after grab timing — new requests now write hold_until = release_date+1, but
--   rows created before this change still carry hold_until = release_date midnight UTC.
--   This statement corrects them so DueForRelease promotes on the day-after, not the
--   release day itself.
-- Troubleshooting: existing pre-release holds promoted on the release day instead of the
--   day after.
-- Review if: day-after timing decision is reversed.
--
-- WARNING: this statement is NOT idempotent. hold_until is the only record of the release
-- date, so re-running it shifts every matching hold another day. Never re-run by hand,
-- never re-number this migration.
--
-- The date floor (>= CURRENT_DATE) protects rows whose hold_until is already in the past:
-- a hold at yesterday-or-earlier has already passed its day-after threshold anyway.
-- The retry_after = '' guard matches DueForRelease's own guard and protects any row an
-- operator has explicitly promoted (PromoteToFront writes a real retry_after).

UPDATE grabs
SET hold_until = to_char(
                     (substring(hold_until FROM 1 FOR 10))::date + 1,
                     'YYYY-MM-DD'
                 ) || 'T00:00:00.000Z',
    updated_at = sakms_now()
WHERE mode        = 'movies'
  AND status      = 'pending_retry'
  AND download_gid = ''
  AND retry_after  = ''
  AND hold_until   != ''
  AND substring(hold_until FROM 1 FOR 10) >= to_char(CURRENT_DATE, 'YYYY-MM-DD');

-- +goose Down
-- Approximate inverse: shifts back by one day, under the same promotable guards but
-- WITHOUT the date floor (the floor's meaning depends on when Down runs, so it is
-- omitted and documented as approximate). On a fresh install this is a no-op.

UPDATE grabs
SET hold_until = to_char(
                     (substring(hold_until FROM 1 FOR 10))::date - 1,
                     'YYYY-MM-DD'
                 ) || 'T00:00:00.000Z',
    updated_at = sakms_now()
WHERE mode        = 'movies'
  AND status      = 'pending_retry'
  AND download_gid = ''
  AND retry_after  = ''
  AND hold_until   != '';
