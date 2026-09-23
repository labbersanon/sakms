package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/labbersanon/sakms/internal/apidto"
	"github.com/labbersanon/sakms/internal/connections"
	"github.com/labbersanon/sakms/internal/identify"
	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mediafolder"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/serviceconn"
	"github.com/labbersanon/sakms/internal/settings"
	"github.com/labbersanon/sakms/internal/tvdb"
)

// tmdbPosterAbsolute matches the frontend TMDB_POSTER_BASE + path used for
// grid cards. Persisted so a cache hit can return posterUrl without a
// relative-path round-trip through tmdbPoster().
const tmdbPosterAbsolute = "https://image.tmdb.org/t/p/w342"

// posterHandler resolves a Movies/Series library card's poster art (and
// synopsis) lazily, per card, keyed by tmdbId. Movies/Series only — Adult
// scenes carry their own image inline from TPDB.
//
// Claude 2026-09-22: chain TMDB → TVDB → AI+SearXNG; persist absolute URL on
// tracked rows (poster_url / poster_source).
// Reason: letter tiles for TMDB-empty/404 titles; N+1 /poster with no cache.
// Troubleshooting: missing Library posters when TMDB has no art; AI/TVDB
//   re-running every grid load.
// Review if: GET /tracked is the only poster source and this endpoint is
//   posterPath-only again.
// Related files: internal/library/library_poster.go, internal/tvdb/artwork.go,
//   internal/identify/poster_pick.go
func posterHandler(httpClient *http.Client, connStore *connections.Store, scStore *serviceconn.Store, settingsStore *settings.Store, libStore *library.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		m := mode.Mode(r.PathValue("mode"))
		if m != mode.Movies && m != mode.Series {
			http.Error(w, "poster lookup is only supported for movies/series", http.StatusBadRequest)
			return
		}
		ctx := r.Context()
		tmdbID, err := strconv.Atoi(r.URL.Query().Get("tmdbId"))
		if err != nil {
			http.Error(w, "tmdbId query parameter is required and must be an integer", http.StatusBadRequest)
			return
		}

		out := resolvePoster(ctx, m, tmdbID, httpClient, connStore, scStore, settingsStore, libStore)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(out)
	}
}

func resolvePoster(
	ctx context.Context,
	m mode.Mode,
	tmdbID int,
	httpClient *http.Client,
	connStore *connections.Store,
	scStore *serviceconn.Store,
	settingsStore *settings.Store,
	libStore *library.Store,
) (out apidto.PosterResponse) {
	var sess *mode.Session
	defer func() {
		// Claude 2026-09-22: after art resolves, write Jellyfin sidecars.
		// Reason: sakms is metadata source; import + lazy /poster both ensure disk art.
		if libStore != nil && sess != nil && (out.PosterURL != "" || out.PosterPath != "") && tmdbID > 0 {
			go syncMediafolderFromPoster(context.Background(), httpClient, libStore, sess, m, tmdbID)
		}
	}()

	var art library.PosterArt
	if libStore != nil && tmdbID != 0 {
		if m == mode.Series {
			if a, err := libStore.SeriesPosterArt(ctx, tmdbID); err == nil {
				art = a
			}
		} else if a, err := libStore.MoviePosterArt(ctx, m, tmdbID); err == nil {
			art = a
		}
		if strings.TrimSpace(art.URL) != "" {
			out.PosterURL = art.URL
		}
	}

	built, err := mode.Build(ctx, connStore, scStore, settingsStore, httpClient, nil, m)
	if err != nil || built == nil || built.TMDB == nil {
		return out
	}
	sess = built

	title := art.Title
	year := art.Year
	tvdbID := art.TVDBID

	if m == mode.Series {
		details, err := sess.TMDB.TVDetails(ctx, tmdbID)
		if err == nil {
			out.Overview = details.Overview
			if title == "" {
				title = details.Title
			}
			if details.PosterPath != "" {
				out.PosterPath = details.PosterPath
				abs := tmdbPosterAbsolute + details.PosterPath
				if out.PosterURL == "" {
					persistPoster(ctx, libStore, m, tmdbID, abs, library.PosterSourceTMDB)
					out.PosterURL = abs
				}
				return out
			}
		}
	} else {
		details, err := sess.TMDB.MovieDetails(ctx, tmdbID)
		if err == nil {
			out.Overview = details.Overview
			if title == "" {
				title = details.Title
			}
			if year == 0 {
				year = parseYearPrefix(details.ReleaseDate)
			}
			if details.PosterPath != "" {
				out.PosterPath = details.PosterPath
				abs := tmdbPosterAbsolute + details.PosterPath
				if out.PosterURL == "" {
					persistPoster(ctx, libStore, m, tmdbID, abs, library.PosterSourceTMDB)
					out.PosterURL = abs
				}
				return out
			}
		}
	}

	if out.PosterURL != "" {
		return out
	}

	if url := resolveTVDBPoster(ctx, sess, m, tmdbID, tvdbID, title, year); url != "" {
		persistPoster(ctx, libStore, m, tmdbID, url, library.PosterSourceTVDB)
		out.PosterURL = url
		return out
	}

	kind := "movie"
	if m == mode.Series {
		kind = "series"
	}
	if title != "" && sess.WebSearch != nil && sess.MainstreamAI != nil {
		if url, err := identify.PickPosterURL(ctx, sess.WebSearch, sess.MainstreamAI, title, year, kind); err == nil && url != "" {
			persistPoster(ctx, libStore, m, tmdbID, url, library.PosterSourceAI)
			out.PosterURL = url
		}
	}
	return out
}

func syncMediafolderFromPoster(ctx context.Context, httpClient *http.Client, libStore *library.Store, sess *mode.Session, m mode.Mode, tmdbID int) {
	if sess == nil || sess.TMDB == nil {
		return
	}
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	d := mediafolder.SyncDeps{HTTP: httpClient, TMDB: sess.TMDB, Lib: libStore}
	switch m {
	case mode.Movies:
		_ = mediafolder.SyncMovie(ctx, d, tmdbID)
	case mode.Series:
		if tmdbID <= 0 {
			return
		}
		if ser, err := libStore.GetSeriesByTMDBID(ctx, tmdbID); err == nil {
			_ = mediafolder.SyncSeries(ctx, d, *ser)
		}
	}
}

func resolveTVDBPoster(ctx context.Context, sess *mode.Session, m mode.Mode, tmdbID, tvdbID int, title string, year int) string {
	if sess == nil || sess.TVDB == nil {
		return ""
	}
	if m == mode.Series {
		if tvdbID <= 0 && sess.TMDB != nil && tmdbID > 0 {
			if id, err := sess.TMDB.ExternalIDs(ctx, tmdbID); err == nil {
				tvdbID = id
			}
		}
		if tvdbID <= 0 && title != "" {
			if hits, err := sess.TVDB.SearchSeries(ctx, title); err == nil {
				tvdbID = pickTVDBHit(hits, year)
			}
		}
		if tvdbID <= 0 {
			return ""
		}
		u, err := sess.TVDB.SeriesPosterURL(ctx, tvdbID)
		if err != nil {
			return ""
		}
		return u
	}
	if title == "" {
		return ""
	}
	hits, err := sess.TVDB.SearchMovies(ctx, title)
	if err != nil {
		return ""
	}
	id := pickTVDBHit(hits, year)
	if id <= 0 {
		return ""
	}
	u, err := sess.TVDB.MoviePosterURL(ctx, id)
	if err != nil {
		return ""
	}
	return u
}

func pickTVDBHit(hits []tvdb.Result, year int) int {
	if len(hits) == 0 {
		return 0
	}
	if year > 0 {
		for _, h := range hits {
			if h.Year == year && h.TVDBID > 0 {
				return h.TVDBID
			}
		}
	}
	return hits[0].TVDBID
}

func persistPoster(ctx context.Context, libStore *library.Store, m mode.Mode, tmdbID int, url, source string) {
	if libStore == nil || tmdbID == 0 || strings.TrimSpace(url) == "" {
		return
	}
	if m == mode.Series {
		_ = libStore.SetSeriesPosterArt(ctx, tmdbID, url, source)
		return
	}
	_ = libStore.SetMoviePosterArt(ctx, m, tmdbID, url, source)
}

func parseYearPrefix(date string) int {
	date = strings.TrimSpace(date)
	if len(date) < 4 {
		return 0
	}
	y, err := strconv.Atoi(date[:4])
	if err != nil {
		return 0
	}
	return y
}
