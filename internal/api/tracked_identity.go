package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/labbersanon/sakms/internal/apidto"
	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mode"
)

// putTrackedIdentityHandler is PUT /api/modes/{mode}/tracked/{id}/identity.
// Claude 2026-09-24: owned Rematch. {id} is the library row (series/item/scene).
// Reason: SearchTakeover must update catalog ids/title without a Rename proposal.
// Troubleshooting: 409 when another row already owns that catalog identity.
func putTrackedIdentityHandler(libStore *library.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		m := mode.Mode(r.PathValue("mode"))
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil || id < 1 {
			http.Error(w, "invalid tracked id", http.StatusBadRequest)
			return
		}
		var req apidto.TrackedIdentityRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid identity body", http.StatusBadRequest)
			return
		}
		title := strings.TrimSpace(req.Title)
		if title == "" {
			http.Error(w, "title is required", http.StatusBadRequest)
			return
		}
		ctx := r.Context()
		switch m {
		case mode.Movies:
			if req.TmdbId <= 0 {
				http.Error(w, "tmdbId is required", http.StatusBadRequest)
				return
			}
			err = libStore.RematchMovie(ctx, id, req.TmdbId, title, req.Year)
		case mode.Series:
			if req.TmdbId <= 0 {
				http.Error(w, "tmdbId is required", http.StatusBadRequest)
				return
			}
			err = libStore.RematchSeries(ctx, id, req.TmdbId, title, req.Year)
		case mode.Adult:
			if strings.TrimSpace(req.Box) == "" || strings.TrimSpace(req.SceneID) == "" {
				http.Error(w, "box and sceneId are required", http.StatusBadRequest)
				return
			}
			err = libStore.RematchScene(ctx, id, req.Box, req.SceneID, title, req.Studio, req.Date)
		default:
			http.Error(w, "unknown mode", http.StatusBadRequest)
			return
		}
		if err == nil {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		switch {
		case errors.Is(err, library.ErrNotFound):
			http.Error(w, err.Error(), http.StatusNotFound)
		case errors.Is(err, library.ErrIdentityConflict):
			http.Error(w, err.Error(), http.StatusConflict)
		default:
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	}
}
