package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"github.com/curtiswtaylorjr/sakms/internal/connections"
	"github.com/curtiswtaylorjr/sakms/internal/dedup"
	"github.com/curtiswtaylorjr/sakms/internal/library"
	"github.com/curtiswtaylorjr/sakms/internal/mode"
	"github.com/curtiswtaylorjr/sakms/internal/proposals"
	"github.com/curtiswtaylorjr/sakms/internal/purge"
	"github.com/curtiswtaylorjr/sakms/internal/rename"
	"github.com/curtiswtaylorjr/sakms/internal/settings"
	"github.com/curtiswtaylorjr/sakms/internal/webhooks"
)

// listProposalsHandler returns {mode}'s review queue for wf, most recently
// scanned first — includes Applied/Dismissed history alongside the live
// Pending/Unmatched rows, since the queue is also today's simplest stand-in
// for an audit trail. Shared by every workflow (Rename, Purge, and whatever
// comes next) — listing a queue never needs workflow-specific logic, only
// Scan and Apply do.
func listProposalsHandler(propStore *proposals.Store, wf proposals.Workflow) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		m := mode.Mode(r.PathValue("mode"))
		list, err := propStore.List(r.Context(), m, wf)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(list)
	}
}

// applyProposalRequest is only meaningful for Dedup — a duplicate group's
// resolution isn't fully decided at Scan time the way Rename's and Purge's
// are, so Apply needs to know which candidate to keep. Rename and Purge
// ignore it entirely (an empty or missing body is the normal case for
// those). KeepIndex nil means "auto" — whichever candidate Scan already
// marked as the quality winner.
type applyProposalRequest struct {
	KeepIndex *int `json:"keepIndex,omitempty"`
	KeepAll   bool `json:"keepAll,omitempty"`
}

// proposalApplyStore is the subset of *proposals.Store the apply paths touch —
// the two lookups the handlers need plus the two "record the outcome" writes
// applyByWorkflow performs. It exists only as a test seam: there is no natural
// way to make a real store's post-commit MarkApplied write fail while the
// physical move/delete already succeeded (SQLite won't fail an UPDATE of an
// existing row), yet that exact partial-success case — committed file change +
// failed DB write — is what the batch handler's unconditional change
// accumulation must handle. A test wraps a real *proposals.Store and overrides
// one method to exercise it. *proposals.Store satisfies this interface, so
// NewMux's wiring is unchanged.
type proposalApplyStore interface {
	Get(ctx context.Context, id int64) (*proposals.Proposal, error)
	MarkApplied(ctx context.Context, id int64, trackedID int) error
	MarkFingerprintSubmitted(ctx context.Context, id int64) error
}

// applyProposalHandler commits exactly the one proposal ID in the URL, never
// a batch, matching the design's staged-for-approval principle: a Scan
// proposes, a human picks, Apply commits exactly that. The proposal's own
// Workflow field (set at Scan time) decides which package's Apply actually
// runs — the URL doesn't need to say which, since a proposal ID alone is
// already unambiguous. No mode touches a *arr app anymore; every Apply is
// library-backed (see applyByWorkflow).
func applyProposalHandler(httpClient *http.Client, connStore *connections.Store, settingsStore *settings.Store, propStore *proposals.Store, libStore *library.Store, whStore *webhooks.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := parseProposalID(w, r)
		if !ok {
			return
		}
		ctx := r.Context()

		var req applyProposalRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}

		p, err := propStore.Get(ctx, id)
		if err != nil {
			proposalNotFoundOr500(w, err)
			return
		}

		// No proposal needs a Servarr session anymore (every Apply path is
		// libStore-backed — see applyByWorkflow), but mode.Build is still
		// needed for sess.Identify/sess.Stash (Adult give-back + player-rescan
		// notify) and sess.Jellyfin (Movies/Series notify); it leaves
		// sess.Servarr nil for every mode now.
		sess, err := mode.Build(ctx, connStore, settingsStore, httpClient, nil, p.Mode)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		// applyByWorkflow no longer notifies internally — it returns whatever
		// it committed so the two call sites can choose when to fire
		// NotifyPlayers (per-item here; once after the whole loop in the batch
		// handler). The notify happens even when err is non-nil, preserving the
		// partial-success rule the old internal defer had: a committed file move
		// still reaches the players even if a later step (e.g. MarkApplied)
		// failed. NotifyPlayers no-ops on empty changes.
		changes, err := applyByWorkflow(ctx, settingsStore, propStore, libStore, sess, *p, req, true)
		sess.NotifyPlayers(ctx, changes)
		if err != nil {
			if errors.Is(err, errUnknownWorkflow) {
				http.Error(w, err.Error(), http.StatusInternalServerError)
			} else {
				http.Error(w, err.Error(), http.StatusBadGateway)
			}
			return
		}

		whStore.Dispatch(workflowEvent(p.Workflow), map[string]any{
			"mode": string(p.Mode), "workflow": string(p.Workflow),
			"title": p.Title, "tmdbId": p.TMDBID,
		})

		updated, err := propStore.Get(ctx, id)
		if err != nil {
			proposalNotFoundOr500(w, err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(updated)
	}
}

// workflowEvent maps a proposal workflow to the corresponding webhook event name.
func workflowEvent(wf proposals.Workflow) string {
	switch wf {
	case proposals.Rename:
		return webhooks.EventRenameApplied
	case proposals.Purge:
		return webhooks.EventPurgeApplied
	case proposals.Dedup:
		return webhooks.EventDedupApplied
	default:
		return string(wf) + ".applied"
	}
}

var errUnknownWorkflow = errors.New("unknown proposal workflow")

// applyByWorkflow dispatches to the right package's Apply and records the
// outcome. Every mode routes each workflow to its libStore-backed *Library
// counterpart now (no *arr app involved): Movies to *Library, Series to
// *LibrarySeries, Adult to *LibraryAdult (Whisparr eliminated, Stage 4). The
// three workflows have different success shapes — Rename relocates+records
// (and Adult's variant additionally reports a best-effort fingerprint
// give-back); Purge's delete either fully succeeds or fully fails; Dedup's
// Apply returns the resulting tracked id the same way Rename's does — so each
// branch marks the queue accordingly rather than forcing all three through
// one shared success rule. It returns whatever file moves/deletes it
// committed to disk (changes) alongside the error, so the caller can feed
// those to sess.NotifyPlayers even when err is non-nil (partial-success rule
// — a committed move still reaches the players; see each Apply function's doc
// comment). It deliberately does NOT notify itself: the single-item handler
// notifies per call, while the batch handler suppresses the per-item notify
// and fires one combined call after its whole loop. changes is nil on an
// early error (nothing committed), which makes NotifyPlayers correctly no-op
// (len(changes) == 0 short-circuit).
func applyByWorkflow(ctx context.Context, settingsStore *settings.Store, propStore proposalApplyStore, libStore *library.Store, sess *mode.Session, p proposals.Proposal, req applyProposalRequest, singleItem bool) ([]mode.PathChange, error) {
	switch p.Workflow {
	case proposals.Rename:
		switch p.Mode {
		case mode.Movies, mode.Series:
			// Apply re-reads whatever preset is currently configured rather
			// than freezing Scan-time's preset into the Proposal — consistent
			// with RelocateEpisode already recomputing the destination name
			// fresh at Apply time from p.Title/season/episode, never from
			// anything precomputed at Scan.
			preset, err := resolveNamingPreset(ctx, settingsStore, p.Mode)
			if err != nil {
				return nil, err
			}
			if p.Mode == mode.Movies {
				itemID, changes, err := rename.ApplyLibrary(ctx, libStore, p, preset)
				if err != nil {
					return changes, err
				}
				if singleItem {
					enrichMovieCollection(ctx, sess, libStore, itemID, p.TMDBID)
				}
				return changes, propStore.MarkApplied(ctx, p.ID, int(itemID))
			}
			episodeID, changes, err := rename.ApplyLibrarySeries(ctx, libStore, p, preset)
			if err != nil {
				return changes, err
			}
			return changes, propStore.MarkApplied(ctx, p.ID, int(episodeID))
		case mode.Adult:
			// Adult owns its own library now too (Whisparr eliminated,
			// Stage 4): relocate+rename to the AdultFileName scheme and
			// UpsertScene, never touching Whisparr. sess is threaded in only
			// for fingerprint give-back (best-effort). changes is returned
			// before the error check so a post-move UpsertScene failure still
			// notifies Stash of what physically moved (partial-success rule,
			// same as the Movies/Series library path).
			sceneID, fingerprintSubmitted, changes, err := rename.ApplyLibraryAdult(ctx, sess, libStore, p)
			if err != nil {
				return changes, err
			}
			if markErr := propStore.MarkApplied(ctx, p.ID, int(sceneID)); markErr != nil {
				return changes, markErr
			}
			if fingerprintSubmitted {
				return changes, propStore.MarkFingerprintSubmitted(ctx, p.ID)
			}
			return changes, nil
		default:
			return nil, fmt.Errorf("rename for unknown mode %q", p.Mode)
		}
	case proposals.Purge:
		switch p.Mode {
		case mode.Movies:
			changes, err := purge.ApplyLibrary(ctx, libStore, p)
			if err != nil {
				return changes, err
			}
			return changes, propStore.MarkApplied(ctx, p.ID, p.TrackedID)
		case mode.Series:
			changes, err := purge.ApplyLibrarySeries(ctx, libStore, p)
			if err != nil {
				return changes, err
			}
			return changes, propStore.MarkApplied(ctx, p.ID, p.TrackedID)
		case mode.Adult:
			changes, err := purge.ApplyLibraryAdult(ctx, libStore, p)
			if err != nil {
				return changes, err
			}
			return changes, propStore.MarkApplied(ctx, p.ID, p.TrackedID)
		default:
			return nil, fmt.Errorf("purge for unknown mode %q", p.Mode)
		}
	case proposals.Dedup:
		switch p.Mode {
		case mode.Movies:
			itemID, changes, err := dedup.ApplyLibrary(ctx, libStore, p, req.KeepIndex, req.KeepAll)
			if err != nil {
				return changes, err
			}
			return changes, propStore.MarkApplied(ctx, p.ID, int(itemID))
		case mode.Series:
			episodeID, changes, err := dedup.ApplyLibrarySeries(ctx, libStore, p, req.KeepIndex, req.KeepAll)
			if err != nil {
				return changes, err
			}
			return changes, propStore.MarkApplied(ctx, p.ID, int(episodeID))
		case mode.Adult:
			sceneID, changes, err := dedup.ApplyLibraryAdult(ctx, libStore, p, req.KeepIndex, req.KeepAll)
			if err != nil {
				return changes, err
			}
			return changes, propStore.MarkApplied(ctx, p.ID, int(sceneID))
		default:
			return nil, fmt.Errorf("dedup for unknown mode %q", p.Mode)
		}
	default:
		return nil, fmt.Errorf("%w: %q", errUnknownWorkflow, p.Workflow)
	}
}

// maxBatchItems bounds one apply-batch request. The proposals List endpoint
// has no pagination cap of its own to reuse, so this is the plan's fallback
// bound (200): a screen's worth of already-reviewed Pending rows applied in
// one click, not an unbounded firehose.
const maxBatchItems = 200

// applyBatchItem is one entry in an apply-batch request. It carries the same
// per-item Dedup override fields as applyProposalRequest (KeepIndex/KeepAll);
// Rename and Purge items ignore them, exactly as the single-item path does.
type applyBatchItem struct {
	ID        int64 `json:"id"`
	KeepIndex *int  `json:"keepIndex,omitempty"`
	KeepAll   bool  `json:"keepAll,omitempty"`
}

// applyBatchRequest is the body of POST /api/proposals/apply-batch — a
// same-screen (single workflow+mode) multi-select of already-reviewed Pending
// proposals to apply sequentially. No mode/workflow in the path: each proposal
// carries its own, looked up per item, so the existing applyByWorkflow
// dispatch is reused unchanged, just looped.
type applyBatchRequest struct {
	Items []applyBatchItem `json:"items"`
}

// applyBatchResultItem is one item's outcome. OK true means the proposal was
// applied and Proposal holds its refreshed (now Applied) row; OK false means
// it was skipped with Error explaining why — the batch never aborts early, so
// every requested id gets exactly one result.
type applyBatchResultItem struct {
	ID       int64               `json:"id"`
	OK       bool                `json:"ok"`
	Error    string              `json:"error,omitempty"`
	Proposal *proposals.Proposal `json:"proposal,omitempty"`
}

type applyBatchResponse struct {
	Results []applyBatchResultItem `json:"results"`
}

// applyBatchHandler applies a same-screen selection of Pending proposals
// sequentially with skip-and-continue semantics: one item's failure never
// stops the batch, every requested id gets an individual ok/error result, and
// the response is always 200 (per-item success lives in the body, not the HTTP
// status). Each item's file mutations are accumulated and fed to
// NotifyPlayers exactly once after the whole loop — one combined player-rescan
// notification instead of one per item. Sequential (not concurrent) by design:
// it matches today's one-at-a-time mental model and avoids reasoning about
// concurrent filesystem mutations across items that may touch overlapping
// paths (e.g. the same series folder). See .omc/plans/bulk-apply.md.
func applyBatchHandler(httpClient *http.Client, connStore *connections.Store, settingsStore *settings.Store, propStore proposalApplyStore, libStore *library.Store, whStore *webhooks.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		var req applyBatchRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		if len(req.Items) == 0 {
			http.Error(w, "items must not be empty", http.StatusBadRequest)
			return
		}
		if len(req.Items) > maxBatchItems {
			http.Error(w, fmt.Sprintf("too many items: %d exceeds the %d-item batch cap", len(req.Items), maxBatchItems), http.StatusBadRequest)
			return
		}

		// A batch is scoped to one screen (single workflow+mode), so every
		// item shares a mode in normal use. Sessions are built lazily and
		// cached by mode so mode.Build runs at most once per distinct mode
		// rather than once per item. changesByMode groups committed mutations
		// by mode so the post-loop notify fires exactly once per mode through
		// the correct mode-scoped session — routing changes through a single
		// last-built session would misroute a cross-mode batch's player
		// notifications (best-effort only, but correctness is free here).
		sessions := make(map[mode.Mode]*mode.Session)
		changesByMode := make(map[mode.Mode][]mode.PathChange)
		results := make([]applyBatchResultItem, 0, len(req.Items))

		// Concurrency note: no lock prevents a concurrent Scan (from a second
		// browser tab or background recheck) from calling ReplacePending while
		// this batch is in flight. If that race lands, a mid-batch item's
		// MarkApplied call returns ErrNotFound even though the file already
		// moved. The item is reported ok:false in the response, and the player
		// notify still fires for any items that did commit — partial success
		// is preserved. Single-operator, single-tab usage (the normal case)
		// is not affected.
		for _, item := range req.Items {
			p, err := propStore.Get(ctx, item.ID)
			if err != nil {
				results = append(results, applyBatchResultItem{ID: item.ID, OK: false, Error: err.Error()})
				continue
			}

			sess, ok := sessions[p.Mode]
			if !ok {
				sess, err = mode.Build(ctx, connStore, settingsStore, httpClient, nil, p.Mode)
				if err != nil {
					results = append(results, applyBatchResultItem{ID: item.ID, OK: false, Error: err.Error()})
					continue
				}
				sessions[p.Mode] = sess
			}

			changes, err := applyByWorkflow(ctx, settingsStore, propStore, libStore, sess, *p, applyProposalRequest{KeepIndex: item.KeepIndex, KeepAll: item.KeepAll}, false)
			// Accumulate committed changes unconditionally — independent of the
			// item's ok/error result. applyByWorkflow returns non-nil changes
			// alongside a non-nil err when the physical move/delete committed
			// but a later step (e.g. the MarkApplied DB write) failed: the file
			// really moved, so the players must be told regardless of whether
			// the proposal row got marked Applied. This mirrors the single-item
			// handler's unconditional notify (partial-success rule). Only the
			// Results entry below depends on err.
			changesByMode[p.Mode] = append(changesByMode[p.Mode], changes...)
			if err != nil {
				results = append(results, applyBatchResultItem{ID: item.ID, OK: false, Error: err.Error()})
				continue
			}

			updated, err := propStore.Get(ctx, item.ID)
			if err != nil {
				results = append(results, applyBatchResultItem{ID: item.ID, OK: false, Error: err.Error()})
				continue
			}
			results = append(results, applyBatchResultItem{ID: item.ID, OK: true, Proposal: updated})
			whStore.Dispatch(workflowEvent(p.Workflow), map[string]any{
				"mode": string(p.Mode), "workflow": string(p.Workflow),
				"title": p.Title, "tmdbId": p.TMDBID,
			})
		}

		// Fire one NotifyPlayers call per mode so each mode's changes reach
		// the correct mode-scoped players (Jellyfin for Movies/Series, Stash
		// for Adult). sessions[m] is always non-nil here since a mode entry is
		// only added after a successful mode.Build. NotifyPlayers is a no-op
		// on an empty slice.
		for m, changes := range changesByMode {
			if sess := sessions[m]; sess != nil {
				sess.NotifyPlayers(ctx, changes)
			}
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(applyBatchResponse{Results: results})
	}
}

// submitDraftHandler gives an Adult proposal's identification back to the
// community databases (TPDB/StashDB) — a separate, explicitly human-triggered
// action from Apply, only meaningful for Unmatched Adult Rename proposals
// that were confidently AI-identified but matched nothing (see
// rename.SubmitDraft's doc comment for why this isn't automatic).
func submitDraftHandler(httpClient *http.Client, connStore *connections.Store, settingsStore *settings.Store, propStore *proposals.Store) http.HandlerFunc {
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

		sess, err := mode.Build(ctx, connStore, settingsStore, httpClient, nil, p.Mode)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		draftID, err := rename.SubmitDraft(ctx, sess, *p)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		if err := propStore.MarkDraftSubmitted(ctx, id, draftID); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
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

type repickProposalRequest struct {
	TMDBID int    `json:"tmdbId"`
	Title  string `json:"title"`
	Year   int    `json:"year,omitempty"`
}

// repickProposalHandler is Rename's manual-override workflow: when Scan's
// automatic TMDB match picked wrong, or scored too low to auto-accept (see
// internal/rename/confidence.go), Dismiss alone can't correct it — it only
// removes the proposal from the queue. This lets an operator search TMDB
// directly (GET /api/modes/{mode}/tmdb-search, tmdbSearchHandler in
// discover.go) and assign a specific result instead, promoting an Unmatched
// proposal back to Pending (or correcting an already-Pending one) so it
// becomes actionable via the normal Apply path.
//
// Movies/Series Rename proposals only — Purge/Dedup have no "wrong
// identification" concept to correct, and Adult's Whisparr-lookup
// identification uses a different id space (foreignId, not tmdbId) with its
// own correction path, not this one. Applied/Dismissed proposals are
// refused: re-picking one would silently rewrite the queue's record of what
// already happened without touching anything on disk to match.
func repickProposalHandler(propStore *proposals.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := parseProposalID(w, r)
		if !ok {
			return
		}
		ctx := r.Context()

		var req repickProposalRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		if req.TMDBID <= 0 || req.Title == "" {
			http.Error(w, "tmdbId and title are both required", http.StatusBadRequest)
			return
		}

		p, err := propStore.Get(ctx, id)
		if err != nil {
			proposalNotFoundOr500(w, err)
			return
		}
		if p.Workflow != proposals.Rename || (p.Mode != mode.Movies && p.Mode != mode.Series) {
			http.Error(w, "re-picking is only supported for movies/series rename proposals", http.StatusBadRequest)
			return
		}
		if p.Status != proposals.Pending && p.Status != proposals.Unmatched {
			http.Error(w, fmt.Sprintf("proposal %d is %q — only pending or unmatched proposals can be re-picked", id, p.Status), http.StatusBadRequest)
			return
		}

		if err := propStore.Repick(ctx, id, req.Title, req.TMDBID, req.Year); err != nil {
			proposalNotFoundOr500(w, err)
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

// enrichMovieCollection is a best-effort post-Apply step that fetches
// belongs_to_collection from TMDB and records it on the just-relocated movie
// row. Any error (TMDB unavailable, movie with no collection) is silently
// ignored — Apply already succeeded and the file is already relocated.
func enrichMovieCollection(ctx context.Context, sess *mode.Session, libStore *library.Store, itemID int64, tmdbID int) {
	if sess.TMDB == nil {
		return
	}
	details, err := sess.TMDB.MovieDetails(ctx, tmdbID)
	if err != nil || details.Collection.ID == 0 {
		return
	}
	collID, err := libStore.UpsertCollection(ctx, details.Collection.ID, details.Collection.Name)
	if err != nil {
		return
	}
	_ = libStore.SetItemCollection(ctx, itemID, collID)
}
