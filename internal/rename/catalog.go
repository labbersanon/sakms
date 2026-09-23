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

// catalogEpisodeAtPath writes series + episode rows from tvshow.nfo / episode
// nfo / [tmdbid-N] plus an SxxExx parse. It does not move files.
func catalogEpisodeAtPath(ctx context.Context, libStore *library.Store, videoPath, foundRoot string) (bool, error) {
	if libStore == nil || videoPath == "" {
		return false, nil
	}
	hint := nfo.ReadSeriesSidecar(videoPath)
	tmdbID := hint.TMDBID
	if tmdbID <= 0 {
		tmdbID = naming.TMDBIDFromPath(videoPath)
	}
	if tmdbID <= 0 {
		return false, nil
	}
	season, eps, ok := library.ParseEpisodeNumbersLoose(filepath.Base(videoPath), filepath.Base(filepath.Dir(videoPath)))
	if !ok || len(eps) == 0 {
		return false, nil
	}
	title := strings.TrimSpace(hint.Title)
	if title == "" {
		title = filepath.Base(filepath.Dir(filepath.Dir(videoPath)))
		if strings.HasPrefix(strings.ToLower(title), "season ") {
			title = filepath.Base(filepath.Dir(videoPath))
		}
	}
	series, err := libStore.GetSeriesByTMDBID(ctx, tmdbID)
	if err != nil && !errors.Is(err, library.ErrNotFound) {
		return false, err
	}
	if series == nil {
		created, upErr := libStore.UpsertSeries(ctx, library.Series{
			TMDBID:         tmdbID,
			TVDBID:         hint.TVDBID,
			Title:          title,
			Year:           hint.Year,
			RootFolderPath: foundRoot,
		})
		if upErr != nil {
			return false, upErr
		}
		series = &created
	}
	cataloged := false
	for _, epNum := range eps {
		got, getErr := libStore.GetEpisode(ctx, series.ID, season, epNum)
		if getErr != nil && !errors.Is(getErr, library.ErrNotFound) {
			return cataloged, getErr
		}
		if got != nil && got.FilePath != "" && got.FilePath != videoPath {
			_, err = libStore.UpsertEpisodeFile(ctx, library.EpisodeFile{
				EpisodeID: got.ID, FilePath: videoPath, IsPrimary: false,
				Size: library.FileSize(videoPath),
			})
			if err != nil {
				return cataloged, err
			}
			cataloged = true
			continue
		}
		if got != nil && got.FilePath == videoPath {
			cataloged = true
			continue
		}
		_, err = libStore.UpsertEpisode(ctx, library.Episode{
			SeriesID:     series.ID,
			SeasonNumber: season,
			EpisodeNumber: epNum,
			FilePath:     videoPath,
			Size:         library.FileSize(videoPath),
		})
		if err != nil {
			return cataloged, err
		}
		cataloged = true
	}
	return cataloged, nil
}
