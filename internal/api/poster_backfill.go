package api

import (
	"context"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/labbersanon/sakms/internal/connections"
	"github.com/labbersanon/sakms/internal/identify"
	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mediafolder"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/nfo"
	"github.com/labbersanon/sakms/internal/serviceconn"
	"github.com/labbersanon/sakms/internal/settings"
)

// Claude 2026-09-23: DB-only poster backfill (Jellyfin stays independent — no sidecars).
// Reason: letter tiles from empty poster_url / tmdb_id=0; mass sidecar sweep hung TMDB.
// Troubleshooting: POST /api/admin/posters/backfill; progress every title in logs.
// Review if: gap tuned differently or AI fallback disabled during backfill.
// Related files: internal/api/poster.go, internal/library/library_poster.go

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
			out := resolvePoster(ctx, mode.Movies, it.TMDBID, httpClient, connStore, scStore, settingsStore, libStore)
			if out.PosterURL != "" {
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
			if tmdbID <= 0 {
				seriesFail++
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

	log.Printf("poster backfill: done movies_ok=%d movies_fail=%d series_ok=%d series_fail=%d id_repaired=%d",
		moviesOK, moviesFail, seriesOK, seriesFail, repaired)
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
	if title != "" && sess.WebSearch != nil && sess.MainstreamAI != nil {
		if url, err := identify.PickPosterURL(ctx, sess.WebSearch, sess.MainstreamAI, title, year, kind); err == nil && url != "" {
			persistPoster(ctx, libStore, m, tmdbID, url, library.PosterSourceAI)
		}
	}
}
