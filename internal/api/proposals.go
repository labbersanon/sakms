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

// applyProposalHandler commits exactly the one proposal ID in the URL, never
// a batch, matching the design's staged-for-approval principle: a Scan
// proposes, a human picks, Apply commits exactly that. The proposal's own
// Workflow field (set at Scan time) decides which package's Apply actually
// runs — the URL doesn't need to say which, since a proposal ID alone is
// already unambiguous. No mode touches a *arr app anymore; every Apply is
// library-backed (see applyByWorkflow).
func applyProposalHandler(httpClient *http.Client, connStore *connections.Store, settingsStore *settings.Store, propStore *proposals.Store, libStore *library.Store) http.HandlerFunc {
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
		sess, err := mode.Build(ctx, connStore, settingsStore, httpClient, p.Mode)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		if err := applyByWorkflow(ctx, settingsStore, propStore, libStore, sess, *p, req); err != nil {
			if errors.Is(err, errUnknownWorkflow) {
				http.Error(w, err.Error(), http.StatusInternalServerError)
			} else {
				http.Error(w, err.Error(), http.StatusBadGateway)
			}
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
// one shared success rule. A committed file move is still fed to
// sess.NotifyPlayers even when the branch returns a non-nil err afterward
// (partial-success rule; changes is captured before the error check).
func applyByWorkflow(ctx context.Context, settingsStore *settings.Store, propStore *proposals.Store, libStore *library.Store, sess *mode.Session, p proposals.Proposal, req applyProposalRequest) error {
	// changes accumulates whatever file-level mutations the branch below
	// actually commits to disk; the deferred NotifyPlayers fires on
	// whatever landed in it even when the branch goes on to return a
	// non-nil err (partial success — see each Apply function's doc
	// comment). Nil changes on an early error means nothing committed, so
	// NotifyPlayers correctly no-ops (len(changes) == 0 short-circuit).
	var changes []mode.PathChange
	defer func() { sess.NotifyPlayers(ctx, changes) }()

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
				return err
			}
			if p.Mode == mode.Movies {
				itemID, c, err := rename.ApplyLibrary(ctx, libStore, p, preset)
				changes = c
				if err != nil {
					return err
				}
				return propStore.MarkApplied(ctx, p.ID, int(itemID))
			}
			episodeID, c, err := rename.ApplyLibrarySeries(ctx, libStore, p, preset)
			changes = c
			if err != nil {
				return err
			}
			return propStore.MarkApplied(ctx, p.ID, int(episodeID))
		case mode.Adult:
			// Adult owns its own library now too (Whisparr eliminated,
			// Stage 4): relocate+rename to the AdultFileName scheme and
			// UpsertScene, never touching Whisparr. sess is threaded in only
			// for fingerprint give-back (best-effort). changes is captured
			// before the error check so a post-move UpsertScene failure still
			// notifies Stash of what physically moved (partial-success rule,
			// same as the Movies/Series library path).
			sceneID, fingerprintSubmitted, c, err := rename.ApplyLibraryAdult(ctx, sess, libStore, p)
			changes = c
			if err != nil {
				return err
			}
			if markErr := propStore.MarkApplied(ctx, p.ID, int(sceneID)); markErr != nil {
				return markErr
			}
			if fingerprintSubmitted {
				return propStore.MarkFingerprintSubmitted(ctx, p.ID)
			}
			return nil
		default:
			return fmt.Errorf("rename for unknown mode %q", p.Mode)
		}
	case proposals.Purge:
		switch p.Mode {
		case mode.Movies:
			c, err := purge.ApplyLibrary(ctx, libStore, p)
			changes = c
			if err != nil {
				return err
			}
			return propStore.MarkApplied(ctx, p.ID, p.TrackedID)
		case mode.Series:
			c, err := purge.ApplyLibrarySeries(ctx, libStore, p)
			changes = c
			if err != nil {
				return err
			}
			return propStore.MarkApplied(ctx, p.ID, p.TrackedID)
		case mode.Adult:
			c, err := purge.ApplyLibraryAdult(ctx, libStore, p)
			changes = c
			if err != nil {
				return err
			}
			return propStore.MarkApplied(ctx, p.ID, p.TrackedID)
		default:
			return fmt.Errorf("purge for unknown mode %q", p.Mode)
		}
	case proposals.Dedup:
		switch p.Mode {
		case mode.Movies:
			itemID, c, err := dedup.ApplyLibrary(ctx, libStore, p, req.KeepIndex, req.KeepAll)
			changes = c
			if err != nil {
				return err
			}
			return propStore.MarkApplied(ctx, p.ID, int(itemID))
		case mode.Series:
			episodeID, c, err := dedup.ApplyLibrarySeries(ctx, libStore, p, req.KeepIndex, req.KeepAll)
			changes = c
			if err != nil {
				return err
			}
			return propStore.MarkApplied(ctx, p.ID, int(episodeID))
		case mode.Adult:
			sceneID, c, err := dedup.ApplyLibraryAdult(ctx, libStore, p, req.KeepIndex, req.KeepAll)
			changes = c
			if err != nil {
				return err
			}
			return propStore.MarkApplied(ctx, p.ID, int(sceneID))
		default:
			return fmt.Errorf("dedup for unknown mode %q", p.Mode)
		}
	default:
		return fmt.Errorf("%w: %q", errUnknownWorkflow, p.Workflow)
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

		sess, err := mode.Build(ctx, connStore, settingsStore, httpClient, p.Mode)
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
