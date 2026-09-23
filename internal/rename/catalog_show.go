package rename

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/nfo"
	"github.com/labbersanon/sakms/internal/tmdb"
)

// seriesSidecarAgrees is true when the nfo title and the on-disk show folder
// can be the same show. A missing title or folder cannot disagree. Token
// overlap is bidirectional so "Looney Toons" still matches "Looney Tunes".
//
// Claude 2026-09-23: discard a tvshow.nfo that names a different series.
// Reason: kids Looney Toons shipped The Tooney and Russo Show's TVDB id.
// Review if: nfo title is empty but ids are still wrong — then folder search
//   must win, not the ids.
func seriesSidecarAgrees(hint nfo.SeriesNFO, showFolder string) bool {
	title := strings.TrimSpace(hint.Title)
	folder := strings.TrimSpace(showFolder)
	if title == "" || folder == "" {
		return true
	}
	return HasTitleTokenOverlap(folder, title) || HasTitleTokenOverlap(title, folder)
}

func trustedSeriesSidecar(videoPath, showFolder, root string) nfo.SeriesNFO {
	hint := readSeriesSidecarNested(videoPath, root)
	if !seriesSidecarAgrees(hint, showFolder) {
		return nfo.SeriesNFO{}
	}
	return hint
}

// readSeriesSidecarNested walks up from the video to root for tvshow.nfo so
// Show/1958/Disc 1/file.mkv still sees Show/tvshow.nfo.
func readSeriesSidecarNested(videoPath, root string) nfo.SeriesNFO {
	if s := nfo.ReadSeriesSidecarAny(videoPath); s.TMDBID != 0 || s.TVDBID != 0 || s.Title != "" {
		return s
	}
	dir := filepath.Dir(videoPath)
	root = filepath.Clean(root)
	for dir != "" && dir != "." {
		p := filepath.Join(dir, "tvshow.nfo")
		if s, err := nfo.ReadSeries(p); err == nil && (s.TMDBID != 0 || s.TVDBID != 0 || s.Title != "") {
			return s
		}
		if root != "" && filepath.Clean(dir) == root {
			break
		}
		next := filepath.Dir(dir)
		if next == dir {
			break
		}
		dir = next
	}
	return nfo.SeriesNFO{}
}

func resolveTMDBFromTVDB(ctx context.Context, client *tmdb.Client, tvdbID int) int {
	if client == nil || tvdbID <= 0 {
		return 0
	}
	id, err := client.FindTVByTVDBID(ctx, tvdbID)
	if err != nil || id <= 0 {
		return 0
	}
	return id
}

func rootContaining(path string, roots []string) string {
	clean := filepath.Clean(path)
	best := ""
	for _, root := range roots {
		root = filepath.Clean(root)
		if root == "" {
			continue
		}
		rel, err := filepath.Rel(root, clean)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		if len(root) > len(best) {
			best = root
		}
	}
	return best
}

// seriesSeasonAcceptable is true when TMDB lists that season, or the season
// is a pre-2000 year-season (TMDB often numbers shorts sequentially).
func seriesSeasonAcceptable(ctx context.Context, client *tmdb.Client, tmdbID, season int) bool {
	if library.IsYearSeason(season) {
		return true
	}
	if client == nil {
		return false
	}
	_, err := client.SeasonDetails(ctx, tmdbID, season)
	return err == nil
}
