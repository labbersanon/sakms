package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/labbersanon/sakms/internal/connections"
	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/proposals"
	"github.com/labbersanon/sakms/internal/pruning"
	"github.com/labbersanon/sakms/internal/purge"
	"github.com/labbersanon/sakms/internal/settings"
)

// purgeScanHandler runs the Purge workflow's propose-phase for {mode}: loads
// that mode's enabled pruning rules, evaluates every tracked item against
// them, and replaces the live Purge queue with whatever matched.
// Every mode dispatches to a library-backed sibling now (libStore, no *arr
// app involved): Movies/Series to purge.ScanLibrary/ScanLibrarySeries, Adult
// to purge.ScanLibraryAdult (Whisparr eliminated, Stage 4). connStore/
// settingsStore/httpClient are retained on the signature (NewMux wires them)
// but no longer used here, since no mode builds a Servarr session.
//
// Claude 2026-08-11: the global per-mode tag allowlist load is gone; rules are
// now the single matching mechanism, with tags a fourth AND'd per-rule
// condition (plan
// .omc/plans/autopilot-impl-purge-rules-consolidation-cleanup-rename.md §5.1).
// Reason: the rules are loaded HERE and passed down as a parameter so
// cmd/sakms/scanadapter.go's scheduled ScanPurge runs the byte-identical
// propose logic without needing a new exported purge symbol (which
// internal/scanschedule's allowlist test would reject — see internal/purge's
// own header comment).
// Troubleshooting: pruningStore MAY BE NIL in tests that don't exercise rules;
// a nil store means "no rules," not a panic — and with the allowlist retired,
// no rules now means no proposals at all.
// Review if: Purge ever gains a second matching mechanism alongside rules.
func purgeScanHandler(httpClient *http.Client, connStore *connections.Store, settingsStore *settings.Store, propStore *proposals.Store, libStore *library.Store, pruningStore *pruning.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		m := mode.Mode(r.PathValue("mode"))
		ctx := r.Context()

		aspect, aerr := parseAdultAspectQuery(r)
		if aerr != nil {
			http.Error(w, aerr.Error(), http.StatusBadRequest)
			return
		}

		var rules []pruning.Rule
		var err error
		if pruningStore != nil {
			rules, err = pruningStore.ListEnabledForMode(ctx, m)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
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
			found, err = purge.ScanLibraryAdult(ctx, libStore, rules, aspect)
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
			if errors.Is(err, proposals.ErrReplaceDeferred) {
				http.Error(w, "scan deferred: an apply is currently in flight — retry after the apply completes", http.StatusServiceUnavailable)
			} else {
				http.Error(w, err.Error(), http.StatusInternalServerError)
			}
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(saved)
	}
}
