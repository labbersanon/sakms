package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"github.com/curtiswtaylorjr/sak/internal/connections"
	"github.com/curtiswtaylorjr/sak/internal/dedup"
	"github.com/curtiswtaylorjr/sak/internal/mode"
	"github.com/curtiswtaylorjr/sak/internal/proposals"
	"github.com/curtiswtaylorjr/sak/internal/purge"
	"github.com/curtiswtaylorjr/sak/internal/rename"
	"github.com/curtiswtaylorjr/sak/internal/settings"
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
func applyProposalHandler(httpClient *http.Client, connStore *connections.Store, settingsStore *settings.Store, propStore *proposals.Store) http.HandlerFunc {
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

		sess, err := mode.Build(ctx, connStore, settingsStore, httpClient, p.Mode)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		if err := applyByWorkflow(ctx, propStore, sess, *p, req); err != nil {
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
// outcome. The three workflows have different success shapes — Rename can
// partially succeed (registered, but the follow-up scan trigger failed) and
// still counts as applied; Purge's delete either fully succeeds or fully
// fails; Dedup's Apply already returns the resulting tracked id the same
// way Rename's does — so each branch marks the queue accordingly rather
// than forcing all three through one shared success rule.
func applyByWorkflow(ctx context.Context, propStore *proposals.Store, sess *mode.Session, p proposals.Proposal, req applyProposalRequest) error {
	switch p.Workflow {
	case proposals.Rename:
		trackedID, err := rename.Apply(ctx, sess, p)
		if trackedID != 0 {
			// Registered even if the follow-up scan trigger failed — see
			// rename.Apply's doc comment. Record it as applied either way so
			// the queue doesn't lose track of an item that's now real.
			if markErr := propStore.MarkApplied(ctx, p.ID, trackedID); markErr != nil {
				return markErr
			}
		}
		return err
	case proposals.Purge:
		if err := purge.Apply(ctx, sess, p); err != nil {
			return err
		}
		return propStore.MarkApplied(ctx, p.ID, p.TrackedID)
	case proposals.Dedup:
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
