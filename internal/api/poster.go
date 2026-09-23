package api

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/labbersanon/sakms/internal/apidto"
	"github.com/labbersanon/sakms/internal/connections"
	"github.com/labbersanon/sakms/internal/identify"
	"github.com/labbersanon/sakms/internal/library"
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
// Claude 2026-09-23: chain TMDB → TVDB → image search.
// Reason: a catalog miss was picking a page URL out of a text search.
// Troubleshooting: poster_source stays empty when SearXNG image search is empty.
// Review if: image results must be approved before they are stored.
// Related files: internal/identify/poster_pick.go, internal/searxng/client.go
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
	var art library.PosterArt
	if libStore != nil && tmdbID != 0 {
		if m == mode.Series {
			if a, err := libStore.SeriesPosterArt(ctx, tmdbID); err == nil {
				art = a
			}
		} else if a, err := libStore.MoviePosterArt(ctx, m, tmdbID); err == nil {
			art = a
		}
		if posterURLIsImage(art.URL) {
			out.PosterURL = art.URL
		}
	}

	sess, err := mode.Build(ctx, connStore, scStore, settingsStore, httpClient, nil, m)
	if err != nil || sess == nil || sess.TMDB == nil {
		return out
	}

	title := art.Title
	year := art.Year
	tvdbID := art.TVDBID
	searchAsMovie := m != mode.Series

	if m == mode.Series {
		// Claude 2026-09-23: a short filed as a series may be a TMDB movie.
		// Reason: TV details for that number are a different show, or empty.
		// Troubleshooting: series poster_url is a themoviedb.org gallery page.
		// Review if: shorts are no longer stored in library_series.
		cat := loadSeriesPosterCatalog(ctx, sess.TMDB, tmdbID, year)
		if cat.Overview != "" {
			out.Overview = cat.Overview
		}
		if title == "" {
			title = cat.Title
		}
		if year == 0 {
			year = cat.Year
		}
		if cat.FromMovie {
			searchAsMovie = true
		}
		if cat.PosterPath != "" {
			out.PosterPath = cat.PosterPath
			abs := tmdbPosterAbsolute + cat.PosterPath
			if out.PosterURL == "" {
				persistPoster(ctx, libStore, m, tmdbID, abs, library.PosterSourceTMDB)
				out.PosterURL = abs
			}
			return out
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

	kind := "series"
	if searchAsMovie {
		kind = "movie"
	}
	if url := searchedPoster(ctx, sess, title, year, kind); url != "" {
		persistPoster(ctx, libStore, m, tmdbID, url, library.PosterSourceImage)
		out.PosterURL = url
		log.Printf("poster: image search %q tmdb=%d", title, tmdbID)
	}
	return out
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

func searchedPoster(ctx context.Context, sess *mode.Session, title string, year int, kind string) string {
	if sess == nil || sess.WebSearch == nil || strings.TrimSpace(title) == "" {
		return ""
	}
	url, err := identify.PickPosterURL(ctx, sess.WebSearch, sess.MainstreamAI, title, year, kind)
	if err != nil || strings.TrimSpace(url) == "" {
		return ""
	}
	return url
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
