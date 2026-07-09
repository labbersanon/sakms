package api

import (
	"encoding/json"
	"net/http"

	"github.com/curtiswtaylorjr/sak/internal/allowlist"
	"github.com/curtiswtaylorjr/sak/internal/connections"
	"github.com/curtiswtaylorjr/sak/internal/mode"
	"github.com/curtiswtaylorjr/sak/internal/proposals"
	"github.com/curtiswtaylorjr/sak/internal/purge"
	"github.com/curtiswtaylorjr/sak/internal/settings"
)

// purgeScanHandler runs the Purge workflow's propose-phase for {mode}:
// fetches that mode's current allowlist, matches it against every tracked
// item's tags, and replaces the live Purge queue with whatever matched.
func purgeScanHandler(httpClient *http.Client, connStore *connections.Store, settingsStore *settings.Store, propStore *proposals.Store, allowStore *allowlist.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		m := mode.Mode(r.PathValue("mode"))
		ctx := r.Context()

		sess, err := mode.Build(ctx, connStore, settingsStore, httpClient, m)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		rules, err := allowStore.List(ctx, m)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		found, err := purge.Scan(ctx, sess, rules)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}

		saved, err := propStore.ReplacePending(ctx, m, proposals.Purge, found)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(saved)
	}
}

// listAllowlistHandler returns {mode}'s current Purge allowlist.
func listAllowlistHandler(allowStore *allowlist.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		m := mode.Mode(r.PathValue("mode"))
		tags, err := allowStore.List(r.Context(), m)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(tags)
	}
}

type addAllowlistTagRequest struct {
	Tag string `json:"tag"`
}

// addAllowlistTagHandler adds one tag rule to {mode}'s allowlist. Adding a
// tag already present is not an error — see allowlist.Store.Add.
func addAllowlistTagHandler(allowStore *allowlist.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		m := mode.Mode(r.PathValue("mode"))
		var req addAllowlistTagRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		if req.Tag == "" {
			http.Error(w, "tag is required", http.StatusBadRequest)
			return
		}
		if err := allowStore.Add(r.Context(), m, req.Tag); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// removeAllowlistTagHandler removes one tag rule from {mode}'s allowlist.
func removeAllowlistTagHandler(allowStore *allowlist.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		m := mode.Mode(r.PathValue("mode"))
		tag := r.PathValue("tag")
		if err := allowStore.Remove(r.Context(), m, tag); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}
