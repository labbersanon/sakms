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
//
//	never entered library_items unless Apply ran.
//
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
	pathTMDB := tmdbID
	season, eps, parsed := library.ParseEpisodeNumbersNested(videoPath, foundRoot)
	if dummyMovieEpisodeParse(season, eps) {
		parsed = false
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
	if !parsed {
		titleHint := nestTitleHint(hint.Title, showFolder, videoPath)
		parent, nestSeason, nestEp, ok := findEpisodeNest(ctx, libStore, titleHint)
		var epTitle, airDate string
		if !ok {
			dummyFolder := isDummyMovieFolder(showFolder, videoPath)
			allowSearch := allowNestSearchCreate(showFolder, titleHint, dummyFolder)
			parent, nestSeason, nestEp, epTitle, airDate, ok = findTVDBEpisodeNest(ctx, sess, libStore, titleHint, yearFromShowFolder(showFolder), foundRoot, allowSearch)
		}
		if !ok {
			return false, nil
		}
		prior, _ := libStore.GetEpisode(ctx, parent.ID, nestSeason, nestEp)
		cataloged, err := upsertCatalogedEpisode(ctx, sess, libStore, catalogEpisode{
			TMDBID: parent.TMDBID, TVDBID: parent.TVDBID, Title: parent.Title, Year: parent.Year,
			Season: nestSeason, Episodes: []int{nestEp}, VideoPath: videoPath, FoundRoot: foundRoot,
			AttachExtra: true, EpisodeTitle: epTitle, AirDate: airDate,
		})
		if err != nil || !cataloged {
			return cataloged, err
		}
		retireStrayMovieSeries(ctx, libStore, pathTMDB, videoPath, parent.TMDBID)
		recordNestIdentification(ctx, libStore, parent, nestSeason, nestEp, videoPath, foundRoot, prior)
		return true, nil
	}
	if tmdbID == 0 {
		// Claude 2026-09-24: year-season without a show id still catalogs in place.
		// Reason: Looney Tunes/1958/S1958E14 has a parse but no tvshow.nfo;
		//   parent is the show-folder title among pre-1970 tracked / SearchSeries.
		// Troubleshooting: year-season shorts stay untracked after Save.
		// Review if: catalog grows a TMDB-search fallback for modern SxxExx.
		if !library.IsYearSeason(season) {
			return false, nil
		}
		parent, ok := findParentByShowFolder(ctx, sess, libStore, showFolder, foundRoot)
		if !ok {
			return false, nil
		}
		prior, _ := libStore.GetEpisode(ctx, parent.ID, season, firstEpisode(eps))
		cataloged, err := upsertCatalogedEpisode(ctx, sess, libStore, catalogEpisode{
			TMDBID: parent.TMDBID, TVDBID: parent.TVDBID, Title: parent.Title, Year: parent.Year,
			Season: season, Episodes: eps, VideoPath: videoPath, FoundRoot: foundRoot,
			AttachExtra: true,
		})
		if err != nil || !cataloged {
			return cataloged, err
		}
		recordNestIdentification(ctx, libStore, parent, season, firstEpisode(eps), videoPath, foundRoot, prior)
		return true, nil
	}
	return upsertCatalogedEpisode(ctx, sess, libStore, catalogEpisode{
		TMDBID: tmdbID, TVDBID: hint.TVDBID, Title: title, Year: hint.Year,
		Season: season, Episodes: eps, VideoPath: videoPath, FoundRoot: foundRoot,
		AttachExtra: true,
	})
}

func firstEpisode(eps []int) int {
	if len(eps) == 0 {
		return 0
	}
	return eps[0]
}

type catalogEpisode struct {
	TMDBID       int
	TVDBID       int
	Title        string
	Year         int
	Season       int
	Episodes     []int
	VideoPath    string
	FoundRoot    string
	AttachExtra  bool
	EpisodeTitle string
	AirDate      string
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
		epTitle, airDate := in.EpisodeTitle, in.AirDate
		if got != nil {
			if epTitle == "" {
				epTitle = got.Title
			}
			if airDate == "" {
				airDate = got.AirDate
			}
		}
		_, err = libStore.UpsertEpisode(ctx, library.Episode{
			SeriesID:      series.ID,
			SeasonNumber:  in.Season,
			EpisodeNumber: epNum,
			Title:         epTitle,
			AirDate:       airDate,
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
