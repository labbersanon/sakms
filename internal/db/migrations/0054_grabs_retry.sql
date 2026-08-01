-- +goose Up
-- Retry state for the gated auto-grab pipeline. There is NO `requests` table —
-- internal/api/requests.go aggregates Requests live on read ("a grab IS the
-- request"), so retry state belongs on `grabs`, extending it the way migrations
-- 0012/0014/0021/0035 already did.
--
-- download_url_encrypted: nothing in grabs.Grab stored the download URL —
-- dispatchToDownloadClient consumed and discarded it — so a retry had nothing to
-- re-derive from. Encrypted because an indexer/NZB download URL commonly embeds
-- an API key, exactly the reasoning 0043_rss_feeds_encrypt_url.sql applied to
-- feed URLs.
ALTER TABLE grabs ADD COLUMN download_url_encrypted TEXT NOT NULL DEFAULT '';
ALTER TABLE grabs ADD COLUMN retry_after TEXT NOT NULL DEFAULT '';
ALTER TABLE grabs ADD COLUMN retry_count INTEGER NOT NULL DEFAULT 0;
ALTER TABLE grabs ADD COLUMN retry_reason TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE grabs DROP COLUMN retry_reason;
ALTER TABLE grabs DROP COLUMN retry_count;
ALTER TABLE grabs DROP COLUMN retry_after;
ALTER TABLE grabs DROP COLUMN download_url_encrypted;
