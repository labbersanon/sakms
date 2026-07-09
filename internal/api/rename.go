package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/curtiswtaylorjr/sak/internal/connections"
	"github.com/curtiswtaylorjr/sak/internal/mode"
	"github.com/curtiswtaylorjr/sak/internal/proposals"
	"github.com/curtiswtaylorjr/sak/internal/rename"
	"github.com/curtiswtaylorjr/sak/internal/settings"
)

type kidsRootPathResponse struct {
	Path string `json:"path"`
}

type kidsRootPathRequest struct {
	Path string `json:"path"`
}

// getKidsRootPathHandler returns {mode}'s configured Kids root folder path,
// or an empty string if unset (a normal state — the feature is off for that
// mode). 400s for Adult, which has no kids/general split concept.
func getKidsRootPathHandler(settingsStore *settings.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key, ok := mode.Mode(r.PathValue("mode")).KidsRootPathKey()
		if !ok {
			http.Error(w, "kids root path isn't applicable to this mode", http.StatusBadRequest)
			return
		}
		path, err := settingsStore.Get(r.Context(), key)
		if err != nil && !errors.Is(err, settings.ErrNotFound) {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(kidsRootPathResponse{Path: path})
	}
}

// putKidsRootPathHandler stores {mode}'s Kids root folder path. An empty
// path is accepted (turns the feature back off) — unlike the AI model
// setting, "off" is a perfectly normal, common choice here, not a mistake to
// reject.
func putKidsRootPathHandler(settingsStore *settings.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key, ok := mode.Mode(r.PathValue("mode")).KidsRootPathKey()
		if !ok {
			http.Error(w, "kids root path isn't applicable to this mode", http.StatusBadRequest)
			return
		}
		var req kidsRootPathRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		if err := settingsStore.Set(r.Context(), key, req.Path); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// renameScanHandler runs the Rename workflow's propose-phase for {mode} and
// replaces that mode's live Rename queue with the result — the HTTP
// equivalent of the top bar's Scan button.
func renameScanHandler(httpClient *http.Client, connStore *connections.Store, settingsStore *settings.Store, propStore *proposals.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		m := mode.Mode(r.PathValue("mode"))
		ctx := r.Context()

		sess, err := mode.Build(ctx, connStore, settingsStore, httpClient, m)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		found, err := rename.Scan(ctx, sess)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}

		saved, err := propStore.ReplacePending(ctx, m, proposals.Rename, found)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(saved)
	}
}
