package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/labbersanon/sakms/internal/connections"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/organizeevents"
	"github.com/labbersanon/sakms/internal/proposals"
	"github.com/labbersanon/sakms/internal/rename"
	"github.com/labbersanon/sakms/internal/serviceconn"
	"github.com/labbersanon/sakms/internal/settings"
)

// proposals_delete.go holds Rename's Delete action — the only handler in this
// package that permanently destroys a file. It lives in its own file rather
// than alongside applyBatchHandler in proposals.go specifically so the
// destructive path stays visibly separate and small enough to read whole.

// deleteBatchItem mirrors applyBatchItem's {ID, SourcePath} pair EXACTLY and
// deliberately carries nothing else — no KeepIndex/KeepAll (Dedup-only), and
// no action field: this endpoint has exactly one action by construction,
// which is precisely why it exists as its own route rather than as a mode
// flag on apply-batch. See .omc/plans/autopilot-impl.md §1.3.
//
// SourcePath is optional safety-net context: when the id lookup misses after a
// concurrent rescan rotated ids, resolveBatchProposal re-resolves the live row
// by this path. Safe for delete because the fallback matches on the path the
// operator already confirmed, so it can never redirect the deletion to a
// different file — only to a different row pointing at that same file (§5.6).
type deleteBatchItem struct {
	ID         int64  `json:"id"`
	SourcePath string `json:"sourcePath,omitempty"`
}

type deleteBatchRequest struct {
	Items []deleteBatchItem `json:"items"`
}

// deleteBatchResultItem mirrors applyBatchResultItem's {id, ok, error} shape
// MINUS the Proposal field, and the omission is the correct answer rather than
// an oversight: a deleted item has no row to refresh — that absence IS the
// success condition. Synthesizing one would return a proposal guaranteed never
// to exist again. The existing dismiss path already returns this exact shape
// client-side and BatchResultSummary already consumes it. See §2.4.
type deleteBatchResultItem struct {
	ID    int64  `json:"id"`
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

type deleteBatchResponse struct {
	Results []deleteBatchResultItem `json:"results"`
}

// proposalDeleteStore is the store surface this handler needs: the read-only
// resolver resolveBatchProposal already takes, plus the row destruction
// rename.DeleteSource performs. Declared as an interface (rather than taking
// *proposals.Store) so a test can make the row delete fail while os.Remove
// already committed — the partial-success case a real store cannot produce.
// *proposals.Store satisfies it, so handler.go's wiring passes propStore
// unchanged.
type proposalDeleteStore interface {
	proposalResolver
	Delete(ctx context.Context, id int64) error
}

// deleteBatchHandler permanently removes each named Rename proposal's source
// file from disk AND deletes the proposal row entirely — NOT a Dismiss. The
// operator explicitly chose "leave no trace" over Dismiss's keep-an-audit-trail
// semantics, so nothing survives in the queue or the history view. There is no
// trash, no undo, and no soft-delete anywhere in this repo; os.Remove cannot be
// taken back.
//
// Skip-and-continue, always 200: one item's failure never stops the batch,
// every requested id gets exactly one ok/error result, and per-item success
// lives in the body rather than the HTTP status — the same contract
// applyBatchHandler documents. Each item's committed PathChange is accumulated
// and fed to NotifyPlayers exactly once per mode after the whole loop.
//
// # SECURITY REQUIREMENT: mode.Build MUST run before rename.DeleteSource
//
// This is a security requirement, not an optimization or a style choice.
// /api/proposals/* classifies under {organize} only, never {adult-content}, so
// Layer 1 of the section PIN lock structurally cannot see an Adult proposal
// addressed by row id — exactly the trap dismissProposalHandler's own doc
// comment records: "it does NOT call mode.Build, so Layer 2 never sees it, and
// /api/proposals/* classifies as {organize} only — dismissing an ADULT proposal
// while Adult alone was locked would otherwise succeed." mode.Build's Layer 2
// (internal/mode/mode.go:425, sectionlock.RequireMode) is the ONLY thing
// enforcing the Adult PIN lock on this route.
//
// Therefore the session is built BEFORE any filesystem mutation, per item,
// cached per mode. The tempting optimization — defer mode.Build until after
// the loop since notify is post-loop anyway, or build sessions lazily only for
// modes that produced changes — WOULD SILENTLY DELETE ADULT FILES WHILE ADULT
// IS LOCKED, because the lock is only ever discovered by mode.Build and by
// then os.Remove has already run. The ordering is pinned by
// TestDeleteBatch_AdultLockedRefusedBeforeFileRemoved; a static reading of this
// handler is not enough to keep it true across future edits. See §5.5.
//
// # DELIBERATE DIVERGENCE: capped at proposals.MaxProposalPageSize
//
// Rename's apply-batch is deliberately UNCAPPED (proposals.go's cap switch has
// an explicit "no item-count cap" case for Rename). This handler caps. A future
// session WILL try to "align" the two — do not. An uncapped destructive request
// is not the same risk class as an uncapped rename: a bad rename is
// recoverable, an os.Remove is not. MaxProposalPageSize is the same bound
// Dedup and Purge batches already carry, and Purge is the closest existing
// analogue precisely because it deletes files too. Exceeding it rejects the
// WHOLE request with one 400 naming the cap — deliberately not per-item
// skip-and-continue for this specific failure, so the operator learns to split
// the selection rather than reading 250 identical row errors.
//
// # DELIBERATE OMISSION: no webhook dispatch, no whStore parameter
//
// applyBatchHandler fires whStore.Dispatch(workflowEvent(p.Workflow), ...) per
// OK item. workflowEvent(Rename) means "a rename was applied" — dispatching it
// for a destroyed file would tell the operator's subscribers a rename happened
// to a file that no longer exists. There is no delete webhook event and
// inventing one is out of scope, so whStore is not a parameter at all. This is
// a decision, not an omission. See §1.3.
//
// # DELIBERATE OMISSION: no libStore parameter
//
// A Pending/Unmatched Rename proposal is untracked — every Rename scan feeds a
// `known` tracked-path set into library.ScanRootFolder, which masks tracked
// paths from ever becoming proposals (see rename.DeleteSource's own doc comment
// for the full mechanism and its Review-if). There is no library row to clean
// up, and an unused libStore param would invite a future session to "restore"
// bookkeeping that was never needed.
func deleteBatchHandler(httpClient *http.Client, connStore *connections.Store, scStore *serviceconn.Store, settingsStore *settings.Store, propStore proposalDeleteStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		var req deleteBatchRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		if len(req.Items) == 0 {
			http.Error(w, "items must not be empty", http.StatusBadRequest)
			return
		}
		// UNCONDITIONAL, and deliberately not nested inside a first-item Get the
		// way apply-batch's cap switch is: a request whose first id happens to be
		// dead must still be capped. See the divergence note above for why this
		// endpoint caps at all.
		if len(req.Items) > proposals.MaxProposalPageSize {
			http.Error(w, fmt.Sprintf("too many items: %d exceeds the %d-item cap for delete batches — split the selection", len(req.Items), proposals.MaxProposalPageSize), http.StatusBadRequest)
			return
		}

		// Apply-gate held for the batch's (mode, workflow) as soon as we know it —
		// before the item loop — so a concurrent ReplacePending (watch-folder scan
		// or manual rescan) defers instead of deleting rows mid-loop. Without it
		// the id-miss fallback below becomes the routine path instead of a rare
		// safety net, which is the single most likely way to make this feature
		// intermittently delete the wrong thing. Do not "simplify" it away as
		// apply-batch-specific ceremony (§5.7).
		gateActive := make(map[applyGateKey]bool)
		defer func() {
			for k := range gateActive {
				proposals.DefaultGate.EndApply(k.m, k.wf)
			}
		}()
		if first, err := propStore.Get(ctx, req.Items[0].ID); err == nil {
			gk := applyGateKey{first.Mode, first.Workflow}
			proposals.DefaultGate.BeginApply(first.Mode, first.Workflow)
			gateActive[gk] = true
		}

		sessions := make(map[mode.Mode]*mode.Session)
		changesByMode := make(map[mode.Mode][]mode.PathChange)
		results := make([]deleteBatchResultItem, 0, len(req.Items))

		// Captured from the first item that RESOLVES — not from req.Items[0],
		// whose own resolve can fail, and NOT post-loop via propStore.Get.
		//
		// applyBatchHandler fills these with a post-loop Get of req.Items[0].ID.
		// That is correct there and BROKEN here, silently: apply only flips the
		// row's status, so the row still exists post-loop. After a successful
		// DELETE the row is gone — that is the entire feature — so the Get returns
		// ErrNotFound, both fields stay "", and organizeevents.Store.List's
		// `WHERE workflow = ?` branch matches the event against no screen's
		// filter. OrganizeChrome.tsx's activity log always passes a workflow, so
		// the event would never render — destroying the only durable trace a
		// delete leaves anywhere. NEVER use a second propStore.Get for this.
		logWF, logMode := "", ""

		for _, item := range req.Items {
			p, err := resolveBatchProposal(ctx, propStore, applyBatchItem{ID: item.ID, SourcePath: item.SourcePath}, gateActive)
			if err != nil {
				results = append(results, deleteBatchResultItem{ID: item.ID, OK: false, Error: err.Error()})
				continue
			}

			// AFTER resolve (only p carries the authoritative workflow/mode — the
			// request body carries neither), BEFORE the eligibility re-check (an
			// item that resolves and is then rejected still truthfully identifies
			// the screen this batch came from), and BEFORE DeleteSource destroys
			// the row.
			if logWF == "" {
				logWF, logMode = string(p.Workflow), string(p.Mode)
			}

			// Acquire the gate for this (mode, workflow) on first encounter.
			gk := applyGateKey{p.Mode, p.Workflow}
			if !gateActive[gk] {
				proposals.DefaultGate.BeginApply(p.Mode, p.Workflow)
				gateActive[gk] = true
			}

			// Server-side eligibility, re-checked on the RESOLVED proposal — never
			// on the client's request. rename.DeleteSource re-checks both of these
			// too: belt and braces. The check here is what produces a good
			// user-facing error naming the actual status/workflow; the check in the
			// function is what makes the function safe for any future caller that
			// bypasses this handler. Workflow gating matters as much as status
			// gating — without it this endpoint is a generic "delete any
			// proposal's source file" primitive reachable for Dedup and Purge rows,
			// with none of those workflows' own libStore bookkeeping (§5.4).
			if p.Workflow != proposals.Rename {
				results = append(results, deleteBatchResultItem{ID: item.ID, OK: false,
					Error: fmt.Sprintf("proposal %d is a %q proposal, not rename — delete is a Rename-only action", p.ID, p.Workflow)})
				continue
			}
			if p.Status != proposals.Pending && p.Status != proposals.Unmatched {
				results = append(results, deleteBatchResultItem{ID: item.ID, OK: false,
					Error: fmt.Sprintf("proposal %d is %q — delete is only available for pending or unmatched proposals", p.ID, p.Status)})
				continue
			}

			// SECURITY ORDERING — see the doc comment. This must stay above
			// DeleteSource: it is the only enforcement of the Adult PIN lock on
			// this route, and a locked Adult section surfaces here as a wrapped
			// sectionlock.ErrSectionLocked from mode.Build.
			sess, ok := sessions[p.Mode]
			if !ok {
				sess, err = mode.Build(ctx, connStore, scStore, settingsStore, httpClient, nil, p.Mode)
				if err != nil {
					results = append(results, deleteBatchResultItem{ID: item.ID, OK: false, Error: err.Error()})
					continue
				}
				sessions[p.Mode] = sess
			}

			changes, err := rename.DeleteSource(ctx, propStore, *p)
			// Accumulate committed changes UNCONDITIONALLY, before checking err.
			// DeleteSource returns non-nil changes alongside a non-nil err when
			// os.Remove committed but the subsequent row delete failed: the file
			// really is gone, so the players must be told regardless of the item's
			// reported outcome. Only the result entry below depends on err. This
			// mirrors applyBatchHandler's documented partial-success rule (§5.1).
			changesByMode[p.Mode] = append(changesByMode[p.Mode], changes...)
			if err != nil {
				results = append(results, deleteBatchResultItem{ID: item.ID, OK: false, Error: err.Error()})
				continue
			}
			results = append(results, deleteBatchResultItem{ID: item.ID, OK: true})
		}

		// One grouped NotifyPlayers per mode so each mode's deletions reach the
		// correct mode-scoped players (Jellyfin for Movies/Series, Stash's
		// CleanMetadata for Adult). A per-item endpoint would fire N round trips
		// instead of one, which is the whole reason delete got a batch route
		// rather than dismiss's per-id loop shape.
		for m, changes := range changesByMode {
			if sess := sessions[m]; sess != nil {
				sess.NotifyPlayers(ctx, changes)
			}
		}

		if len(results) > 0 {
			okCount := 0
			for _, res := range results {
				if res.OK {
					okCount++
				}
			}
			ok := okCount == len(results)
			organizeevents.Log(ctx, organizeevents.Event{
				Workflow: logWF,
				Mode:     logMode,
				Kind:     organizeevents.KindDeleteBatch,
				OK:       &ok,
				Message:  fmt.Sprintf("delete-batch %d/%d ok", okCount, len(results)),
			})
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(deleteBatchResponse{Results: results})
	}
}
