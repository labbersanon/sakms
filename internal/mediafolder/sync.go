package mediafolder

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/naming"
	"github.com/labbersanon/sakms/internal/nfo"
	"github.com/labbersanon/sakms/internal/tmdb"
)

const (
	tmdbPosterW   = "https://image.tmdb.org/t/p/w780"
	tmdbBackdropW = "https://image.tmdb.org/t/p/w1280"
)

// SyncDeps is the minimal surface Ensure* needs from mode.Session / stores.
type SyncDeps struct {
	HTTP   *http.Client
	TMDB   *tmdb.Client
	Lib    *library.Store
	Preset naming.Preset
	Force  bool
}

// SyncMovie writes Jellyfin sidecars for one tracked movie (by tmdb id).
func SyncMovie(ctx context.Context, d SyncDeps, tmdbID int) error {
	if d.TMDB == nil || d.Lib == nil || tmdbID <= 0 {
		return nil
	}
	item, err := d.Lib.GetByTMDBID(ctx, mode.Movies, tmdbID)
	if err != nil || item.FilePath == "" {
		return err
	}
	details, err := d.TMDB.MovieDetails(ctx, tmdbID)
	if err != nil {
		return err
	}
	dir := MovieDirFromFile(item.FilePath)
	art := Art{
		Title:    firstNonEmpty(details.Title, item.Title),
		Year:     item.Year,
		Plot:     details.Overview,
		TMDBID:   tmdbID,
		IMDBID:   details.IMDBID,
		Poster:   absTMDB(tmdbPosterW, details.PosterPath),
		Backdrop: absTMDB(tmdbBackdropW, details.BackdropPath),
	}
	if art.Year == 0 && len(details.ReleaseDate) >= 4 {
		fmt.Sscanf(details.ReleaseDate[:4], "%d", &art.Year)
	}
	if art.Poster == "" {
		if p := LocalPosterPath(dir); p != "" {
			// Keep existing local poster; still refresh NFO.
			art.Poster = ""
		}
	}
	return EnsureMovie(ctx, d.HTTP, dir, art, d.Force)
}

// SyncSeries writes Jellyfin sidecars for one tracked series row.
// Repairs tmdb_id=0 from existing tvshow.nfo when possible.
func SyncSeries(ctx context.Context, d SyncDeps, series library.Series) error {
	if d.Lib == nil {
		return nil
	}
	dir, err := resolveSeriesDir(ctx, d, series)
	if err != nil || dir == "" {
		return err
	}

	tmdbID := series.TMDBID
	tvdbID := series.TVDBID
	title := series.Title
	year := series.Year
	plot := ""

	// Prefer IDs already on disk (Jellyfin-written or prior sakms write).
	if sn := nfo.ReadSeriesFile(filepath.Join(dir, SeriesNFOFile)); sn.Title != "" || sn.TMDBID != 0 || sn.TVDBID != 0 {
		if tmdbID <= 0 && sn.TMDBID > 0 {
			tmdbID = sn.TMDBID
		}
		if tvdbID <= 0 && sn.TVDBID > 0 {
			tvdbID = sn.TVDBID
		}
		if title == "" {
			title = sn.Title
		}
		if year == 0 {
			year = sn.Year
		}
		plot = sn.Plot
	}

	if tmdbID <= 0 && tvdbID > 0 && d.TMDB != nil {
		if id, err := d.TMDB.FindTVByTVDBID(ctx, tvdbID); err == nil && id > 0 {
			tmdbID = id
		}
	}

	if tmdbID > 0 && series.TMDBID <= 0 && series.ID > 0 {
		_ = d.Lib.SetSeriesTMDBID(ctx, series.ID, tmdbID, tvdbID)
		series.TMDBID = tmdbID
	}

	art := Art{Title: title, Year: year, Plot: plot, TMDBID: tmdbID, TVDBID: tvdbID}
	if d.TMDB != nil && tmdbID > 0 {
		details, err := d.TMDB.TVDetails(ctx, tmdbID)
		if err == nil {
			art.Title = firstNonEmpty(details.Title, title)
			art.Plot = firstNonEmpty(details.Overview, plot)
			art.Poster = absTMDB(tmdbPosterW, details.PosterPath)
			art.Backdrop = absTMDB(tmdbBackdropW, details.BackdropPath)
		}
		if id, err := d.TMDB.ExternalIDs(ctx, tmdbID); err == nil && id > 0 {
			art.TVDBID = id
		}
		if imdb, err := d.TMDB.TVIMDBID(ctx, tmdbID); err == nil {
			art.IMDBID = imdb
		}
	}

	if art.Poster == "" && !HasLocalPoster(dir) && art.TMDBID == 0 {
		return nil // nothing to write
	}
	if err := EnsureSeries(ctx, d.HTTP, dir, art, d.Force); err != nil {
		return err
	}
	if art.Poster != "" && art.TMDBID > 0 {
		_ = d.Lib.SetSeriesPosterArt(ctx, art.TMDBID, art.Poster, library.PosterSourceTMDB)
	}
	return nil
}

func resolveSeriesDir(ctx context.Context, d SyncDeps, series library.Series) (string, error) {
	eps, err := d.Lib.ListEpisodes(ctx, series.ID)
	if err == nil {
		for _, ep := range eps {
			if ep.FilePath == "" {
				continue
			}
			return SeriesDirFromEpisode(ep.FilePath), nil
		}
	}
	if series.RootFolderPath == "" || series.Title == "" {
		return "", nil
	}
	// Prefer existing on-disk folder matching title (Ancient Aliens layout).
	direct := filepath.Join(series.RootFolderPath, series.Title)
	if st, err := os.Stat(direct); err == nil && st.IsDir() {
		return direct, nil
	}
	if d.Preset != "" {
		named := filepath.Join(series.RootFolderPath, naming.SeriesFolderName(d.Preset, series.Title, series.Year, series.TMDBID))
		if st, err := os.Stat(named); err == nil && st.IsDir() {
			return named, nil
		}
		return named, nil
	}
	return direct, nil
}

// Backfill writes sidecars for every tracked movie/series. Soft-fails per title.
func Backfill(ctx context.Context, d SyncDeps) (moviesOK, seriesOK, fail int) {
	if d.Lib == nil {
		return
	}
	items, err := d.Lib.List(ctx, mode.Movies)
	if err == nil {
		for _, it := range items {
			if ctx.Err() != nil {
				return
			}
			if err := SyncMovie(ctx, d, it.TMDBID); err != nil {
				fail++
			} else {
				moviesOK++
			}
		}
	}
	series, err := d.Lib.ListSeries(ctx)
	if err == nil {
		for _, s := range series {
			if ctx.Err() != nil {
				return
			}
			if err := SyncSeries(ctx, d, s); err != nil {
				fail++
			} else {
				seriesOK++
			}
		}
	}
	return
}

func absTMDB(base, path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if strings.HasPrefix(path, "https://") {
		return path
	}
	return base + path
}

func firstNonEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}
