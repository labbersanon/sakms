package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/labbersanon/sakms/internal/apidto"
	"github.com/labbersanon/sakms/internal/connections"
	"github.com/labbersanon/sakms/internal/discoverrefresh"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/serviceconn"
	"github.com/labbersanon/sakms/internal/settings"
	"github.com/labbersanon/sakms/internal/tmdb"
)

// mediaTypeForMode maps {mode} onto TMDB's media type, the same convention
// categoriesForSearch uses for Prowlarr's Newznab categories: Series is TV,
// everything else (Movies) is the movie catalog.
func mediaTypeForMode(m mode.Mode) tmdb.MediaType {
	if m == mode.Series {
		return tmdb.TV
	}
	return tmdb.Movie
}

// mapSortBy translates the Discover filter bar's UI sort key into a TMDB
// sort_by value — an allow-list mapper, deliberately NOT a passthrough, so a
// raw client string can never reach TMDB's sort_by. "newest" maps to the
// media-type-appropriate date field (first_air_date.desc for tv,
// primary_release_date.desc for movies), and anything unrecognized (including
// the empty string) falls back to the safe popularity.desc default.
func mapSortBy(uiSort string, mt tmdb.MediaType) string {
	switch uiSort {
	case "rating":
		return "vote_average.desc"
	case "newest":
		if mt == tmdb.TV {
			return "first_air_date.desc"
		}
		return "primary_release_date.desc"
	default:
		return "popularity.desc"
	}
}

// cachedDiscoverCategory reports whether category is one of the three
// mount-time rows the Discover row-content cache (internal/discoverrefresh)
// backs — trending/popular/upcoming, each a fixed per-mode key
// ("{mode}:{category}", matching tmdbTarget.cacheKey's spelling). The other
// four categories (genre/studio/network/filter) are an operator-driven
// browse with an unbounded or cross-product key space and are deliberately
// never cached (discover-scheduled-refresh plan §0.3/§4.1).
func cachedDiscoverCategory(category string) bool {
	switch category {
	case "trending", "popular", "upcoming":
		return true
	default:
		return false
	}
}

// discoverHandler returns TMDB's trending or popular titles for {mode}'s
// media type — a read-only proxy+normalize, nothing staged or persisted.
// Series items carry only their TMDB id here;
// resolving the TVDB id Sonarr's AddRequest actually needs is deferred to
// resolveTVDBIDHandler, called only once a user picks a specific title to
// search+grab — not eagerly for every item in a trending list, which would
// multiply this one TMDB call into one-plus-N for results nobody clicks.
//
// discoverCache backs cachedDiscoverCategory's three rows via the shared
// lookupDiscoverCache/writeRawJSONArray read-through helpers (§4.1); it MAY
// BE NIL, meaning "no cache" — every lookup misses and this handler behaves
// exactly as it did before this cache existed.
func discoverHandler(httpClient *http.Client, connStore *connections.Store, scStore *serviceconn.Store, settingsStore *settings.Store, discoverCache *discoverrefresh.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		m := mode.Mode(r.PathValue("mode"))
		ctx := r.Context()
		category := r.URL.Query().Get("category")
		if category == "" {
			category = "trending"
		}
		// page is TMDB's 1-based pagination cursor, backing Discover's per-row
		// "Show more". Absent/blank/invalid defaults to 1 (the first page) —
		// the pre-pagination behavior — rather than erroring, so an old client
		// or a bare first load keeps working unchanged.
		page := 1
		if p, err := strconv.Atoi(r.URL.Query().Get("page")); err == nil && p > 0 {
			page = p
		}

		sess, err := mode.Build(ctx, connStore, scStore, settingsStore, httpClient, nil, m)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if sess.TMDB == nil {
			http.Error(w, "tmdb isn't configured yet — add it in Settings first", http.StatusBadRequest)
			return
		}

		// Claude 2026-08-03: read-through cache lookup for the three
		// mount-time rows (BE-11, discover-scheduled-refresh plan §4.1).
		// Reason: must run AFTER the TMDB-not-configured check above so an
		// unconfigured connection still 400s byte-identically (§4.0's
		// ordering rule) — a decorator wrapping this handler could not do
		// that (§4.5). liveRawPage (not the client's page) is what the
		// upstream fetch below must use once filtering has made cached and
		// raw TMDB pages diverge (§4.0/H5) — every switch case, the movies
		// release-date filter's fetchPage closure, and its third argument
		// all read upstreamPage, never the raw page, from here down.
		upstreamPage := page
		if cachedDiscoverCategory(category) {
			items, hit, liveRawPage := lookupDiscoverCache(ctx, discoverCache, "tmdb", string(m)+":"+category, page)
			if hit {
				writeRawJSONArray(w, items)
				return
			}
			if liveRawPage > 0 {
				upstreamPage = liveRawPage
			}
		}

		mt := mediaTypeForMode(m)
		var items []tmdb.Item
		switch category {
		case "trending":
			items, err = sess.TMDB.Trending(ctx, mt, "week", upstreamPage)
		case "popular":
			items, err = sess.TMDB.Popular(ctx, mt, upstreamPage)
		case "upcoming":
			// UpcomingTV is TMDB's /tv/on_the_air — the closest TV analog to
			// Upcoming Movies' "future release date" (see tmdb.UpcomingTV's
			// doc comment); TMDB has no direct TV equivalent.
			if mt == tmdb.TV {
				items, err = sess.TMDB.UpcomingTV(ctx, upstreamPage)
			} else {
				items, err = sess.TMDB.UpcomingMovies(ctx, upstreamPage)
			}
		case "genre":
			genreID, gerr := strconv.Atoi(r.URL.Query().Get("genreId"))
			if gerr != nil {
				http.Error(w, "genreId query parameter is required and must be an integer", http.StatusBadRequest)
				return
			}
			if mt == tmdb.TV {
				items, err = sess.TMDB.DiscoverTVByGenre(ctx, genreID, upstreamPage)
			} else {
				items, err = sess.TMDB.DiscoverMoviesByGenre(ctx, genreID, upstreamPage)
			}
		case "studio":
			// Studios are a movie-catalog concept (TMDB production companies) —
			// there is no TV equivalent, so this category is Movies/Adult only,
			// mirroring the mode restriction network below applies the other way.
			if m == mode.Series {
				http.Error(w, "studio browsing is not available for series — TMDB companies are a movie-only concept", http.StatusBadRequest)
				return
			}
			studioID, serr := strconv.Atoi(r.URL.Query().Get("studioId"))
			if serr != nil {
				http.Error(w, "studioId query parameter is required and must be an integer", http.StatusBadRequest)
				return
			}
			items, err = sess.TMDB.DiscoverMoviesByStudio(ctx, studioID, upstreamPage)
		case "network":
			// Symmetric restriction to studio above: networks are a TV-catalog
			// concept, series only.
			if m != mode.Series {
				http.Error(w, "network browsing is only available for series", http.StatusBadRequest)
				return
			}
			networkID, nerr := strconv.Atoi(r.URL.Query().Get("networkId"))
			if nerr != nil {
				http.Error(w, "networkId query parameter is required and must be an integer", http.StatusBadRequest)
				return
			}
			items, err = sess.TMDB.DiscoverTVByNetwork(ctx, networkID, upstreamPage)
		case "filter":
			// The Discover filter bar's ad-hoc genre/year/rating/sort browse.
			// Unlike genre/studio/network above, none of these params are
			// required — category=filter with zero params is a valid pure-sort
			// or default-popularity browse — so each is parsed leniently and
			// silently skipped when absent/invalid rather than 400-ing.
			opts := tmdb.FilterOptions{SortBy: mapSortBy(r.URL.Query().Get("sortBy"), mt)}
			for _, g := range strings.Split(r.URL.Query().Get("genreIds"), ",") {
				if id, gerr := strconv.Atoi(strings.TrimSpace(g)); gerr == nil {
					opts.GenreIDs = append(opts.GenreIDs, id)
				}
			}
			if y, yerr := strconv.Atoi(r.URL.Query().Get("year")); yerr == nil {
				opts.Year = y
			}
			if mr, merr := strconv.ParseFloat(r.URL.Query().Get("minRating"), 64); merr == nil {
				opts.MinRating = mr
			}
			if mt == tmdb.TV {
				if n, nerr := strconv.Atoi(r.URL.Query().Get("networkId")); nerr == nil {
					opts.NetworkID = n
				}
				items, err = sess.TMDB.DiscoverTVFiltered(ctx, opts, upstreamPage)
			} else {
				if s, serr := strconv.Atoi(r.URL.Query().Get("studioId")); serr == nil {
					opts.StudioID = s
				}
				items, err = sess.TMDB.DiscoverMoviesFiltered(ctx, opts, upstreamPage)
			}
		default:
			http.Error(w, fmt.Sprintf("unrecognized category %q", category), http.StatusBadRequest)
			return
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}

		// Movies-only: hide titles with no US digital/physical release yet
		// (see tmdb.Client.HasUSRelease). Series is excluded by the mt check
		// (Adult never reaches this handler at all); Upcoming is deliberately
		// exempt too, since showing not-yet-released titles is that row's
		// entire purpose — only Trending/Popular claim to be "watch it now."
		// category=="filter" is deliberately NOT in this condition either: a
		// year/genre-filtered browse should be able to surface a title with no
		// US release yet, exactly as genre/studio/network already can.
		if mt == tmdb.Movie && (category == "trending" || category == "popular") {
			fetchPage := func(p int) ([]tmdb.Item, error) {
				if category == "trending" {
					return sess.TMDB.Trending(ctx, mt, "week", p)
				}
				return sess.TMDB.Popular(ctx, mt, p)
			}
			items, err = filterReleasedMovies(ctx, sess, upstreamPage, items, fetchPage)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadGateway)
				return
			}
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(items)
	}
}

// maxUnreleasedFilterRetries bounds how many extra TMDB pages
// filterReleasedMovies fetches when an entire page's movies filter out to
// empty — without this, a single page where every movie happens to be
// unreleased (rare but real, since TMDB's trending/popular ordering is
// popularity, not release status) would falsely report the row as exhausted
// to the frontend's PaginatedRow on the very next "Show more" click.
const maxUnreleasedFilterRetries = 3

// filterReleasedMovies removes movies with no US release yet from items
// (see tmdb.Client.HasUSRelease), checked with bounded concurrency. If every
// item on the page filters out, it fetches up to maxUnreleasedFilterRetries
// additional consecutive TMDB pages via fetchPage and returns the first
// page whose survivors are non-empty. A genuinely empty raw fetch (TMDB has
// no more pages) is returned as-is, with no retry — there's nothing to
// filter. This empty-batch contract is exactly what PaginatedRow's
// exhaustion check relies on (Mainstream.tsx: `if (batch.length === 0)
// setExhausted(true)`) — every built-in Trending/Popular/Upcoming Movies row
// routes through PaginatedRow, not shared.tsx's PaginatedStrip (which uses a
// `batch.length < perPage` heuristic instead). Filtering routinely returns
// FEWER than a full page even when more pages exist, so if a filtered
// category is ever rerouted through PaginatedStrip, "Show more" would
// falsely vanish after page 1 — don't make that change without also
// updating this filter's page-fetching contract.
//
// ACCEPTED LIMITATION: the frontend's own page counter increments by one per
// "Show more" click, independent of how many raw TMDB pages a single
// response actually consumed internally here. If a retry advances past a
// PARTIALLY-filtered page (some movies kept, some removed) to resolve an
// earlier logical page, the frontend's next request re-fetches that same
// raw TMDB page from scratch — its survivors would then appear a second
// time (rendered twice, no crash — Carousel's <For> keys by object
// reference, and PaginatedRow only ever appends). This only happens when a
// partial-filter page sits immediately next to a fully-empty one being
// retried past — a narrow edge case. A fully general fix would require
// threading a "last raw TMDB page consumed" cursor back to the frontend, a
// bigger wire-contract change out of scope for this pass.
func filterReleasedMovies(ctx context.Context, sess *mode.Session, page int, items []tmdb.Item, fetchPage func(int) ([]tmdb.Item, error)) ([]tmdb.Item, error) {
	filtered := discoverrefresh.FilterByUSRelease(ctx, sess.TMDB, items)
	var err error
	for attempt := page; len(filtered) == 0 && len(items) > 0 && attempt < page+maxUnreleasedFilterRetries; {
		attempt++
		items, err = fetchPage(attempt)
		if err != nil {
			return nil, err
		}
		if len(items) == 0 {
			break
		}
		filtered = discoverrefresh.FilterByUSRelease(ctx, sess.TMDB, items)
	}
	return filtered, nil
}

// discoverGenresHandler returns TMDB's fixed genre list for {mode}'s media
// type (movie genres for Movies/Adult, TV genres for Series) — reference
// data for the genre-browse row's picker and a "genre" slider's FilterValue
// dropdown in the admin editor. Not paginated; TMDB's genre list is small
// and rarely changes.
func discoverGenresHandler(httpClient *http.Client, connStore *connections.Store, scStore *serviceconn.Store, settingsStore *settings.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		m := mode.Mode(r.PathValue("mode"))
		ctx := r.Context()

		sess, err := mode.Build(ctx, connStore, scStore, settingsStore, httpClient, nil, m)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if sess.TMDB == nil {
			http.Error(w, "tmdb isn't configured yet — add it in Settings first", http.StatusBadRequest)
			return
		}

		var genres []tmdb.Genre
		if mediaTypeForMode(m) == tmdb.TV {
			genres, err = sess.TMDB.TVGenres(ctx)
		} else {
			genres, err = sess.TMDB.MovieGenres(ctx)
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(genres)
	}
}

// discoverStudiosHandler serves tmdb.KnownStudios — a fixed, static seed
// list requiring no TMDB call — backing the "browse by studio" row and the
// admin slider editor's studio picker. Global, not mode-scoped: the same
// list regardless of which mode's Discover screen is asking.
func discoverStudiosHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(tmdb.KnownStudios)
	}
}

// discoverNetworksHandler is discoverStudiosHandler's direct sibling for
// tmdb.KnownNetworks.
func discoverNetworksHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(tmdb.KnownNetworks)
	}
}

// discoverKeywordsHandler proxies TMDB's /search/keyword — the admin slider
// editor's way of resolving free-typed keyword text into the numeric TMDB
// id a "keyword" slider's FilterValue actually stores (see tmdb.Keyword's
// doc comment for why keywords, unlike genre/studio/network, have no fixed
// seed list). Global like discoverStudiosHandler/discoverNetworksHandler —
// keyword search isn't mode-specific, so this always builds a Movies-mode
// session purely to reach the shared "tmdb" connection (see
// tmdbSearchHandler's doc comment: sess.TMDB is populated identically for
// every mode).
func discoverKeywordsHandler(httpClient *http.Client, connStore *connections.Store, scStore *serviceconn.Store, settingsStore *settings.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		query := r.URL.Query().Get("q")
		if query == "" {
			http.Error(w, "q query parameter is required", http.StatusBadRequest)
			return
		}

		sess, err := mode.Build(ctx, connStore, scStore, settingsStore, httpClient, nil, mode.Movies)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if sess.TMDB == nil {
			http.Error(w, "tmdb isn't configured yet — add it in Settings first", http.StatusBadRequest)
			return
		}

		keywords, err := sess.TMDB.SearchKeywords(ctx, query)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(keywords)
	}
}

// tmdbSearchHandler is a thin TMDB title-search proxy (mirrors
// discoverHandler's session/media-type handling) for Rename's manual
// override/re-pick workflow (see internal/api/proposals.go's
// repickProposalHandler) — the search box an operator uses to find the
// correct title when Scan's automatic match (confidence-scored or not, see
// internal/rename/confidence.go) picked wrong, or scored too low to
// auto-accept. Movies/Series only, enforced by an explicit mode check
// below — mode.Build's buildSearchPipeline populates sess.TMDB from the one
// global "tmdb" connection for EVERY mode, Adult included (unlike this
// handler's sibling repickProposalHandler, which has its own Movies/Series
// guard for a different reason — refusing to re-pick Adult's foreignId-based
// proposals), so relying on "sess.TMDB is nil for Adult" here would be false
// and let Adult calls return real-but-useless movie results.
func tmdbSearchHandler(httpClient *http.Client, connStore *connections.Store, scStore *serviceconn.Store, settingsStore *settings.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		m := mode.Mode(r.PathValue("mode"))
		if m != mode.Movies && m != mode.Series {
			http.Error(w, "tmdb-search is only supported for movies/series", http.StatusBadRequest)
			return
		}
		ctx := r.Context()
		query := r.URL.Query().Get("q")
		if query == "" {
			http.Error(w, "q query parameter is required", http.StatusBadRequest)
			return
		}

		sess, err := mode.Build(ctx, connStore, scStore, settingsStore, httpClient, nil, m)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if sess.TMDB == nil {
			http.Error(w, "tmdb isn't configured yet — add it in Settings first", http.StatusBadRequest)
			return
		}

		var items []tmdb.Item
		if m == mode.Series {
			items, err = sess.TMDB.SearchTV(ctx, query)
		} else {
			items, err = sess.TMDB.SearchMovies(ctx, query)
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(items)
	}
}

// tvdbSearchHandler is Rename SearchTakeover's TVDB-backed series search
// (GET /api/modes/series/tvdb-search). kind=series searches show names;
// kind=episode searches episode titles and returns slot numbers for one-click
// repick. Every hit is mapped to a TMDB id via FindTVByTVDBID so the existing
// repick/move endpoints stay unchanged.
func tvdbSearchHandler(httpClient *http.Client, connStore *connections.Store, scStore *serviceconn.Store, settingsStore *settings.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		m := mode.Mode(r.PathValue("mode"))
		if m != mode.Series {
			http.Error(w, "tvdb-search is only supported for series", http.StatusBadRequest)
			return
		}
		ctx := r.Context()
		query := r.URL.Query().Get("q")
		if query == "" {
			http.Error(w, "q query parameter is required", http.StatusBadRequest)
			return
		}
		kind := r.URL.Query().Get("kind")
		if kind == "" {
			kind = "series"
		}
		if kind != "series" && kind != "episode" {
			http.Error(w, "kind must be series or episode", http.StatusBadRequest)
			return
		}

		sess, err := mode.Build(ctx, connStore, scStore, settingsStore, httpClient, nil, m)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if sess.TVDB == nil {
			http.Error(w, "tvdb isn't configured yet — add it in Settings first", http.StatusBadRequest)
			return
		}
		if sess.TMDB == nil {
			http.Error(w, "tmdb isn't configured yet — add it in Settings first", http.StatusBadRequest)
			return
		}

		out := []apidto.SeriesSearchItem{}
		switch kind {
		case "series":
			results, err := sess.TVDB.SearchSeries(ctx, query)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadGateway)
				return
			}
			for _, res := range results {
				tmdbID, err := sess.TMDB.FindTVByTVDBID(ctx, res.TVDBID)
				if err != nil || tmdbID == 0 {
					continue
				}
				item := apidto.SeriesSearchItem{
					TmdbID: tmdbID,
					Title:  res.Name,
				}
				if res.Year > 0 {
					item.ReleaseDate = fmt.Sprintf("%d-01-01", res.Year)
				}
				out = append(out, item)
			}
		default:
			hits, err := sess.TVDB.SearchEpisodes(ctx, query)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadGateway)
				return
			}
			type episodeSeriesInfo struct {
				tmdbID int
				name   string
				year   int
			}
			seriesCache := make(map[int]episodeSeriesInfo)
			for _, hit := range hits {
				info, ok := seriesCache[hit.SeriesID]
				if !ok {
					tmdbID, err := sess.TMDB.FindTVByTVDBID(ctx, hit.SeriesID)
					if err != nil || tmdbID == 0 {
						continue
					}
					name, year, err := sess.TVDB.SeriesBrief(ctx, hit.SeriesID)
					if err != nil || name == "" {
						continue
					}
					info = episodeSeriesInfo{tmdbID: tmdbID, name: name, year: year}
					seriesCache[hit.SeriesID] = info
				}
				season := hit.SeasonNumber
				episode := hit.EpisodeNumber
				item := apidto.SeriesSearchItem{
					TmdbID:        info.tmdbID,
					Title:         hit.Name,
					SeriesTitle:   info.name,
					SeasonNumber:  &season,
					EpisodeNumber: &episode,
				}
				if info.year > 0 {
					item.ReleaseDate = fmt.Sprintf("%d-01-01", info.year)
				}
				out = append(out, item)
			}
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(out)
	}
}

// posterHandler resolves a Movies/Series library card's poster art lazily,
// per card, keyed by tmdbId. SAK's library caches TMDBID/Year but no poster
// path, so the existing-library row on Discover fetches each visible card's
// poster on demand (one bounded call per rendered card) rather than the list
// endpoint doing an unbounded N+1 lookup for the whole library up front,
// exactly the N+1 discoverHandler's own doc warns against. Movies/Series
// only — Adult scenes carry their own image inline from TPDB.
func posterHandler(httpClient *http.Client, connStore *connections.Store, scStore *serviceconn.Store, settingsStore *settings.Store) http.HandlerFunc {
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

		sess, err := mode.Build(ctx, connStore, scStore, settingsStore, httpClient, nil, m)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if sess.TMDB == nil {
			http.Error(w, "tmdb isn't configured yet — add it in Settings first", http.StatusBadRequest)
			return
		}

		var posterPath string
		if m == mode.Series {
			details, err := sess.TMDB.TVDetails(ctx, tmdbID)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadGateway)
				return
			}
			posterPath = details.PosterPath
		} else {
			details, err := sess.TMDB.MovieDetails(ctx, tmdbID)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadGateway)
				return
			}
			posterPath = details.PosterPath
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"posterPath": posterPath})
	}
}

// resolveTVDBIDHandler resolves a TMDB TV show id to its TVDB id — the one
// extra call needed before grabbing a Series title discovered via TMDB,
// since Sonarr's AddRequest wants a TVDB id, a different id space entirely
// (see internal/tmdb's package doc for why).
func resolveTVDBIDHandler(httpClient *http.Client, connStore *connections.Store, scStore *serviceconn.Store, settingsStore *settings.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		m := mode.Mode(r.PathValue("mode"))
		ctx := r.Context()
		tmdbID, err := strconv.Atoi(r.URL.Query().Get("tmdbId"))
		if err != nil {
			http.Error(w, "tmdbId query parameter is required and must be an integer", http.StatusBadRequest)
			return
		}

		sess, err := mode.Build(ctx, connStore, scStore, settingsStore, httpClient, nil, m)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if sess.TMDB == nil {
			http.Error(w, "tmdb isn't configured yet — add it in Settings first", http.StatusBadRequest)
			return
		}

		tvdbID, err := sess.TMDB.ExternalIDs(ctx, tmdbID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]int{"tvdbId": tvdbID})
	}
}
