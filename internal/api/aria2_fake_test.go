package api

import (
	"github.com/labbersanon/sakms/internal/downloader"
)

// newTestDownloader builds a Manager in test mode. AddTorrent returns gid;
// pre-seeded state is visible immediately to List/FindByGID/Subscribe.
func newTestDownloader(gid, stagingDir string) *downloader.Manager {
	dl := downloader.NewForTesting(stagingDir)
	dl.SetTestNextGID(gid)
	return dl
}

// seedComplete marks gid complete with dir as its staging directory and no
// individual files — so checkImportHandler's contentPath falls back to dir
// (the whole directory is moved, matching the torrent-subfolder behavior).
func seedComplete(dl *downloader.Manager, gid, dir string) {
	dl.SeedState(downloader.Download{GID: gid, Status: "complete", Dir: dir})
}

// seedActive marks gid as an active (still-downloading) item at 50% progress.
func seedActive(dl *downloader.Manager, gid string) {
	dl.SeedState(downloader.Download{
		GID: gid, Status: "active",
		TotalLength: 100, CompletedLength: 50,
		DownloadSpeed: 1024, Connections: 4,
	})
}
