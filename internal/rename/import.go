package rename

// Claude 2026-09-29: Organize Import now includes Adult.
// Reason: dump-folder Adult files reuse identifyAdultFiles + ApplyLibraryAdult
//   (MOVE). Unmatched+phash mint a local Pending (box=local, phash:<hash>).
//   Already-tracked box/scene or phash becomes PendingAlternate like Rename.
//   MatchesAdultSchema is NOT skipped — a dump file may already look named.
// Troubleshooting: dest is destRoot (Adult library root), never sourcePath.
//   Identify + hasher required. No Kids split. Confirm reconstructs identity.
// Review if: Adult import gains a Kids root, or unmatched-without-phash is
//   allowed to MOVE.

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

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

// ScanImportAdult walks sourcePath for videos that are not already in the
// Adult library and identifies them the same way Rename does
// (identifyAdultFiles: phash → stash-box/TPDB → text). Destination is
// destRoot (the Adult library root). Unlike ScanLibraryAdult, a file that
// already matches AdultFileName is still proposed when it is sitting in the
// dump folder — schema-skip is Rename-only, for files already in the library.
//
// Unmatched files with a computed phash become Pending local scenes
// (box=local, scene_id=phash:<hash>) so Confirm can MOVE them. Already-tracked
// catalog or phash hits stay on the PendingAlternate fold that
// buildAdultLibraryProposal already applies.
func ScanImportAdult(ctx context.Context, sess *mode.Session, libStore *library.Store, hasher PHasher, prober Prober, sourcePath, destRoot string, cfg MatchConfig) ([]proposals.Proposal, error) {
	cfg = cfg.Normalize()
	if sess == nil || sess.Identify == nil {
		return nil, fmt.Errorf("adult identification isn't configured — add a connection for your chosen AI provider and set the AI model in Settings, plus at least one of StashDB/FansDB/TPDB")
	}
	if hasher == nil {
		return nil, fmt.Errorf("adult import needs a video hasher")
	}
	if destRoot == "" {
		return nil, fmt.Errorf("no Adult library root folder configured yet — add one in Settings first")
	}
	if sourcePath == "" {
		return nil, fmt.Errorf("source folder is required")
	}

	known := map[string]bool{}
	if paths, err := libStore.AllScenePaths(ctx); err == nil {
		for _, p := range paths {
			known[p] = true
		}
	} else {
		scenes, listErr := libStore.ListScenes(ctx)
		if listErr != nil {
			return nil, fmt.Errorf("loading library scenes: %w", listErr)
		}
		for _, sc := range scenes {
			known[sc.FilePath] = true
		}
	}

	entries, err := library.ScanRootFolder(sourcePath, known)
	if err != nil {
		return nil, fmt.Errorf("scanning %s: %w", sourcePath, err)
	}

	type candidate struct {
		entry     library.UnmappedEntry
		videoPath string
	}
	var candidates []candidate
	for _, entry := range entries {
		if cfg.OnProgress != nil {
			cfg.OnProgress(len(candidates)+1, len(entries), entry.Name)
		}
		if config.SidecarExts[strings.ToLower(filepath.Ext(entry.Name))] {
			continue
		}
		videoPath, err := library.ResolveVideoFile(entry.Path)
		if err != nil {
			continue
		}
		if searchterm.IsSampleVideo(videoPath) {
			continue
		}
		candidates = append(candidates, candidate{entry: entry, videoPath: videoPath})
	}

	files := make([]adultFileID, len(candidates))
	for i, c := range candidates {
		files[i] = adultFileID{
			path:       c.videoPath,
			stem:       filepath.Base(c.videoPath),
			parentName: filepath.Base(filepath.Dir(c.videoPath)),
		}
	}
	ids := identifyAdultFiles(ctx, sess, hasher, prober, files)

	var out []proposals.Proposal
	for i, c := range candidates {
		p := buildAdultLibraryProposal(ctx, libStore, destRoot, c.entry, c.videoPath, ids[i])
		p.Workflow = proposals.Rename
		mintAdultImportLocal(&p, c.videoPath, ids[i])
		out = append(out, p)
	}
	return out, nil
}

// mintAdultImportLocal turns an Unmatched Adult import row into a Pending
// local scene when a phash exists. Catalog Pending and PendingAlternate rows
// are left alone. A DB-error Unmatched (could not check tracked) is left
// unmatched so apply does not invent a second identity.
func mintAdultImportLocal(p *proposals.Proposal, videoPath string, id adultIdentification) {
	if p == nil || p.Status == proposals.Pending {
		return
	}
	if !id.hashed || strings.TrimSpace(id.phash) == "" {
		return
	}
	if strings.Contains(p.Reason, "could not check whether") {
		return
	}
	title := strings.TrimSpace(p.Title)
	if title == "" {
		title = strings.TrimSuffix(filepath.Base(videoPath), filepath.Ext(videoPath))
	}
	p.Status = proposals.Pending
	p.Title = title
	p.GiveBackBox = library.LocalSceneBox
	p.GiveBackSceneID = library.LocalSceneID(id.phash)
	p.PHash = id.phash
	if p.DurationSeconds == 0 {
		p.DurationSeconds = id.duration
	}
	p.Reason = "local identity — no catalog scene; will MOVE as a local scene"
}

// ImportDestPath is the library path RelocateMovie/RelocateEpisode/
// RelocateAdultScene will use for a pending import proposal. Empty when the
// row is not ready to move.
func ImportDestPath(p proposals.Proposal, preset naming.Preset) string {
	if p.Status != proposals.Pending || p.RootFolderPath == "" || p.Title == "" {
		return ""
	}
	switch p.Mode {
	case mode.Movies:
		folder := filepath.Join(p.RootFolderPath, naming.MovieFolderName(preset, p.Title, p.Year, p.TMDBID))
		return filepath.Join(folder, naming.MovieFileName(preset, p.Title, p.Year, p.TMDBID, filepath.Ext(p.SourcePath)))
	case mode.Series:
		eps := []int{p.EpisodeNumber}
		if len(p.ExtraEpisodeNumbers) > 0 {
			eps = append(eps, p.ExtraEpisodeNumbers...)
		}
		seriesFolder := naming.SeriesFolderName(preset, p.Title, p.Year, p.TMDBID)
		seasonDir := filepath.Join(p.RootFolderPath, seriesFolder, naming.SeasonDirName(p.SeasonNumber))
		return filepath.Join(seasonDir, naming.EpisodeRangeFileName(preset, p.Title, p.SeasonNumber, eps, "", filepath.Ext(p.SourcePath)))
	case mode.Adult:
		if p.GiveBackBox == "" || p.GiveBackSceneID == "" {
			return ""
		}
		return filepath.Join(p.RootFolderPath, naming.AdultFileName(p.Studio, p.Title, p.Date, p.PHash, filepath.Ext(p.SourcePath)))
	default:
		return ""
	}
}
