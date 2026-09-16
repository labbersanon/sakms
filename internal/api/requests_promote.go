package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/labbersanon/sakms/internal/apidto"
	"github.com/labbersanon/sakms/internal/connections"
	"github.com/labbersanon/sakms/internal/grabs"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/tmdb"
)

// promoteRequestReason is operator-facing copy only. Nothing branches on it;
// DueForRetry keys off retry_after / hold_until.
const promoteRequestReason = "operator promoted to top of schedule"

// promoteRequestHandler backs POST /api/requests/promote — move a Pending /
// Pending Retry / Scheduled grab to the front of the retry schedule. See
// grabs.PromoteToFront for what it deliberately leaves alone.
//
// Claude 2026-09-16: added movie-release gate.
// Reason: PromoteToFront clears hold_until for held rows, which could let a
//   theatrical-only film through to the next retry cycle. The gate here gives
//   the operator an immediate 409 rather than a silent promote followed by a
//   gate-block one cycle later.
// Troubleshooting: 409 on promote → check TMDB release_dates for the film.
// Review if: the gate's call-site list changes.
func promoteRequestHandler(grabsStore *grabs.Store, connStore *connections.Store, httpClient *http.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		var req apidto.PromoteRequestRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		if req.GrabID <= 0 {
			http.Error(w, "grabId is required", http.StatusBadRequest)
			return
		}

		// Fetch the grab to get mode and TMDB id for the gate.
		g, err := grabsStore.Get(ctx, req.GrabID)
		if err != nil {
			if errors.Is(err, grabs.ErrNotFound) {
				http.Error(w, "grab not found or not eligible to promote", http.StatusNotFound)
				return
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		// Run the movie-release gate before promoting. Build a TMDB client only
		// for Movies rows with a non-zero TMDB id (the gate short-circuits for
		// all other cases, so the connStore lookup is skipped when not needed).
		if g.Mode == mode.Movies && g.TMDBID > 0 {
			var tmdbClient *tmdb.Client
			if connStore != nil && httpClient != nil {
				if conn, err := connStore.Get(ctx, "tmdb"); err == nil {
					tmdbClient = tmdb.New(tmdb.Config{BaseURL: tmdb.DefaultBaseURL, APIKey: conn.APIKey}, httpClient)
				}
			}
			if _, blocked, reason := gateMovieGrab(ctx, tmdbClient, g.Mode, g.TMDBID); blocked {
				http.Error(w, reason, http.StatusConflict)
				return
			}
		}

		if err := grabsStore.PromoteToFront(ctx, req.GrabID, time.Now(), promoteRequestReason); err != nil {
			if errors.Is(err, grabs.ErrNotFound) {
				http.Error(w, "grab not found or not eligible to promote", http.StatusNotFound)
				return
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}
