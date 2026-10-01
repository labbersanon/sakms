package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/labbersanon/sakms/internal/apidto"
	"github.com/labbersanon/sakms/internal/connections"
	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/purge"
	"github.com/labbersanon/sakms/internal/sectionlock"
	"github.com/labbersanon/sakms/internal/serviceconn"
	"github.com/labbersanon/sakms/internal/settings"
)

// deleteTrackedHandler is DELETE /api/modes/{mode}/tracked/{id}.
// Claude 2026-10-01: owned DetailPopup remove-from-library.
// Reason: operator-confirmed permanent file+row delete, same disk contract as Purge.
// Troubleshooting: nested Modal would close both overlays on one backdrop click.
// Review if: Purge Apply becomes a wrapper around purge.RemoveOwned.
func deleteTrackedHandler(httpClient *http.Client, connStore *connections.Store, scStore *serviceconn.Store, settingsStore *settings.Store, libStore *library.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		m := mode.Mode(r.PathValue("mode"))
		switch m {
		case mode.Movies, mode.Series, mode.Adult:
		default:
			http.Error(w, "unknown mode", http.StatusBadRequest)
			return
		}
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil || id < 1 {
			http.Error(w, "invalid tracked id", http.StatusBadRequest)
			return
		}
		var req apidto.RemoveTrackedRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
			http.Error(w, "invalid remove body", http.StatusBadRequest)
			return
		}

		ctx := r.Context()
		sess, err := mode.Build(ctx, connStore, scStore, settingsStore, httpClient, nil, m)
		if err != nil {
			if errors.Is(err, sectionlock.ErrSectionLocked) {
				writeSectionLocked(w, sectionlock.SectionAdultContent)
				return
			}
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		changes, gone, err := purge.RemoveOwned(ctx, libStore, m, id, purge.RemoveSpec{
			EntireSeries: req.EntireSeries,
			Seasons:      req.Seasons,
		})
		sess.NotifyPlayers(ctx, changes)
		if err != nil {
			switch {
			case errors.Is(err, library.ErrNotFound):
				http.Error(w, err.Error(), http.StatusNotFound)
			case errors.Is(err, purge.ErrRemoveSpec):
				http.Error(w, err.Error(), http.StatusBadRequest)
			default:
				http.Error(w, err.Error(), http.StatusInternalServerError)
			}
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(apidto.RemoveTrackedResponse{Gone: gone})
	}
}
