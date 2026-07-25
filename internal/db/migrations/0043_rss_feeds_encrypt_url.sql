-- +goose Up
-- Encrypt rss_feeds.feed_url at rest. A Newznab/Torznab feed URL commonly
-- embeds an indexer API key, and once feeds drive identification/availability
-- (not just a passive display row), the project's "secrets encrypted at rest"
-- convention applies — same treatment connections/trakt/webhooks already give
-- their own secrets (internal/secrets, AES-256-GCM).
--
-- The plaintext feed_url column is RETAINED (no-silent-delete posture) but
-- stops being authoritative: internal/rssfeeds.Store now writes the ciphertext
-- to feed_url_encrypted and blanks feed_url on every Create/Update. A boot-time
-- backfill (Store.BackfillEncryption) migrates existing plaintext rows; until it
-- reaches a row, the Store's decrypt-on-read path falls back to the plaintext
-- column so a partial/failed backfill self-heals rather than orphaning a feed.
--
-- feed_url stays NOT NULL with no DEFAULT (a table rebuild just to add a column
-- default would be riskier than keeping the Store's explicit feed_url='' insert),
-- so every INSERT must still supply it.
ALTER TABLE rss_feeds ADD COLUMN feed_url_encrypted TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE rss_feeds DROP COLUMN feed_url_encrypted;
