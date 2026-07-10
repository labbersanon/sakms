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

// applyProposalHandler is the only place in SAK's API that actually
// mutates a *arr app on a workflow's behalf — and only for the one proposal
// ID in the URL, never a batch, matching the design's staged-for-approval
// principle: a Scan proposes, a human picks, Apply commits exactly that. The
// proposal's own Workflow field (set at Scan time) decides which package's
// Apply actually runs — the URL doesn't need to say which, since a proposal
// ID alone is already unambiguous.
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

		// Movies proposals never need a Servarr session (their Apply path is
		// entirely libStore-backed — see applyByWorkflow), but mode.Build is
		// still cheap and harmless to call: it just leaves sess.Servarr nil.
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
// outcome. Movies/Series route each workflow to its libStore-backed
// *Library counterpart instead (no *arr app involved); Adult uses the
// existing Servarr-backed functions, unchanged. The three workflows have
// different success shapes — Rename can partially succeed (registered, but
// the follow-up scan trigger failed) and still counts as applied; Purge's
// delete either fully succeeds or fully fails; Dedup's Apply already
// returns the resulting tracked id the same way Rename's does — so each
// branch marks the queue accordingly rather than forcing all three through
// one shared success rule.
func applyByWorkflow(ctx context.Context, settingsStore *settings.Store, propStore *proposals.Store, libStore *library.Store, sess *mode.Session, p proposals.Proposal, req applyProposalRequest) error {
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
				itemID, err := rename.ApplyLibrary(ctx, libStore, p, preset)
				if err != nil {
					return err
				}
				return propStore.MarkApplied(ctx, p.ID, int(itemID))
			}
			episodeID, err := rename.ApplyLibrarySeries(ctx, libStore, p, preset)
			if err != nil {
				return err
			}
			return propStore.MarkApplied(ctx, p.ID, int(episodeID))
		}
		trackedID, fingerprintSubmitted, err := rename.Apply(ctx, sess, p)
		if trackedID != 0 {
			// Registered even if the follow-up scan trigger failed — see
			// rename.Apply's doc comment. Record it as applied either way so
			// the queue doesn't lose track of an item that's now real.
			if markErr := propStore.MarkApplied(ctx, p.ID, trackedID); markErr != nil {
				return markErr
			}
			if fingerprintSubmitted {
				if markErr := propStore.MarkFingerprintSubmitted(ctx, p.ID); markErr != nil {
					return markErr
				}
			}
		}
		return err
	case proposals.Purge:
		switch p.Mode {
		case mode.Movies:
			if err := purge.ApplyLibrary(ctx, libStore, p); err != nil {
				return err
			}
			return propStore.MarkApplied(ctx, p.ID, p.TrackedID)
		case mode.Series:
			if err := purge.ApplyLibrarySeries(ctx, libStore, p); err != nil {
				return err
			}
			return propStore.MarkApplied(ctx, p.ID, p.TrackedID)
		}
		if err := purge.Apply(ctx, sess, p); err != nil {
			return err
		}
		return propStore.MarkApplied(ctx, p.ID, p.TrackedID)
	case proposals.Dedup:
		switch p.Mode {
		case mode.Movies:
			itemID, err := dedup.ApplyLibrary(ctx, libStore, p, req.KeepIndex, req.KeepAll)
			if err != nil {
				return err
			}
			return propStore.MarkApplied(ctx, p.ID, int(itemID))
		case mode.Series:
			episodeID, err := dedup.ApplyLibrarySeries(ctx, libStore, p, req.KeepIndex, req.KeepAll)
			if err != nil {
				return err
			}
			return propStore.MarkApplied(ctx, p.ID, int(episodeID))
		}
		trackedID, err := dedup.Apply(ctx, sess, p, req.KeepIndex, req.KeepAll)
		if err != nil {
			return err
		}
		return propStore.MarkApplied(ctx, p.ID, trackedID)
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
