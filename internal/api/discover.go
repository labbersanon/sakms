package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"github.com/curtiswtaylorjr/sakms/internal/connections"
	"github.com/curtiswtaylorjr/sakms/internal/mode"
	"github.com/curtiswtaylorjr/sakms/internal/settings"
	"github.com/curtiswtaylorjr/sakms/internal/tmdb"
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

// discoverHandler returns TMDB's trending or popular titles for {mode}'s
// media type — a read-only proxy+normalize (like listRootFoldersHandler),
// nothing staged or persisted. Series items carry only their TMDB id here;
// resolving the TVDB id Sonarr's AddRequest actually needs is deferred to
// resolveTVDBIDHandler, called only once a user picks a specific title to
// search+grab — not eagerly for every item in a trending list, which would
// multiply this one TMDB call into one-plus-N for results nobody clicks.
func discoverHandler(httpClient *http.Client, connStore *connections.Store, settingsStore *settings.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		m := mode.Mode(r.PathValue("mode"))
		ctx := r.Context()
		category := r.URL.Query().Get("category")
		if category == "" {
			category = "trending"
		}

		sess, err := mode.Build(ctx, connStore, settingsStore, httpClient, m)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if sess.TMDB == nil {
			http.Error(w, "tmdb isn't configured yet — add it in Settings first", http.StatusBadRequest)
			return
		}

		mt := mediaTypeForMode(m)
		var items []tmdb.Item
		switch category {
		case "trending":
			items, err = sess.TMDB.Trending(ctx, mt, "week")
		case "popular":
			items, err = sess.TMDB.Popular(ctx, mt)
		default:
			http.Error(w, fmt.Sprintf("unrecognized category %q", category), http.StatusBadRequest)
			return
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(items)
	}
}

// resolveTVDBIDHandler resolves a TMDB TV show id to its TVDB id — the one
// extra call needed before grabbing a Series title discovered via TMDB,
// since Sonarr's AddRequest wants a TVDB id, a different id space entirely
// (see internal/tmdb's package doc for why).
func resolveTVDBIDHandler(httpClient *http.Client, connStore *connections.Store, settingsStore *settings.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		m := mode.Mode(r.PathValue("mode"))
		ctx := r.Context()
		tmdbID, err := strconv.Atoi(r.URL.Query().Get("tmdbId"))
		if err != nil {
			http.Error(w, "tmdbId query parameter is required and must be an integer", http.StatusBadRequest)
			return
		}

		sess, err := mode.Build(ctx, connStore, settingsStore, httpClient, m)
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
