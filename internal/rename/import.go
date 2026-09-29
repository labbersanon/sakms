package rename

// Claude 2026-09-28: Organize manual import scan — identify then MOVE.
// Reason: SAK is the library manager; files picked under a browsable source
//   must land in the mode library root (Kids when classify says so), not
//   stay in the dump folder. Reuses proposeOneLibrary / proposeOneEpisodeLibrary
//   so identity matches Rename. Anthology / catalog-in-place stays Rename-only.
// Troubleshooting: dest is destRoot, never sourcePath. Apply uses Relocate*.
// Review if: Adult gains an import walk, or anthology import is added.

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/labbersanon/sakms/internal/config"
	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/naming"
	"github.com/labbersanon/sakms/internal/proposals"
	"github.com/labbersanon/sakms/internal/searchterm"
)

// ScanImportMovies walks sourcePath for videos that are not already in the
// library and identifies them the same way Rename does. Destination is
// destRoot (the Movies library root, or Kids when classify says so) — Apply
// will MOVE the file there. sourcePath is only the scan origin.
func ScanImportMovies(ctx context.Context, sess *mode.Session, libStore *library.Store, sourcePath, destRoot string, cfg MatchConfig, prober Prober) ([]proposals.Proposal, error) {
	cfg = cfg.Normalize()
	if sess.TMDB == nil {
		return nil, fmt.Errorf("tmdb isn't configured yet — add it in Settings first")
	}
	if destRoot == "" {
		return nil, fmt.Errorf("no Movies library root folder configured yet — add one in Settings first")
	}
	if sourcePath == "" {
		return nil, fmt.Errorf("source folder is required")
	}

	existing, err := libStore.List(ctx, mode.Movies)
	if err != nil {
		return nil, fmt.Errorf("loading library items: %w", err)
	}
	byTMDB := make(map[int]bool, len(existing))
	for _, item := range existing {
		byTMDB[item.TMDBID] = true
	}
	known := map[string]bool{}
	if paths, err := libStore.AllFilePaths(ctx, mode.Movies); err == nil {
		for _, p := range paths {
			known[p] = true
		}
	} else {
		for _, item := range existing {
			known[item.FilePath] = true
		}
	}

	entries, err := library.ScanRootFolder(sourcePath, known)
	if err != nil {
		return nil, fmt.Errorf("scanning %s: %w", sourcePath, err)
	}

	var out []proposals.Proposal
	for i, entry := range entries {
		if cfg.OnProgress != nil {
			cfg.OnProgress(i+1, len(entries), entry.Name)
		}
		if config.SidecarExts[filepath.Ext(entry.Name)] {
			continue
		}
		videoPath, err := library.ResolveVideoFile(entry.Path)
		if err != nil {
			continue
		}
		if searchterm.IsSampleVideo(videoPath) {
			continue
		}
		p := proposeOneLibrary(ctx, sess, byTMDB, destRoot, sourcePath, library.UnmappedEntry{
			Name: entry.Name,
			Path: videoPath,
		}, cfg, prober)
		p.Workflow = proposals.Rename
		out = append(out, p)
	}
	return out, nil
}

// ScanImportSeries walks sourcePath for episode files and identifies them
// the same way Rename does. Destination is destRoot (the Series library
// root). Anthology / in-place catalog passes are Rename-only; this walk
// only proposes files that can be moved into the library.
func ScanImportSeries(ctx context.Context, sess *mode.Session, libStore *library.Store, sourcePath, destRoot string, cfg MatchConfig, prober Prober) ([]proposals.Proposal, error) {
	cfg = cfg.Normalize()
	if sess.TMDB == nil {
		return nil, fmt.Errorf("tmdb isn't configured yet — add it in Settings first")
	}
	if destRoot == "" {
		return nil, fmt.Errorf("no Series library root folder configured yet — add one in Settings first")
	}
	if sourcePath == "" {
		return nil, fmt.Errorf("source folder is required")
	}

	allSeries, err := libStore.ListSeries(ctx)
	if err != nil {
		return nil, fmt.Errorf("loading series: %w", err)
	}
	roots := []string{destRoot, sourcePath}
	known := map[string]bool{}
	if paths, err := libStore.AllEpisodeFilePaths(ctx); err == nil {
		for _, p := range paths {
			known[p] = true
		}
	}
	tracked := map[episodeKey]bool{}
	folderIDs := map[string]int{}
	seriesByID := make(map[int]library.Series, len(allSeries))
	for _, series := range allSeries {
		episodes, err := libStore.ListEpisodes(ctx, series.ID)
		if err != nil {
			return nil, fmt.Errorf("loading episodes for %q: %w", series.Title, err)
		}
		seriesByID[series.TMDBID] = series
		pinFolderID(folderIDs, showFolderTitleKey(series.Title), series.TMDBID)
		for _, ep := range episodes {
			if ep.FilePath == "" {
				continue
			}
			known[ep.FilePath] = true
			tracked[episodeKey{tmdbID: series.TMDBID, season: ep.SeasonNumber, episode: ep.EpisodeNumber}] = true
			pinFolderID(folderIDs, showFolderKey(ep.FilePath, roots), series.TMDBID)
		}
	}

	entries, err := library.ScanRootFolder(sourcePath, known)
	if err != nil {
		return nil, fmt.Errorf("scanning %s: %w", sourcePath, err)
	}

	var out []proposals.Proposal
	n := 0
	for _, entry := range entries {
		n++
		if cfg.OnProgress != nil {
			cfg.OnProgress(n, len(entries), entry.Name)
		}
		if config.SidecarExts[filepath.Ext(entry.Name)] {
			continue
		}
		videoFiles, err := library.ResolveEpisodeVideoFiles(entry.Path)
		if err != nil {
			continue
		}
		for _, videoPath := range videoFiles {
			pin := pinnedShow{}
			if key := showFolderKey(videoPath, roots); key != "" {
				if id := folderIDs[key]; id > 0 {
					pin.tmdbID = id
					if s, ok := seriesByID[id]; ok {
						pin.title, pin.year, pin.root = s.Title, s.Year, destRoot
					}
				}
			}
			p, _ := proposeOneEpisodeLibrary(ctx, sess, tracked, pin, destRoot, sourcePath, videoPath, roots, cfg, prober)
			if p.Status == proposals.Pending {
				p.RootFolderPath = destRoot
			}
			out = append(out, p)
		}
	}
	return out, nil
}

// ImportDestPath is the library path RelocateMovie/RelocateEpisode will use
// for a pending import proposal. Empty when the row is not ready to move.
func ImportDestPath(p proposals.Proposal, preset naming.Preset) string {
	if p.Status != proposals.Pending || p.RootFolderPath == "" || p.Title == "" {
		return ""
	}
	switch p.Mode {
	case mode.Movies:
		folder := filepath.Join(p.RootFolderPath, naming.MovieFolderName(preset, p.Title, p.Year, p.TMDBID))
		return filepath.Join(folder, naming.MovieFileName(preset, p.Title, p.Year, p.TMDBID, filepath.Ext(p.SourcePath)))
	case mode.Series:
		return naming.EpisodeFilePath(preset, p.RootFolderPath, p.Title, p.Year, p.TMDBID, p.SeasonNumber, p.EpisodeNumber, "", filepath.Ext(p.SourcePath))
	default:
		return ""
	}
}
