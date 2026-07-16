package api

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/curtiswtaylorjr/sakms/internal/apidto"
	"github.com/curtiswtaylorjr/sakms/internal/connections"
	"github.com/curtiswtaylorjr/sakms/internal/mode"
	"github.com/curtiswtaylorjr/sakms/internal/settings"
)

// discoverTrailerHandler backs GET /api/modes/{mode}/discover/trailer?tmdbId=N
// — the Discover detail popup's "Watch Trailer" link resolver. Movies/Series
// only (mirrors discoverHandler's mediaTypeForMode dispatch); Adult has no
// TMDB id to resolve a trailer from, so it 400s here rather than silently
// returning nothing. Fires once per explicit detail-popup open — the same
// user-click trigger shape as discoverAvailabilityHandler, not a per-card
// automatic fetch (see CLAUDE.md's "Discover never queries Prowlarr" note;
// this endpoint doesn't touch Prowlarr at all, but the same one-shot-per-
// click discipline applies to every Discover-popup upstream call). A title
// with no trailer on file returns {url: ""} (200), not an error — the popup
// simply omits the link.
func discoverTrailerHandler(httpClient *http.Client, connStore *connections.Store, settingsStore *settings.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		m := mode.Mode(r.PathValue("mode"))
		if m != mode.Movies && m != mode.Series {
			http.Error(w, "trailer lookup is only supported for movies/series", http.StatusBadRequest)
			return
		}
		ctx := r.Context()
		tmdbID, err := strconv.Atoi(r.URL.Query().Get("tmdbId"))
		if err != nil || tmdbID <= 0 {
			http.Error(w, "tmdbId query parameter is required and must be a positive integer", http.StatusBadRequest)
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

		trailerURL, err := sess.TMDB.TrailerURL(ctx, mediaTypeForMode(m), tmdbID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(apidto.TrailerResponse{URL: trailerURL})
	}
}
