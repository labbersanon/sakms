package api

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"path/filepath"
	"strconv"

	"github.com/labbersanon/sakms/internal/connections"
	"github.com/labbersanon/sakms/internal/dedup"
	"github.com/labbersanon/sakms/internal/downloader"
	"github.com/labbersanon/sakms/internal/grabs"
	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/rename"
	"github.com/labbersanon/sakms/internal/serviceconn"
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
//
// videoHasher is Adult's StashDB-compatible phash (may be nil in tests that
// never import Adult); required for OrganizeImportedAdult on Adult grabs.
func DownloadCompleteImporter(httpClient *http.Client, connStore *connections.Store, scStore *serviceconn.Store, settingsStore *settings.Store, grabsStore *grabs.Store, libStore *library.Store, prober dedup.Prober, dl *downloader.Manager, videoHasher rename.PHasher) func(gid string, files []string) {
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

		// With seeding on, files are the per-gid IMPORT COPY under
		// <staging>/.import/<gid>/ — the originals stay put and keep seeding.
		// downloadContentPath decides "single file" vs "wrapping folder" by
		// comparing files[0]'s parent against the staging root it is given, so
		// it must be given the import root, not the global staging dir, or a
		// single-file torrent's copy would relocate its whole directory.
		// ImportRoot falls back to StagingDir() whenever there is no copy, so
		// a seeding-off install takes exactly today's path.
		importRoot := dl.ImportRoot(gid)
		contentPath := downloadContentPath(files, importRoot, importRoot)
		if contentPath == "" {
			log.Printf("downloader import: grab %d (gid %s) completed but has no content path", g.ID, gid)
			return
		}

		sess, sessErr := mode.Build(ctx, connStore, scStore, settingsStore, httpClient, dl, g.Mode)
		if sessErr != nil {
			log.Printf("downloader import: grab %d building session: %v", g.ID, sessErr)
		}

		changes, err := importGrabContent(ctx, libStore, g, contentPath, string(autoGrabTier(ctx, settingsStore, g.Mode)), settingsStore, sess, videoHasher, prober)
		if err != nil {
			log.Printf("downloader import: grab %d (gid %s): %v", g.ID, gid, err)
			return
		}

		if sess != nil {
			postGrabRuntimeReview(ctx, prober, grabsStore, sess, g, changes)
			sess.NotifyPlayers(ctx, changes)
		}

		if err := grabsStore.SetDownloadStatus(ctx, g.ID, "complete", contentPath); err != nil {
			log.Printf("downloader import: grab %d recording download status: %v", g.ID, err)
		}
		if err := grabsStore.UpdateStatus(ctx, g.ID, grabs.Imported); err != nil {
			log.Printf("downloader import: grab %d marking imported: %v", g.ID, err)
			return
		}
		// The import consumed the copy; Relocate moved the content out but
		// left the .import/<gid>/ tree behind. Reclaim it now rather than
		// waiting for the next restart's sweep. Only on the fully-successful
		// path: while the grab is not yet Imported, the copy is still what a
		// manual check-import must consume.
		dl.ClearImportCopy(gid)
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
func UsenetCompleteImporter(httpClient *http.Client, connStore *connections.Store, scStore *serviceconn.Store, settingsStore *settings.Store, grabsStore *grabs.Store, libStore *library.Store, prober dedup.Prober, dl *downloader.Manager, nzb *usenet.Manager, videoHasher rename.PHasher) func(gid string, files []string) {
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

		sess, sessErr := mode.Build(ctx, connStore, scStore, settingsStore, httpClient, dl, g.Mode)
		if sessErr != nil {
			log.Printf("usenet import: grab %d building session: %v", g.ID, sessErr)
		}

		changes, err := importGrabContent(ctx, libStore, g, contentPath, string(autoGrabTier(ctx, settingsStore, g.Mode)), settingsStore, sess, videoHasher, prober)
		if err != nil {
			log.Printf("usenet import: grab %d (gid %s): %v", g.ID, gid, err)
			return
		}

		if sess != nil {
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
// download at contentPath into g's target root folder using the mode's
// library naming (RelocateMovie / RelocateEpisode / OrganizeImportedAdult)
// and records it in SAK's own library the same way Rename's Apply does.
// Grab approval is operator consent — no Rename proposal row is required.
//
// Extracted from checkImportHandler verbatim when the unified downloader
// added a second, non-HTTP caller (the background completion callback), which
// is exactly the "second real caller justifies the extraction" bar this
// project's conventions set for pulling logic out of a handler.
//
// Claude 2026-08-11: auto rename+move on grab import (all modes).
// Reason: finished grabs were left as raw torrent/NZB basenames until a
//   manual Rename Apply; operator expects download → library name in one step.
// Troubleshooting: Adult Step Sis landed as "[Familylust.com] …mp4" on Adult-NAS.
// Review if: import gains a settings toggle to keep proposal-gated Apply only.
//
// tier is the operator's configured quality tier for g.Mode, recorded on every
// library row this import writes. sess/videoHasher may be nil (session build
// failed, or Adult hasher unset in a Movies-only test) — Movies/Series still
// organize via naming presets; Adult then falls back to basename Relocate and
// leaves the file for a later Rename scan (import still succeeds).
func importGrabContent(ctx context.Context, libStore *library.Store, g *grabs.Grab, contentPath, tier string, settingsStore *settings.Store, sess *mode.Session, videoHasher rename.PHasher, prober dedup.Prober) ([]mode.PathChange, error) {
	switch g.Mode {
	case mode.Movies:
		return importGrabMovies(ctx, libStore, g, contentPath, tier, settingsStore, sess, prober)
	case mode.Series:
		return importGrabSeries(ctx, libStore, g, contentPath, tier, settingsStore, sess, prober)
	case mode.Adult:
		return importGrabAdult(ctx, libStore, g, contentPath, tier, sess, videoHasher, prober)
	default:
		return nil, fmt.Errorf("unknown mode %q", g.Mode)
	}
}

func importGrabMovies(ctx context.Context, libStore *library.Store, g *grabs.Grab, contentPath, tier string, settingsStore *settings.Store, sess *mode.Session, prober dedup.Prober) ([]mode.PathChange, error) {
	_ = prober
	preset, err := resolveNamingPreset(ctx, settingsStore, mode.Movies)
	if err != nil {
		return nil, fmt.Errorf("resolving movies naming preset: %w", err)
	}
	videoPath, err := library.ResolveVideoFile(contentPath)
	if err != nil {
		return nil, fmt.Errorf("resolving the video file failed: %w", err)
	}
	year := movieYearFromTMDB(ctx, sess, g.TMDBID)
	destPath, err := rename.RelocateMovie(videoPath, g.RootFolderPath, g.Title, year, g.TMDBID, preset)
	if err != nil {
		return nil, fmt.Errorf("download completed but import failed: %w", err)
	}
	var changes []mode.PathChange
	if destPath != videoPath {
		changes = []mode.PathChange{{Path: videoPath, Kind: mode.Deleted}, {Path: destPath, Kind: mode.Created}}
	} else {
		changes = []mode.PathChange{{Path: destPath, Kind: mode.Created}}
	}
	if _, err := libStore.Upsert(ctx, library.Item{
		Mode: mode.Movies, TMDBID: g.TMDBID, Title: g.Title, Year: year,
		FilePath: destPath, RootFolderPath: g.RootFolderPath,
		Size: library.FileSize(destPath), QualityTier: tier,
	}); err != nil {
		return changes, fmt.Errorf("file relocated but recording it in the library failed: %w", err)
	}
	return changes, nil
}

func importGrabSeries(ctx context.Context, libStore *library.Store, g *grabs.Grab, contentPath, tier string, settingsStore *settings.Store, sess *mode.Session, prober dedup.Prober) ([]mode.PathChange, error) {
	_ = prober
	preset, err := resolveNamingPreset(ctx, settingsStore, mode.Series)
	if err != nil {
		return nil, fmt.Errorf("resolving series naming preset: %w", err)
	}
	videoPaths, err := library.ResolveEpisodeVideoFiles(contentPath)
	if err != nil {
		return nil, fmt.Errorf("resolving the video file(s) failed: %w", err)
	}
	seriesYear := seriesYearFromTMDB(ctx, sess, g.TMDBID)
	series, err := libStore.UpsertSeries(ctx, library.Series{
		TMDBID: g.TMDBID, Title: g.Title, Year: seriesYear, RootFolderPath: g.RootFolderPath,
	})
	if err != nil {
		return nil, fmt.Errorf("recording the series failed: %w", err)
	}
	var changes []mode.PathChange
	for _, videoPath := range videoPaths {
		season, episodes, ok := library.ParseEpisodeNumbers(filepath.Base(videoPath))
		if !ok {
			if len(videoPaths) != 1 || !g.SeasonSpecified {
				continue
			}
			season, episodes = g.SeasonNumber, []int{g.EpisodeNumber}
		}
		destPath, err := rename.RelocateEpisodeRange(videoPath, g.RootFolderPath, g.Title, seriesYear, g.TMDBID, season, episodes, "", preset)
		if err != nil {
			return changes, fmt.Errorf("download completed but import failed: %w", err)
		}
		if destPath != videoPath {
			changes = append(changes, mode.PathChange{Path: videoPath, Kind: mode.Deleted}, mode.PathChange{Path: destPath, Kind: mode.Created})
		} else {
			changes = append(changes, mode.PathChange{Path: destPath, Kind: mode.Created})
		}
		videoSize := library.FileSize(destPath)
		for _, episode := range episodes {
			if _, err := libStore.UpsertEpisode(ctx, library.Episode{
				SeriesID: series.ID, SeasonNumber: season, EpisodeNumber: episode, FilePath: destPath,
				Size: videoSize, QualityTier: tier,
			}); err != nil {
				return changes, fmt.Errorf("file relocated but recording episode s%de%d failed: %w", season, episode, err)
			}
		}
	}
	if len(changes) == 0 {
		return nil, fmt.Errorf("download completed but no episode files could be identified for import")
	}
	return changes, nil
}

func importGrabAdult(ctx context.Context, libStore *library.Store, g *grabs.Grab, contentPath, tier string, sess *mode.Session, videoHasher rename.PHasher, prober dedup.Prober) ([]mode.PathChange, error) {
	// Claude 2026-08-11: basename Relocate into Adult root, then organize.
	// Reason: OrganizeImportedAdult expects a path already under the library
	//   root (same as Rename Apply); staging→root still needs the first move.
	// Troubleshooting: identifying from /data/downloads left files on the wrong volume.
	// Review if: OrganizeImportedAdult accepts a staging source and relocates in one step.
	movedPath, err := rename.Relocate(contentPath, g.RootFolderPath)
	if err != nil {
		return nil, fmt.Errorf("download completed but import failed: %w", err)
	}
	videoPath, resolveErr := library.ResolveVideoFile(movedPath)
	if resolveErr != nil {
		// Directory tree or non-video — notify the relocated path; Rename scan later.
		return []mode.PathChange{{Path: movedPath, Kind: mode.Created}}, nil
	}
	finalPath, orgChanges, orgErr := rename.OrganizeImportedAdult(ctx, sess, libStore, videoHasher, prober, videoPath, g.RootFolderPath, tier)
	if orgErr != nil {
		// Identify/relocate organize failure must not fail the import — file is
		// already in the Adult root for a later Rename scan.
		log.Printf("import: adult organize for grab %d: %v (left at %q)", g.ID, orgErr, videoPath)
		return []mode.PathChange{{Path: videoPath, Kind: mode.Created}}, nil
	}
	if finalPath != "" {
		if len(orgChanges) > 0 {
			return orgChanges, nil
		}
		return []mode.PathChange{{Path: finalPath, Kind: mode.Created}}, nil
	}
	return []mode.PathChange{{Path: videoPath, Kind: mode.Created}}, nil
}

// movieYearFromTMDB returns the release year for tmdbID when TMDB is available;
// 0 otherwise (naming omits "(year)" — still a correct RelocateMovie destination).
func movieYearFromTMDB(ctx context.Context, sess *mode.Session, tmdbID int) int {
	if sess == nil || sess.TMDB == nil || tmdbID <= 0 {
		return 0
	}
	d, err := sess.TMDB.MovieDetails(ctx, tmdbID)
	if err != nil {
		return 0
	}
	return yearFromDate(d.ReleaseDate)
}

func seriesYearFromTMDB(ctx context.Context, sess *mode.Session, tmdbID int) int {
	if sess == nil || sess.TMDB == nil || tmdbID <= 0 {
		return 0
	}
	d, err := sess.TMDB.TVDetails(ctx, tmdbID)
	if err != nil {
		return 0
	}
	// TVDetails does not expose first_air_date; season 1's air_date is the
	// usual show-year stand-in for RelocateEpisode folder naming.
	for _, s := range d.Seasons {
		if s.SeasonNumber == 1 {
			if y := yearFromDate(s.AirDate); y != 0 {
				return y
			}
		}
	}
	for _, s := range d.Seasons {
		if y := yearFromDate(s.AirDate); y != 0 {
			return y
		}
	}
	return 0
}

func yearFromDate(date string) int {
	if len(date) < 4 {
		return 0
	}
	y, err := strconv.Atoi(date[:4])
	if err != nil {
		return 0
	}
	return y
}
