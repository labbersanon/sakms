-- +goose Up
-- Claude 2026-08-10: delete legacy qbittorrent/nzbget rows from `connections`
-- Reason: deep-interview-download-settings-consolidation — the unified native downloader
--   (internal/downloader + internal/usenet) replaced qBittorrent and NZBGet as SAK's download
--   engine on 2026-07-18, and both client packages were deleted on 2026-08-10. Nothing reads
--   these rows, and upsertConnectionHandler now refuses to write new ones
--   (removedConnectionServices, internal/api/handler.go), so any survivor is an inert
--   encrypted-credential blob an operator still believes is configured.
-- Troubleshooting: targets `connections` (service TEXT PRIMARY KEY), NOT `service_connections` —
--   that table's kind column has only ever accepted 'usenet'/'player'
--   (internal/serviceconn.ErrInvalidKind enforces it on every write), so it has never held a
--   qbittorrent or nzbget row. On a fresh install this deletes zero rows and is still correct;
--   the target is an install carried forward from before 2026-07-18.
-- Review if: an external download-client integration is ever reintroduced, which would need its
--   own service key and would have to be removed from removedConnectionServices first.
DELETE FROM connections WHERE service IN ('qbittorrent', 'nzbget');

-- +goose Down
-- Irreversible by design: the deleted rows carried encrypted secrets that cannot be
-- reconstructed, and no code path is left that would read them. A no-op Down keeps `goose down`
-- working rather than failing the whole rollback of an unrelated later migration.
SELECT 1;
