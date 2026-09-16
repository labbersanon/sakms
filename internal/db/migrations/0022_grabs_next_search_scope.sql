-- +goose Up
-- Claude 2026-09-16: escalation marker for Usenet-failure → torrent-retry path.
-- Reason: a 430/incomplete Usenet download must trigger a torrent search on the
--   NEXT attempt, not another Usenet search. Encoding this as a queryable column
--   (not an inference from retry_reason) prevents the class of misclassification
--   bugs documented in airdatemonitor.go's and usenetretry.go's history.
-- One-shot semantics: the marker is consumed (cleared to '') by whichever path
--   performs the next attempt, regardless of outcome. So a failed torrent attempt
--   returns the row to the normal Usenet-first order rather than pinning to torrent.
-- Troubleshooting: escalation not firing → check retry_after (must be <= now) and
--   next_search_scope (must be 'torrent'); verify nothing clears it before the
--   retry cycle or drain picks it up.
-- Review if: the escalation policy changes (e.g. pin to torrent after N consecutive
--   Usenet failures, or support additional scope values).

ALTER TABLE grabs ADD COLUMN next_search_scope TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE grabs DROP COLUMN next_search_scope;
