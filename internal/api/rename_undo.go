package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/labbersanon/sakms/internal/apidto"
	"github.com/labbersanon/sakms/internal/connections"
	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/proposals"
	"github.com/labbersanon/sakms/internal/rename"
	"github.com/labbersanon/sakms/internal/sectionlock"
	"github.com/labbersanon/sakms/internal/serviceconn"
	"github.com/labbersanon/sakms/internal/settings"
)

// Rename Undo — HTTP surface (deep-interview-rename-undo, backend pass).
//
// Its own file rather than another handler in proposals.go, following the
// move-mode / delete-batch precedent: proposals.go is already ~1000 lines and
// this repo splits proposal-scoped features out once they have their own store,
// their own reversal logic, and their own tests.
//
// The undo store is resolved from rename.DefaultUndoStore() at REQUEST time
// rather than passed into NewMux. NewMux has ~283 call sites and carries no
// *sql.DB, and this store is DB-backed — the same "build it from what is
// already wired rather than growing NewMux's public parameter list" reasoning
// NewMux's own discoverRefreshDeps block records. main.go registers it at boot;
// an instance that never did answers 503 rather than silently doing nothing.

// undoProposalHandler reverses the most recent still-undoable Apply of one
// proposal: moves the file back (subject to the size-match gate in
// rename.UndoApply), reverts the library row(s) that Apply created or
// overwrote, and restores the proposal to its pre-Apply state.
func undoProposalHandler(httpClient *http.Client, connStore *connections.Store, scStore *serviceconn.Store, settingsStore *settings.Store, propStore *proposals.Store, libStore *library.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := parseProposalID(w, r)
		if !ok {
			return
		}
		ctx := r.Context()

		undoStore := rename.DefaultUndoStore()
		if undoStore == nil {
			http.Error(w, "undo is not available on this instance", http.StatusServiceUnavailable)
			return
		}

		p, err := propStore.Get(ctx, id)
		if err != nil {
			proposalNotFoundOr500(w, err)
			return
		}
		if p.Workflow != proposals.Rename {
			http.Error(w, fmt.Sprintf("proposal %d is a %q proposal — only rename proposals can be undone", id, p.Workflow), http.StatusBadRequest)
			return
		}
		if p.Status != proposals.Applied {
			http.Error(w, fmt.Sprintf("proposal %d is %q — only applied proposals can be undone", id, p.Status), http.StatusBadRequest)
			return
		}

		// Same apply-gate discipline as applyProposalHandler, and for the same
		// reason: undo mutates the row a concurrent ReplacePending would
		// otherwise be free to delete mid-flight.
		proposals.DefaultGate.BeginApply(p.Mode, p.Workflow)
		defer proposals.DefaultGate.EndApply(p.Mode, p.Workflow)

		// Built for sess.Players only (the move-back has to reach Jellyfin/
		// Stash), and it doubles as Layer 2 of the section lock for an Adult
		// proposal — a mode this URL cannot express, so Layer 1 never saw it.
		sess, err := mode.Build(ctx, connStore, scStore, settingsStore, httpClient, nil, p.Mode)
		if err != nil {
			if errors.Is(err, sectionlock.ErrSectionLocked) {
				writeSectionLocked(w, sectionlock.SectionAdultContent)
				return
			}
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		// An EVICTED entry is filtered out by LatestActiveFor exactly as a
		// consumed or never-archived one is, so it reports "nothing to undo".
		// That is the deterministic, intended outcome of the rolling depth —
		// an evicted Apply is no longer undoable, and saying so is correct.
		entry, err := undoStore.LatestActiveFor(ctx, id)
		if err != nil {
			if errors.Is(err, rename.ErrNoUndoEntry) {
				http.Error(w, fmt.Sprintf("nothing to undo for proposal %d", id), http.StatusNotFound)
				return
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		// Pre-condition, checked BEFORE anything is moved or written. Migration
		// 0004 puts a PARTIAL UNIQUE INDEX on (mode, workflow, source_path)
		// WHERE status IN ('pending','unmatched'), and restoring this proposal
		// puts it back to Pending at its original source path. If a later Scan
		// has since minted a live row at that same path (a re-download landing
		// in the same inbox under the same name), the restore UPDATE would
		// violate that index — and it happens at step 4, AFTER the file has
		// already been moved back, leaving a half-applied undo behind a 500.
		// Refusing up front leaves everything exactly as it was.
		if conflict, cerr := propStore.GetLiveBySourcePath(ctx, p.Mode, p.Workflow, entry.PreApplySourcePath); cerr == nil && conflict != nil && conflict.ID != p.ID {
			http.Error(w, fmt.Sprintf(
				"proposal %d is already queued for %q — undo would put two live proposals at the same source path; dismiss or apply that one first",
				conflict.ID, entry.PreApplySourcePath), http.StatusConflict)
			return
		} else if cerr != nil && !errors.Is(cerr, proposals.ErrNotFound) {
			http.Error(w, cerr.Error(), http.StatusInternalServerError)
			return
		}

		result, undoErr := rename.UndoApply(ctx, undoStore, libStore, propStore, entry)
		// The INVERSE of the Apply's own change set. Notified BEFORE the error
		// check, matching applyProposalHandler's partial-success rule: a
		// move-back that physically committed must reach the players even if a
		// later step failed, or their index stays pointed at a path that no
		// longer exists. NotifyPlayers no-ops on an empty set, which is exactly
		// what an alternate-fold or missing-file undo produces.
		sess.NotifyPlayers(ctx, result.Changes)
		if undoErr != nil {
			http.Error(w, undoErr.Error(), http.StatusInternalServerError)
			return
		}

		writeJSON(w, result)
	}
}

// recentlyAppliedHandler lists one mode's still-undoable Applies, newest first
// — the read half of this feature, backing the Rename screen's "Recently
// Applied" section.
//
// It answers 200 with `[]` in EVERY non-error case, including the two that look
// like they want a status code: undo unavailable on this instance (no store
// registered) and nothing undoable yet. This is a list view, not a
// single-resource lookup — an empty list is a normal, valid state, and a 503/404
// here would render an error banner on a perfectly healthy install that simply
// has not applied anything. The MUTATION handler above still answers 503/404 for
// those same two conditions, deliberately: there the operator asked for one
// specific thing that cannot be done, which is genuinely a failure.
//
// The response is a lean apidto.RecentlyAppliedEntry, never the raw UndoEntry —
// see that type's doc for why the snapshot blobs must not reach the client.
//
// TWO DEVIATIONS from the implementation plan's §1, recorded rather than left
// implicit: (1) the plan's signature was
// `recentlyAppliedHandler(undoStoreFn func() *rename.UndoStore)`; the store is
// resolved from rename.DefaultUndoStore() inside instead, matching
// undoProposalHandler above exactly (see this file's doc for why that pattern
// was chosen) so the feature's two halves resolve it one way, not two. (2) the
// response DTO is an EXPORTED apidto type rather than the plan's local
// unexported struct — see apidto.RecentlyAppliedEntry's own doc for why.
func recentlyAppliedHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		m := mode.Mode(r.PathValue("mode"))
		out := []apidto.RecentlyAppliedEntry{}

		undoStore := rename.DefaultUndoStore()
		if undoStore == nil {
			writeJSON(w, out)
			return
		}
		entries, err := undoStore.ListActive(r.Context(), m)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		for _, e := range entries {
			row := apidto.RecentlyAppliedEntry{
				UndoID:           e.ID,
				ProposalID:       e.ProposalID,
				Mode:             string(e.Mode),
				AppliedAt:        e.AppliedAt,
				ViaAlternateFold: e.ViaAlternateFold,
			}
			// A snapshot that fails to decode degrades to an id-only row rather
			// than dropping the entry or failing the whole list: the entry IS
			// still undoable, and hiding it would hide a working Undo button.
			var snap proposals.Proposal
			if uerr := json.Unmarshal([]byte(e.PreApplyProposalSnapshot), &snap); uerr == nil {
				row.SourceName = snap.SourceName
				row.Title = snap.Title
			}
			out = append(out, row)
		}
		writeJSON(w, out)
	}
}
