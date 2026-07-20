package api

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"path/filepath"

	"github.com/labbersanon/sakms/internal/connections"
	"github.com/labbersanon/sakms/internal/dedup"
	"github.com/labbersanon/sakms/internal/downloader"
	"github.com/labbersanon/sakms/internal/grabs"
	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/rename"
	"github.com/labbersanon/sakms/internal/settings"
	"github.com/labbersanon/sakms/internal/usenet"
)

// DownloadCompleteImporter returns the downloader Manager's onComplete
// callback: when the torrent engine finishes a download (identified by GID),
// it finds the owning grab, runs the shared import core (relocate + library
// upsert), notifies the mode's downstream player, runs the advisory post-grab
// runtime review, and flips the grab to Imported. Built in cmd/sakms and
// handed to downloader.Manager.SetOnComplete — it's the automatic counterpart
// to the manual check-import handler, so a grab typically imports itself the
// instant the torrent engine completes.
//
// Every failure path is log-only: this runs in the Manager's background
// goroutine with no HTTP response to write, and a completed download that
// can't be imported must never crash the poll loop — it just stays
// un-flipped (the operator can retry via check-import). files is aria2's
// reported file list for the GID; the first file (or, empty, the grab's
// staging dir) is the content path handed to the import core.
func DownloadCompleteImporter(httpClient *http.Client, connStore *connections.Store, settingsStore *settings.Store, grabsStore *grabs.Store, libStore *library.Store, prober dedup.Prober, dl *downloader.Manager) func(gid string, files []string) {
	return func(gid string, files []string) {
		ctx := context.Background()
		g, err := grabsStore.GetByDownloadGID(ctx, gid)
		if err != nil {
			if !errors.Is(err, grabs.ErrNotFound) {
				log.Printf("downloader import: looking up grab for gid %s: %v", gid, err)
			}
			return // a download SAK didn't initiate, or already gone
		}
		if g.Status == grabs.Imported {
			return // already imported (e.g. a manual check-import beat us)
		}

		contentPath := downloadContentPath(files, dl.StagingDir(), dl.StagingDir())
		if contentPath == "" {
			log.Printf("downloader import: grab %d (gid %s) completed but has no content path", g.ID, gid)
			return
		}

		changes, err := importGrabContent(ctx, libStore, g, contentPath)
		if err != nil {
			log.Printf("downloader import: grab %d (gid %s): %v", g.ID, gid, err)
			return
		}

		sess, err := mode.Build(ctx, connStore, settingsStore, httpClient, dl, g.Mode)
		if err != nil {
			log.Printf("downloader import: grab %d building session: %v", g.ID, err)
		} else {
			postGrabRuntimeReview(ctx, prober, grabsStore, sess, g, changes)
			sess.NotifyPlayers(ctx, changes)
		}

		if err := grabsStore.SetDownloadStatus(ctx, g.ID, "complete", contentPath); err != nil {
			log.Printf("downloader import: grab %d recording download status: %v", g.ID, err)
		}
		if err := grabsStore.UpdateStatus(ctx, g.ID, grabs.Imported); err != nil {
			log.Printf("downloader import: grab %d marking imported: %v", g.ID, err)
		}
	}
}

// downloadContentPath derives the on-disk path importGrabContent should
// relocate for a completed aria2 download, from aria2's reported staging dir
// and per-file paths. aria2 always populates files for a real download, so
// this is the production path (not a fallback):
//
//   - A multi-file download (season pack, or a movie folder with sample/subs)
//     stages every file under one per-torrent subfolder of stagingDir. We
//     relocate that whole subfolder — moving only files[0] would orphan the
//     rest — so we return the parent directory of files[0] when it's a real
//     subfolder (not stagingDir itself). importGrabContent already walks a
//     directory tree (ResolveVideoFile / ResolveEpisodeVideoFiles), so this
//     records every episode/the movie correctly.
//   - A single file dropped directly in stagingDir (no wrapping folder) has
//     files[0]'s parent == stagingDir, so we relocate just that file.
//   - No files reported (aria2 hasn't populated them yet, or a magnet still
//     resolving) falls back to the reported dir.
func downloadContentPath(files []string, dir, stagingDir string) string {
	if len(files) == 0 || files[0] == "" {
		return dir
	}
	parent := filepath.Dir(files[0])
	// A file staged directly under stagingDir has no per-torrent folder to
	// move — relocate the file itself. Otherwise relocate its wrapping folder.
	if clean(parent) == clean(stagingDir) {
		return files[0]
	}
	return parent
}

// clean normalizes a path for the stagingDir comparison (trailing-slash /
// "." differences shouldn't defeat the equality check).
func clean(p string) string {
	if p == "" {
		return ""
	}
	return filepath.Clean(p)
}

// UsenetCompleteImporter returns the usenet Manager's onComplete callback,
// parallel to DownloadCompleteImporter. Uses nzb.StagingDir() as the staging
// root. dl is the torrent manager (may be nil) — passed only so mode.Build
// can construct a session for player notification.
func UsenetCompleteImporter(httpClient *http.Client, connStore *connections.Store, settingsStore *settings.Store, grabsStore *grabs.Store, libStore *library.Store, prober dedup.Prober, dl *downloader.Manager, nzb *usenet.Manager) func(gid string, files []string) {
	return func(gid string, files []string) {
		ctx := context.Background()
		g, err := grabsStore.GetByDownloadGID(ctx, gid)
		if err != nil {
			if !errors.Is(err, grabs.ErrNotFound) {
				log.Printf("usenet import: looking up grab for gid %s: %v", gid, err)
			}
			return
		}
		if g.Status == grabs.Imported {
			return
		}

		contentPath := downloadContentPath(files, nzb.StagingDir(), nzb.StagingDir())
		if contentPath == "" {
			log.Printf("usenet import: grab %d (gid %s) completed but has no content path", g.ID, gid)
			return
		}

		changes, err := importGrabContent(ctx, libStore, g, contentPath)
		if err != nil {
			log.Printf("usenet import: grab %d (gid %s): %v", g.ID, gid, err)
			return
		}

		sess, err := mode.Build(ctx, connStore, settingsStore, httpClient, dl, g.Mode)
		if err != nil {
			log.Printf("usenet import: grab %d building session: %v", g.ID, err)
		} else {
			postGrabRuntimeReview(ctx, prober, grabsStore, sess, g, changes)
			sess.NotifyPlayers(ctx, changes)
		}

		if err := grabsStore.SetDownloadStatus(ctx, g.ID, "complete", contentPath); err != nil {
			log.Printf("usenet import: grab %d recording download status: %v", g.ID, err)
		}
		if err := grabsStore.UpdateStatus(ctx, g.ID, grabs.Imported); err != nil {
			log.Printf("usenet import: grab %d marking imported: %v", g.ID, err)
		}
	}
}

// importGrabContent is the shared import core: it relocates a completed
// download at contentPath into g's target root folder (reusing
// internal/rename.Relocate) and records it in SAK's own library the same way
// Rename's Apply does for a brand-new orphan — Movies as an Item, Series as a
// season/episode-aware set of Episode rows, Adult left untracked for the next
// Rename scan to identify (see the mode.Adult branch's rationale). It returns
// the exact PathChanges the import created (for the caller to hand to
// NotifyPlayers), or an error describing which step failed — it writes no HTTP
// response, so both the manual check-import handler and the downloader's
// onComplete callback can call it.
//
// Extracted from checkImportHandler verbatim when the unified downloader
// added a second, non-HTTP caller (the background completion callback), which
// is exactly the "second real caller justifies the extraction" bar this
// project's conventions set for pulling logic out of a handler.
func importGrabContent(ctx context.Context, libStore *library.Store, g *grabs.Grab, contentPath string) ([]mode.PathChange, error) {
	movedPath, err := rename.Relocate(contentPath, g.RootFolderPath)
	if err != nil {
		return nil, fmt.Errorf("download completed but import failed: %w", err)
	}

	// changes accumulates the exact file(s) this import created. movedPath can
	// be a wrapping directory (Relocate moves contentPath's whole tree), so
	// Movies/Series notify with the resolved video file path(s) — the same
	// "actual path, not the directory" discipline as rename.go — while Adult
	// notifies with movedPath directly (the scene is left untracked for the
	// next Rename scan, and Stash's RescanPaths handles a directory tree fine).
	switch g.Mode {
	case mode.Movies:
		videoPath, err := library.ResolveVideoFile(movedPath)
		if err != nil {
			return nil, fmt.Errorf("file relocated but resolving the video file failed: %w", err)
		}
		if _, err := libStore.Upsert(ctx, library.Item{
			Mode: mode.Movies, TMDBID: g.TMDBID, Title: g.Title,
			FilePath: videoPath, RootFolderPath: g.RootFolderPath,
		}); err != nil {
			return nil, fmt.Errorf("file relocated but recording it in the library failed: %w", err)
		}
		return []mode.PathChange{{Path: videoPath, Kind: mode.Created}}, nil

	case mode.Series:
		videoPaths, err := library.ResolveEpisodeVideoFiles(movedPath)
		if err != nil {
			return nil, fmt.Errorf("file relocated but resolving the video file(s) failed: %w", err)
		}
		series, err := libStore.UpsertSeries(ctx, library.Series{
			TMDBID: g.TMDBID, Title: g.Title, RootFolderPath: g.RootFolderPath,
		})
		if err != nil {
			return nil, fmt.Errorf("file relocated but recording the series failed: %w", err)
		}
		var changes []mode.PathChange
		for _, videoPath := range videoPaths {
			season, episodes, ok := library.ParseEpisodeNumbers(filepath.Base(videoPath))
			if !ok {
				// A season-pack grab's own request already recorded which season
				// it targeted; a single-episode grab whose relocated file name
				// didn't carry its own SxxExx token falls back to what was
				// requested — only sound when there's exactly one resolved file
				// and a season was actually specified at grab time (SeasonNumber
				// alone can't tell "Season 0/Specials" from "no season picked").
				if len(videoPaths) != 1 || !g.SeasonSpecified {
					continue
				}
				season, episodes = g.SeasonNumber, []int{g.EpisodeNumber}
			}
			// Logical episode-splitting: a bundled multi-episode filename
			// (e.g. "S01E01-E02") relocates as ONE file but must record an
			// Episode row for EVERY number it contains. One Created PathChange
			// per physical file still, not per episode row.
			for _, episode := range episodes {
				if _, err := libStore.UpsertEpisode(ctx, library.Episode{
					SeriesID: series.ID, SeasonNumber: season, EpisodeNumber: episode, FilePath: videoPath,
				}); err != nil {
					return nil, fmt.Errorf("file relocated but recording episode s%de%d failed: %w", season, episode, err)
				}
			}
			changes = append(changes, mode.PathChange{Path: videoPath, Kind: mode.Created})
		}
		return changes, nil

	case mode.Adult:
		// Adult owns its own library (Whisparr eliminated, Stage 4), but an
		// Adult grab carries NO stable scene identity at grab time (grabRequest
		// has no box/scene_id, TMDBID is always 0 for Adult) — library.Scene is
		// keyed on (box, scene_id), so there is nothing to UpsertScene on yet.
		// Recording an empty-key scene would be actively harmful (every grab
		// collides on the ("","") row, and a recorded scene is masked from the
		// next Rename scan that's meant to identify it). So relocate into the
		// Adult root and stop — the next Adult Rename scan discovers, identifies,
		// and UpsertScenes it with a real (box, scene_id). Notify with movedPath
		// directly (Stash's RescanPaths handles a directory tree fine).
		return []mode.PathChange{{Path: movedPath, Kind: mode.Created}}, nil

	default:
		return nil, fmt.Errorf("unknown mode %q", g.Mode)
	}
}
