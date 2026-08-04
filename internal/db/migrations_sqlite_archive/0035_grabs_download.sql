-- +goose Up
-- Unified downloader (aria2c subprocess): a grab now also tracks the aria2
-- GID it was handed to, the last-observed aria2 download status, and the
-- staging path aria2 is writing it to. download_gid is what the Manager's
-- onComplete callback looks a grab up by when a download finishes, to run
-- the auto-import. These are additive to the existing client_ref/status
-- fields (which stay for the legacy per-client polling path), not a
-- replacement — the aria2 GID is a different identifier from qBittorrent's
-- hash / NZBGet's id that client_ref held.
ALTER TABLE grabs ADD COLUMN download_gid TEXT NOT NULL DEFAULT '';
ALTER TABLE grabs ADD COLUMN download_status TEXT NOT NULL DEFAULT '';
ALTER TABLE grabs ADD COLUMN download_staging_path TEXT NOT NULL DEFAULT '';

CREATE INDEX idx_grabs_download_gid ON grabs (download_gid);

-- +goose Down
DROP INDEX idx_grabs_download_gid;
ALTER TABLE grabs DROP COLUMN download_staging_path;
ALTER TABLE grabs DROP COLUMN download_status;
ALTER TABLE grabs DROP COLUMN download_gid;
