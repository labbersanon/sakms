package api

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/labbersanon/sakms/internal/connections"
	"github.com/labbersanon/sakms/internal/identify"
	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mediafolder"
	"github.com/labbersanon/sakms/internal/mediainfo"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/nfo"
	"github.com/labbersanon/sakms/internal/rename"
	"github.com/labbersanon/sakms/internal/serviceconn"
	"github.com/labbersanon/sakms/internal/settings"
	"github.com/labbersanon/sakms/internal/tmdb"
)

// Claude 2026-09-23: DB-only poster backfill (Jellyfin stays independent — no sidecars).
// Reason: letter tiles from empty poster_url / tmdb_id=0; mass sidecar sweep hung TMDB.
// Troubleshooting: POST /api/admin/posters/backfill; progress every title in logs.
// Review if: gap tuned differently or AI fallback disabled during backfill.
// Related files: internal/api/poster.go, internal/library/library_poster.go
//
// Claude 2026-09-23: series identity uses the movie web-search fallthrough.
// Reason: poster list is tmdb_id=0 or empty art, so a negative tmdb_id with a
//   poster (Laurel & Hardy) never reached NFO repair.
// Troubleshooting: series_id_repaired stays 0 → row still tmdb_id<=0; check
//   "web-search" / "guess-title" on the series log line.
// Review if: series embedded tags become an identity source.
// Related files: internal/library/library_series_ids.go

// posterBackfillGap spaces TMDB/TVDB/AI calls so a full-library pass cannot
// stampede outbound APIs the way the unthrottled mediafolder boot sweep did.
const posterBackfillGap = 2 * time.Second

// NewPosterBackfillMux exposes POST /api/admin/posters/backfill — throttled
// repair of missing poster_url (and series tmdb_id=0 via existing NFO when present).
func NewPosterBackfillMux(
	httpClient *http.Client,
	connStore *connections.Store,
	scStore *serviceconn.Store,
	settingsStore *settings.Store,
	libStore *library.Store,
) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/admin/posters/backfill", posterBackfillHandler(httpClient, connStore, scStore, settingsStore, libStore))
	return mux
}

func posterBackfillHandler(
	httpClient *http.Client,
	connStore *connections.Store,
	scStore *serviceconn.Store,
	settingsStore *settings.Store,
	libStore *library.Store,
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		go RunPosterBackfill(context.Background(), httpClient, connStore, scStore, settingsStore, libStore)
		w.WriteHeader(http.StatusAccepted)
	}
}

// RunPosterBackfill fills poster_url for tracked Movies/Series missing art.
// Serial, delayed between titles. Soft-fails per title; respects ctx cancel.
func RunPosterBackfill(
	ctx context.Context,
	httpClient *http.Client,
	connStore *connections.Store,
	scStore *serviceconn.Store,
	settingsStore *settings.Store,
	libStore *library.Store,
) {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	if libStore == nil {
		return
	}
	log.Printf("poster backfill: starting (gap=%s)", posterBackfillGap)

	prober := mediainfo.New()
	idRepaired := 0
	if needID, err := libStore.ListMoviesNeedingIdentity(ctx); err != nil {
		log.Printf("poster backfill: list movies needing identity: %v", err)
	} else if len(needID) > 0 {
		sess, err := mode.Build(ctx, connStore, scStore, settingsStore, httpClient, nil, mode.Movies)
		if err != nil || sess == nil || sess.TMDB == nil {
			log.Printf("poster backfill: cannot repair movie identity (session): %v", err)
		} else {
			for i, it := range needID {
				if ctx.Err() != nil {
					log.Printf("poster backfill: cancelled during identity repair after %d", i)
					return
				}
				if repairMovieIdentity(ctx, libStore, sess, prober, it) {
					idRepaired++
				}
				if err := sleepBackfillGap(ctx); err != nil {
					return
				}
			}
		}
	}

	moviesOK, moviesFail := 0, 0
	movies, err := libStore.ListMoviesNeedingPoster(ctx, mode.Movies)
	if err != nil {
		log.Printf("poster backfill: list movies: %v", err)
	} else {
		for i, it := range movies {
			if ctx.Err() != nil {
				log.Printf("poster backfill: cancelled after %d movies", i)
				return
			}
			if it.TMDBID != 0 {
				out := resolvePoster(ctx, mode.Movies, it.TMDBID, httpClient, connStore, scStore, settingsStore, libStore)
				if out.PosterURL != "" {
					moviesOK++
					if err := sleepBackfillGap(ctx); err != nil {
						log.Printf("poster backfill: cancelled during movies gap")
						return
					}
					continue
				}
			} else if applyImageSearchPoster(ctx, libStore, moviePosterSession(ctx, httpClient, connStore, scStore, settingsStore), it) {
				moviesOK++
				if err := sleepBackfillGap(ctx); err != nil {
					log.Printf("poster backfill: cancelled during movies gap")
					return
				}
				continue
			}
			if applyLocalFilmPoster(ctx, libStore, it) {
				moviesOK++
			} else {
				moviesFail++
			}
			if err := sleepBackfillGap(ctx); err != nil {
				log.Printf("poster backfill: cancelled during movies gap")
				return
			}
		}
	}

	seriesOK, seriesFail, repaired := 0, 0, 0
	if needSeries, err := libStore.ListSeriesNeedingIdentity(ctx); err != nil {
		log.Printf("poster backfill: list series needing identity: %v", err)
	} else if len(needSeries) > 0 {
		sess, err := mode.Build(ctx, connStore, scStore, settingsStore, httpClient, nil, mode.Series)
		if err != nil || sess == nil || sess.TMDB == nil {
			log.Printf("poster backfill: cannot repair series identity (session): %v", err)
		} else {
			for i, ser := range needSeries {
				if ctx.Err() != nil {
					log.Printf("poster backfill: cancelled during series identity repair after %d", i)
					return
				}
				if repairSeriesIdentity(ctx, libStore, sess, ser) {
					repaired++
				}
				if err := sleepBackfillGap(ctx); err != nil {
					return
				}
			}
		}
	}

	series, err := libStore.ListSeriesNeedingPoster(ctx)
	if err != nil {
		log.Printf("poster backfill: list series: %v", err)
	} else {
		for i, ser := range series {
			if ctx.Err() != nil {
				log.Printf("poster backfill: cancelled after %d series", i)
				return
			}
			tmdbID := ser.TMDBID
			if tmdbID <= 0 {
				if id, ok := repairSeriesTMDBFromDisk(ctx, libStore, ser); ok {
					tmdbID = id
					repaired++
				}
			}
			if tmdbID == 0 {
				if applySeriesImagePoster(ctx, libStore, seriesPosterSession(ctx, httpClient, connStore, scStore, settingsStore), ser) {
					seriesOK++
				} else {
					seriesFail++
				}
				if err := sleepBackfillGap(ctx); err != nil {
					return
				}
				continue
			}
			out := resolvePoster(ctx, mode.Series, tmdbID, httpClient, connStore, scStore, settingsStore, libStore)
			if out.PosterURL != "" {
				seriesOK++
			} else {
				seriesFail++
			}
			if err := sleepBackfillGap(ctx); err != nil {
				log.Printf("poster backfill: cancelled during series gap")
				return
			}
		}
	}

	log.Printf("poster backfill: done movies_ok=%d movies_fail=%d series_ok=%d series_fail=%d series_id_repaired=%d movie_id_repaired=%d",
		moviesOK, moviesFail, seriesOK, seriesFail, repaired, idRepaired)
}

func repairMovieIdentity(ctx context.Context, libStore *library.Store, sess *mode.Session, prober *mediainfo.Prober, it library.Item) bool {
	if it.FilePath == "" || sess == nil || sess.TMDB == nil {
		return false
	}
	// 1) Embedded tags
	if prober != nil {
		if probe, err := prober.Probe(ctx, it.FilePath); err == nil && probe != nil && probe.Tags.HasIdentity() {
			if tmdbID, title, year, err := rename.ResolveMovieTMDBFromTags(ctx, sess.TMDB, probe.Tags); err == nil && tmdbID > 0 {
				return commitMovieIdentityRepair(ctx, libStore, sess, it, tmdbID, title, year, "embedded tags")
			}
		}
	}
	// 2) Existing movie.nfo (read-only). IMDb is used when the file has no TMDB id.
	if hint := nfo.ReadSidecar(it.FilePath); hint.TMDBID > 0 || hint.IMDBID != "" {
		tmdbID := hint.TMDBID
		title, year := hint.Title, hint.Year
		via := "nfo"
		if tmdbID <= 0 {
			id, err := sess.TMDB.FindMovieByIMDBID(ctx, hint.IMDBID)
			if err != nil || id <= 0 {
				tmdbID = 0
			} else {
				tmdbID = id
				via = "nfo-imdb"
			}
		}
		if tmdbID > 0 {
			if details, err := sess.TMDB.MovieDetails(ctx, tmdbID); err == nil {
				title = details.Title
				if year == 0 {
					year = parseYearPrefix(details.ReleaseDate)
				}
			}
			return commitMovieIdentityRepair(ctx, libStore, sess, it, tmdbID, title, year, via)
		}
	}
	// 3) GuessTitle → TMDB. A title without a dot beats the release filename.
	seed := filepath.Base(it.FilePath)
	if it.Title != "" && !strings.Contains(it.Title, ".") {
		seed = it.Title
	}
	query := seed
	if sess.MainstreamAI != nil {
		g, err := identify.GuessTitle(ctx, sess.MainstreamAI, seed)
		if err == nil && g.Title != "" {
			query = g.Title
			if tmdbID, title, year, err := rename.ResolveMovieTMDBFromTags(ctx, sess.TMDB, mediainfo.Tags{
				Title: g.Title, Year: g.Year,
			}); err == nil && tmdbID > 0 {
				return commitMovieIdentityRepair(ctx, libStore, sess, it, tmdbID, title, year, "guess-title")
			}
		}
	}
	// 4) Web search → TMDB when the guess missed or was declined.
	grounded, err := identify.GroundTitleViaSearch(ctx, sess.WebSearch, sess.MainstreamAI, query)
	if err != nil || grounded.Title == "" {
		return false
	}
	tmdbID, title, year, err := rename.ResolveMovieTMDBFromTags(ctx, sess.TMDB, mediainfo.Tags{
		Title: grounded.Title, Year: grounded.Year,
	})
	if err != nil || tmdbID <= 0 {
		return false
	}
	return commitMovieIdentityRepair(ctx, libStore, sess, it, tmdbID, title, year, "web-search")
}

func commitMovieIdentityRepair(ctx context.Context, libStore *library.Store, sess *mode.Session, it library.Item, tmdbID int, title string, year int, via string) bool {
	// Claude 2026-09-23: a second file of a movie already in the library is linked, not rekeyed.
	// Reason: A Toxic Love Story's NFO id 1723460 is already row 197; SetMovieTMDBID conflicts.
	// Troubleshooting: movie stays tmdb_id<=0 with "could not set tmdb_id" → owner row missing.
	// Review if: duplicate copies should replace the primary file instead of attaching.
	if owner, err := libStore.GetByTMDBID(ctx, mode.Movies, tmdbID); err == nil && owner.ID != it.ID {
		return linkDuplicateMovie(ctx, libStore, it, owner, tmdbID, via)
	} else if err != nil && !errors.Is(err, library.ErrNotFound) {
		log.Printf("poster backfill: lookup movie tmdb=%d: %v", tmdbID, err)
		return false
	}
	if err := libStore.SetMovieTMDBID(ctx, it.ID, tmdbID, title, year); err != nil {
		log.Printf("poster backfill: repair movie id=%d → tmdb=%d: %v", it.ID, tmdbID, err)
		return false
	}
	log.Printf("poster backfill: repaired movie %q id=%d → tmdb=%d (%s)", it.Title, it.ID, tmdbID, via)
	ensureImportPoster(ctx, libStore, sess, mode.Movies, tmdbID)
	return true
}

func linkDuplicateMovie(ctx context.Context, libStore *library.Store, it library.Item, owner *library.Item, tmdbID int, via string) bool {
	if it.FilePath == "" || it.FilePath == owner.FilePath {
		return false
	}
	if _, err := libStore.UpsertFile(ctx, library.ItemFile{
		ItemID: owner.ID, FilePath: it.FilePath, IsPrimary: false,
	}); err != nil {
		log.Printf("poster backfill: link %q onto movie id=%d: %v", it.FilePath, owner.ID, err)
		return false
	}
	if err := libStore.Delete(ctx, it.ID); err != nil {
		log.Printf("poster backfill: retire duplicate movie id=%d: %v", it.ID, err)
		return false
	}
	log.Printf("poster backfill: linked %q id=%d onto movie %q id=%d tmdb=%d (%s)", it.FilePath, it.ID, owner.Title, owner.ID, tmdbID, via)
	return true
}

func sleepBackfillGap(ctx context.Context) error {
	t := time.NewTimer(posterBackfillGap)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// repairSeriesIdentity assigns a positive TMDB id: existing tvshow.nfo, then
// GuessTitle, then web-search grounding. Same order as movie repair after tags.
func repairSeriesIdentity(ctx context.Context, libStore *library.Store, sess *mode.Session, ser library.Series) bool {
	if _, ok := repairSeriesTMDBFromDisk(ctx, libStore, ser); ok {
		return true
	}
	if sess == nil || sess.TMDB == nil {
		return false
	}
	// A stored TVDB id is a stronger key than a title search.
	if ser.TVDBID > 0 {
		if id, err := sess.TMDB.FindTVByTVDBID(ctx, ser.TVDBID); err == nil && id > 0 {
			return commitSeriesIdentityRepair(ctx, libStore, sess, ser, id, "tvdb")
		}
	}
	seed := seriesIdentitySeed(ctx, libStore, ser)
	query := seed
	if sess.MainstreamAI != nil && seed != "" {
		g, err := identify.GuessTitle(ctx, sess.MainstreamAI, seed)
		if err == nil && g.Title != "" {
			query = g.Title
			if repairSeriesByTitle(ctx, libStore, sess, ser, g.Title, g.Year, "guess-title") {
				return true
			}
		}
	}
	grounded, err := identify.GroundTitleViaSearch(ctx, sess.WebSearch, sess.MainstreamAI, query)
	if err != nil || grounded.Title == "" {
		return false
	}
	return repairSeriesByTitle(ctx, libStore, sess, ser, grounded.Title, grounded.Year, "web-search")
}

func repairSeriesByTitle(ctx context.Context, libStore *library.Store, sess *mode.Session, ser library.Series, title string, year int, via string) bool {
	if year == 0 {
		year = ser.Year
	}
	tmdbID, err := searchSeriesTMDB(ctx, sess.TMDB, title, year)
	if err != nil || tmdbID <= 0 {
		return false
	}
	return commitSeriesIdentityRepair(ctx, libStore, sess, ser, tmdbID, via)
}

func seriesIdentitySeed(ctx context.Context, libStore *library.Store, ser library.Series) string {
	if ser.Title != "" && !strings.Contains(ser.Title, ".") {
		return ser.Title
	}
	eps, err := libStore.ListEpisodes(ctx, ser.ID)
	if err != nil {
		return ser.Title
	}
	for _, ep := range eps {
		if ep.FilePath != "" {
			return filepath.Base(ep.FilePath)
		}
	}
	return ser.Title
}

func searchSeriesTMDB(ctx context.Context, client *tmdb.Client, title string, year int) (int, error) {
	title = strings.TrimSpace(title)
	if client == nil || title == "" {
		return 0, nil
	}
	items, err := client.SearchTV(ctx, title)
	if err != nil || len(items) == 0 {
		return 0, err
	}
	if year <= 0 {
		return items[0].ID, nil
	}
	for _, it := range items {
		if parseYearPrefix(it.ReleaseDate) == year {
			return it.ID, nil
		}
	}
	// A known year that matches nothing is a decline. Taking items[0] assigned
	// the 1919 Hal Roach series to the 1966 cartoon.
	return 0, nil
}

func commitSeriesIdentityRepair(ctx context.Context, libStore *library.Store, sess *mode.Session, ser library.Series, tmdbID int, via string) bool {
	tvdbID := ser.TVDBID
	if tvdbID <= 0 && sess != nil && sess.TMDB != nil {
		if id, err := sess.TMDB.ExternalIDs(ctx, tmdbID); err == nil && id > 0 {
			tvdbID = id
		}
	}
	if err := libStore.SetSeriesTMDBID(ctx, ser.ID, tmdbID, tvdbID); err != nil {
		log.Printf("poster backfill: repair series id=%d → tmdb=%d: %v", ser.ID, tmdbID, err)
		return false
	}
	log.Printf("poster backfill: repaired series %q id=%d → tmdb=%d (%s)", ser.Title, ser.ID, tmdbID, via)
	ensureImportPoster(ctx, libStore, sess, mode.Series, tmdbID)
	return true
}

// repairSeriesTMDBFromDisk reads an existing tvshow.nfo (Jellyfin-written or
// otherwise) to assign tmdb_id. Read-only — does not write sidecars.
func repairSeriesTMDBFromDisk(ctx context.Context, libStore *library.Store, ser library.Series) (tmdbID int, ok bool) {
	dir := seriesDirForRepair(ctx, libStore, ser)
	if dir == "" {
		return 0, false
	}
	sn := nfo.ReadSeriesFile(filepath.Join(dir, mediafolder.SeriesNFOFile))
	tmdbID = sn.TMDBID
	tvdbID := sn.TVDBID
	if tvdbID <= 0 {
		tvdbID = ser.TVDBID
	}
	if tmdbID <= 0 {
		return 0, false
	}
	if err := libStore.SetSeriesTMDBID(ctx, ser.ID, tmdbID, tvdbID); err != nil {
		log.Printf("poster backfill: repair series id=%d tmdb=%d: %v", ser.ID, tmdbID, err)
		return 0, false
	}
	log.Printf("poster backfill: repaired series %q id=%d → tmdb=%d", ser.Title, ser.ID, tmdbID)
	return tmdbID, true
}

func seriesDirForRepair(ctx context.Context, libStore *library.Store, ser library.Series) string {
	eps, err := libStore.ListEpisodes(ctx, ser.ID)
	if err == nil {
		for _, ep := range eps {
			if ep.FilePath == "" {
				continue
			}
			return mediafolder.SeriesDirFromEpisode(ep.FilePath)
		}
	}
	if ser.RootFolderPath == "" || ser.Title == "" {
		return ""
	}
	direct := filepath.Join(ser.RootFolderPath, ser.Title)
	if st, err := os.Stat(direct); err == nil && st.IsDir() {
		return direct
	}
	return ""
}

// ensureImportPoster persists poster_url for a newly imported Movies/Series
// title using the import session (TMDB → TVDB → AI). No sidecar writes.
func ensureImportPoster(ctx context.Context, libStore *library.Store, sess *mode.Session, m mode.Mode, tmdbID int) {
	if libStore == nil || sess == nil || sess.TMDB == nil || tmdbID <= 0 {
		return
	}
	if m == mode.Series {
		if art, err := libStore.SeriesPosterArt(ctx, tmdbID); err == nil && art.URL != "" {
			return
		}
	} else {
		if art, err := libStore.MoviePosterArt(ctx, m, tmdbID); err == nil && art.URL != "" {
			return
		}
	}

	title := ""
	year := 0
	tvdbID := 0
	if m == mode.Series {
		details, err := sess.TMDB.TVDetails(ctx, tmdbID)
		if err == nil {
			title = details.Title
			if details.PosterPath != "" {
				abs := tmdbPosterAbsolute + details.PosterPath
				persistPoster(ctx, libStore, m, tmdbID, abs, library.PosterSourceTMDB)
				return
			}
		}
		if ser, err := libStore.GetSeriesByTMDBID(ctx, tmdbID); err == nil {
			if title == "" {
				title = ser.Title
			}
			year = ser.Year
			tvdbID = ser.TVDBID
		}
	} else {
		details, err := sess.TMDB.MovieDetails(ctx, tmdbID)
		if err == nil {
			title = details.Title
			year = parseYearPrefix(details.ReleaseDate)
			if details.PosterPath != "" {
				abs := tmdbPosterAbsolute + details.PosterPath
				persistPoster(ctx, libStore, m, tmdbID, abs, library.PosterSourceTMDB)
				return
			}
		}
		if item, err := libStore.GetByTMDBID(ctx, m, tmdbID); err == nil {
			if title == "" {
				title = item.Title
			}
			if year == 0 {
				year = item.Year
			}
		}
	}

	if url := resolveTVDBPoster(ctx, sess, m, tmdbID, tvdbID, title, year); url != "" {
		persistPoster(ctx, libStore, m, tmdbID, url, library.PosterSourceTVDB)
		return
	}

	kind := "movie"
	if m == mode.Series {
		kind = "series"
	}
	if url := searchedPoster(ctx, sess, title, year, kind); url != "" {
		persistPoster(ctx, libStore, m, tmdbID, url, library.PosterSourceImage)
	}
}

func moviePosterSession(ctx context.Context, httpClient *http.Client, connStore *connections.Store, scStore *serviceconn.Store, settingsStore *settings.Store) *mode.Session {
	sess, err := mode.Build(ctx, connStore, scStore, settingsStore, httpClient, nil, mode.Movies)
	if err != nil || sess == nil {
		log.Printf("poster backfill: movie image search session: %v", err)
		return nil
	}
	return sess
}

func seriesPosterSession(ctx context.Context, httpClient *http.Client, connStore *connections.Store, scStore *serviceconn.Store, settingsStore *settings.Store) *mode.Session {
	sess, err := mode.Build(ctx, connStore, scStore, settingsStore, httpClient, nil, mode.Series)
	if err != nil || sess == nil {
		log.Printf("poster backfill: series image search session: %v", err)
		return nil
	}
	return sess
}

func applyImageSearchPoster(ctx context.Context, libStore *library.Store, sess *mode.Session, it library.Item) bool {
	url := searchedPoster(ctx, sess, it.Title, it.Year, "movie")
	if url == "" {
		return false
	}
	if err := libStore.SetMoviePosterByID(ctx, it.ID, url, library.PosterSourceImage); err != nil {
		log.Printf("poster backfill: image search movie id=%d: %v", it.ID, err)
		return false
	}
	log.Printf("poster backfill: image search %q id=%d", it.Title, it.ID)
	return true
}

func applySeriesImagePoster(ctx context.Context, libStore *library.Store, sess *mode.Session, ser library.Series) bool {
	url := searchedPoster(ctx, sess, ser.Title, ser.Year, "series")
	if url == "" {
		return false
	}
	if err := libStore.SetSeriesPosterByID(ctx, ser.ID, url, library.PosterSourceImage); err != nil {
		log.Printf("poster backfill: image search series id=%d: %v", ser.ID, err)
		return false
	}
	log.Printf("poster backfill: image search %q id=%d", ser.Title, ser.ID)
	return true
}
