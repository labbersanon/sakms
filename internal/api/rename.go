package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/curtiswtaylorjr/tidyarr/internal/connections"
	"github.com/curtiswtaylorjr/tidyarr/internal/mode"
	"github.com/curtiswtaylorjr/tidyarr/internal/proposals"
	"github.com/curtiswtaylorjr/tidyarr/internal/rename"
)

// scanHandler runs the Rename workflow's propose-phase for {mode} and
// replaces that mode's live review queue with the result — the HTTP
// equivalent of the top bar's Scan button.
func scanHandler(httpClient *http.Client, connStore *connections.Store, propStore *proposals.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		m := mode.Mode(r.PathValue("mode"))
		ctx := r.Context()

		sess, err := mode.Build(ctx, connStore, httpClient, m)
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

// listProposalsHandler returns the Rename review queue for {mode}, most
// recently scanned first — includes Applied/Dismissed history alongside the
// live Pending/Unmatched rows, since the queue is also today's simplest
// stand-in for an audit trail.
func listProposalsHandler(propStore *proposals.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		m := mode.Mode(r.PathValue("mode"))
		list, err := propStore.List(r.Context(), m, proposals.Rename)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(list)
	}
}

// applyProposalHandler is the only place in Tidyarr's API that actually
// registers a new item with a *arr app — and only for the one proposal ID in
// the URL, never a batch, matching the design's staged-for-approval
// principle: a Scan proposes, a human picks, Apply commits exactly that.
func applyProposalHandler(httpClient *http.Client, connStore *connections.Store, propStore *proposals.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := parseProposalID(w, r)
		if !ok {
			return
		}
		ctx := r.Context()

		p, err := propStore.Get(ctx, id)
		if err != nil {
			proposalNotFoundOr500(w, err)
			return
		}

		sess, err := mode.Build(ctx, connStore, httpClient, p.Mode)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		trackedID, applyErr := rename.Apply(ctx, sess, *p)
		if trackedID != 0 {
			// Registered even if the follow-up scan trigger below failed —
			// see rename.Apply's doc comment. Record it as applied either way
			// so the queue doesn't lose track of an item that's now real.
			if err := propStore.MarkApplied(ctx, id, trackedID); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		}
		if applyErr != nil {
			http.Error(w, applyErr.Error(), http.StatusBadGateway)
			return
		}

		updated, err := propStore.Get(ctx, id)
		if err != nil {
			proposalNotFoundOr500(w, err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(updated)
	}
}

// dismissProposalHandler marks one proposal reviewed-and-rejected, dropping
// it out of the live queue without acting on it.
func dismissProposalHandler(propStore *proposals.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := parseProposalID(w, r)
		if !ok {
			return
		}
		if err := propStore.Dismiss(r.Context(), id); err != nil {
			proposalNotFoundOr500(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func parseProposalID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid proposal id", http.StatusBadRequest)
		return 0, false
	}
	return id, true
}

func proposalNotFoundOr500(w http.ResponseWriter, err error) {
	if errors.Is(err, proposals.ErrNotFound) {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	http.Error(w, err.Error(), http.StatusInternalServerError)
}
