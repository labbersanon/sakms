package api

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/labbersanon/sakms/internal/allowlist"
	"github.com/labbersanon/sakms/internal/connections"
	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/proposals"
	"github.com/labbersanon/sakms/internal/purge"
	"github.com/labbersanon/sakms/internal/settings"
)

// purgeScanHandler runs the Purge workflow's propose-phase for {mode}:
// fetches that mode's current allowlist, matches it against every tracked
// item's tags, and replaces the live Purge queue with whatever matched.
// Every mode dispatches to a library-backed sibling now (libStore, no *arr
// app involved): Movies/Series to purge.ScanLibrary/ScanLibrarySeries, Adult
// to purge.ScanLibraryAdult (Whisparr eliminated, Stage 4). connStore/
// settingsStore/httpClient are retained on the signature (NewMux wires them)
// but no longer used here, since no mode builds a Servarr session.
func purgeScanHandler(httpClient *http.Client, connStore *connections.Store, settingsStore *settings.Store, propStore *proposals.Store, allowStore *allowlist.Store, libStore *library.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		m := mode.Mode(r.PathValue("mode"))
		ctx := r.Context()

		rules, err := allowStore.List(ctx, m)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		var found []proposals.Proposal
		switch m {
		case mode.Movies:
			found, err = purge.ScanLibrary(ctx, libStore, rules)
		case mode.Series:
			found, err = purge.ScanLibrarySeries(ctx, libStore, rules)
		case mode.Adult:
			// Adult owns its own library now too (Whisparr eliminated, Stage 4)
			// — served straight from libStore, no *arr app to ask.
			found, err = purge.ScanLibraryAdult(ctx, libStore, rules)
		default:
			http.Error(w, fmt.Sprintf("unknown mode %q", m), http.StatusBadRequest)
			return
		}
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
