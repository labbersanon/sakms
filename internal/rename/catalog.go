package rename

import (
	"context"
	"errors"
	"path/filepath"
	"strings"

	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/naming"
	"github.com/labbersanon/sakms/internal/nfo"
)

// catalogMovieAtPath writes a library row for a video that already has a TMDB
// id on disk (movie.nfo / same-basename .nfo / [tmdbid-N] folder tag). It does
// not move or rename the file. Returns true when the path is now tracked.
//
// Claude 2026-09-23: catalog existing kids/main orphans that Rename skipped.
// Reason: MatchesMovieSchema skips already-named folders; nfo files still
//   never entered library_items unless Apply ran.
// Troubleshooting: Kids root saved, Library empty, no scan progress.
// Review if: catalog grows a TMDB-search fallback for nfo-less orphans.
func catalogMovieAtPath(ctx context.Context, libStore *library.Store, videoPath, foundRoot string) (bool, error) {
	if libStore == nil || videoPath == "" {
		return false, nil
	}
	hint := nfo.ReadSidecar(videoPath)
	tmdbID := hint.TMDBID
	if tmdbID <= 0 {
		tmdbID = naming.TMDBIDFromPath(videoPath)
	}
	if tmdbID <= 0 {
		tmdbID = naming.TMDBIDFromPath(filepath.Dir(videoPath))
	}
	if tmdbID <= 0 {
		return false, nil
	}
	title := strings.TrimSpace(hint.Title)
	if title == "" {
		title = strings.TrimSuffix(filepath.Base(videoPath), filepath.Ext(videoPath))
	}
	existing, err := libStore.GetByTMDBID(ctx, mode.Movies, tmdbID)
	if err != nil && !errors.Is(err, library.ErrNotFound) {
		return false, err
	}
	if existing != nil {
		if existing.FilePath == videoPath {
			return true, nil
		}
		_, err = libStore.UpsertFile(ctx, library.ItemFile{
			ItemID: existing.ID, FilePath: videoPath, IsPrimary: false,
			Size: library.FileSize(videoPath),
		})
		return err == nil, err
	}
	item, err := libStore.Upsert(ctx, library.Item{
		Mode:           mode.Movies,
		TMDBID:         tmdbID,
		Title:          title,
		Year:           hint.Year,
		FilePath:       videoPath,
		RootFolderPath: foundRoot,
		Size:           library.FileSize(videoPath),
	})
	if err != nil {
		return false, err
	}
	_, err = libStore.UpsertFile(ctx, library.ItemFile{
		ItemID: item.ID, FilePath: videoPath, IsPrimary: true,
		Size: item.Size,
	})
	return err == nil, err
}

// catalogEpisodeAtPath writes series + episode rows in place. Show id comes
// from a folder-corroborated nfo (TMDB or TVDB→TMDB) or [tmdbid-N]. Episode
// numbers come from sequential SxxExx or year-season shorts nesting.
func catalogEpisodeAtPath(ctx context.Context, sess *mode.Session, libStore *library.Store, videoPath, foundRoot string, roots []string) (bool, error) {
	if libStore == nil || videoPath == "" {
		return false, nil
	}
	if len(roots) == 0 && foundRoot != "" {
		roots = []string{foundRoot}
	}
	showFolder := showFolderName(videoPath, roots)
	hint := trustedSeriesSidecar(videoPath, showFolder, foundRoot)
	tmdbID := catalogShowTMDBID(ctx, sess, hint, videoPath)
	if tmdbID == 0 {
		return false, nil
	}
	season, eps, ok := library.ParseEpisodeNumbersNested(videoPath, foundRoot)
	if !ok || len(eps) == 0 {
		return false, nil
	}
	title := strings.TrimSpace(hint.Title)
	if title == "" {
		title = showFolder
	}
	if title == "" {
		title = filepath.Base(filepath.Dir(filepath.Dir(videoPath)))
		if strings.HasPrefix(strings.ToLower(title), "season ") || library.IsYearSeason(season) {
			title = showFolder
		}
	}
	return upsertCatalogedEpisode(ctx, sess, libStore, catalogEpisode{
		TMDBID: tmdbID, TVDBID: hint.TVDBID, Title: title, Year: hint.Year,
		Season: season, Episodes: eps, VideoPath: videoPath, FoundRoot: foundRoot,
		AttachExtra: true,
	})
}

type catalogEpisode struct {
	TMDBID    int
	TVDBID    int
	Title     string
	Year      int
	Season    int
	Episodes  []int
	VideoPath   string
	FoundRoot   string
	AttachExtra bool
}

func upsertCatalogedEpisode(ctx context.Context, sess *mode.Session, libStore *library.Store, in catalogEpisode) (bool, error) {
	if in.TMDBID == 0 || in.VideoPath == "" {
		return false, nil
	}
	series, err := libStore.GetSeriesByTMDBID(ctx, in.TMDBID)
	if err != nil && !errors.Is(err, library.ErrNotFound) {
		return false, err
	}
	if series == nil {
		created, upErr := libStore.UpsertSeries(ctx, library.Series{
			TMDBID:         in.TMDBID,
			TVDBID:         in.TVDBID,
			Title:          in.Title,
			Year:           in.Year,
			RootFolderPath: in.FoundRoot,
		})
		if upErr != nil {
			return false, upErr
		}
		series = &created
	}
	if sess != nil && sess.KidsRootPath != "" && in.FoundRoot == sess.KidsRootPath {
		if tagErr := libStore.AddSeriesTag(ctx, series.ID, "kids"); tagErr != nil {
			return false, tagErr
		}
	}
	cataloged := false
	for _, epNum := range in.Episodes {
		got, getErr := libStore.GetEpisode(ctx, series.ID, in.Season, epNum)
		if getErr != nil && !errors.Is(getErr, library.ErrNotFound) {
			return cataloged, getErr
		}
		if got != nil && got.FilePath != "" && got.FilePath != in.VideoPath {
			if !in.AttachExtra {
				continue
			}
			_, err = libStore.UpsertEpisodeFile(ctx, library.EpisodeFile{
				EpisodeID: got.ID, FilePath: in.VideoPath, IsPrimary: false,
				Size: library.FileSize(in.VideoPath),
			})
			if err != nil {
				return cataloged, err
			}
			cataloged = true
			continue
		}
		if got != nil && got.FilePath == in.VideoPath {
			cataloged = true
			continue
		}
		_, err = libStore.UpsertEpisode(ctx, library.Episode{
			SeriesID:      series.ID,
			SeasonNumber:  in.Season,
			EpisodeNumber: epNum,
			FilePath:      in.VideoPath,
			Size:          library.FileSize(in.VideoPath),
		})
		if err != nil {
			return cataloged, err
		}
		cataloged = true
	}
	return cataloged, nil
}
