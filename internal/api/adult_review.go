package api

// Claude 2026-08-12: Phase E1 — Adult Review preview and confirm API endpoints
// Reason: deep-interview-adult-rename-review-alts — two new mode-scoped routes:
//
//	GET  /api/modes/{mode}/rename/proposals/{id}/review
//	POST /api/modes/{mode}/rename/proposals/{id}/review-confirm
//
// Both are mode-scoped (/{mode}/) rather than bare /api/proposals/{id}/…:
// proposal_video.go:79-84 documents that a route without /modes/{mode}/ loses
// Layer 1's Adult section-lock classification entirely. Defense-in-depth
// requires both Layer 1 (classifyModes, URL pattern) and Layer 2 (mode.Build).
//
// Handler ordering follows proposalVideoHandler:86-120 — parse, get row,
// denyIfAdultLocked IMMEDIATELY, then mode mismatch, then eligibility checks.
// The lock check must come first so a locked section leaks nothing beyond
// "locked" (not even "this id exists" or "it's the wrong mode").
//
// Troubleshooting: web-identified-only rows had no operator escape hatch;
// these routes supply it.
// Review if: the section-lock or mode.Build API changes shape.

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/labbersanon/sakms/internal/connections"
	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/organizeevents"
	"github.com/labbersanon/sakms/internal/proposals"
	"github.com/labbersanon/sakms/internal/rename"
	"github.com/labbersanon/sakms/internal/sectionlock"
	"github.com/labbersanon/sakms/internal/serviceconn"
	"github.com/labbersanon/sakms/internal/settings"
)

// adultReviewPreviewHandler serves GET /api/modes/{mode}/rename/proposals/{id}/review.
// Returns 200 + AdultReviewPreview on success.
// 400 for: non-Adult/non-Rename/non-Unmatched/already-has-a-scene-id proposal.
// 502 for an unresolvable source file.
func adultReviewPreviewHandler(connStore *connections.Store, scStore *serviceconn.Store, settingsStore *settings.Store, propStore *proposals.Store, libStore *library.Store, videoHasher rename.PHasher, prober deduprober) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		m := mode.Mode(r.PathValue("mode"))
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			http.Error(w, "invalid proposal id", http.StatusBadRequest)
			return
		}
		ctx := r.Context()

		p, err := propStore.Get(ctx, id)
		if err != nil {
			if errors.Is(err, proposals.ErrNotFound) {
				http.Error(w, "no proposal with that id", http.StatusBadRequest)
				return
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		// SECURITY: Layer 3 — FIRST thing after the row is resolved, BEFORE any
		// other answer is written. See proposal_video.go's security ordering doc.
		if p.Mode == mode.Adult && denyIfAdultLocked(w, r) {
			return
		}
		if p.Mode != m {
			http.Error(w, "proposal does not belong to that mode", http.StatusBadRequest)
			return
		}

		// Layer 2 section lock — mode.Build rejects Adult when locked.
		sess, err := mode.Build(ctx, connStore, scStore, settingsStore, nil, nil, p.Mode)
		if err != nil {
			if errors.Is(err, sectionlock.ErrSectionLocked) {
				writeSectionLocked(w, sectionlock.SectionAdultContent)
				return
			}
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		preview, err := rename.BuildAdultReview(ctx, sess, libStore, videoHasher, prober, *p)
		if err != nil {
			// eligibility errors are 400; resolution errors are 502.
			if isEligibilityError(err) {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}

		resp := adultReviewPreviewToDTO(preview)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}
}

// adultReviewConfirmHandler serves POST /api/modes/{mode}/rename/proposals/{id}/review-confirm.
// Branching on request body:
//   - Both Box and SceneID non-empty → catalog branch (RepickAdultScene → ApplyLibraryAdult).
//   - Otherwise → local branch (ConfirmAdultReviewLocal).
//
// Both branches: NotifyPlayers, organizeevents.Log, respond 200 with the
// refreshed (now-applied) proposal.
func adultReviewConfirmHandler(connStore *connections.Store, scStore *serviceconn.Store, settingsStore *settings.Store, propStore *proposals.Store, libStore *library.Store, videoHasher rename.PHasher, prober deduprober) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		m := mode.Mode(r.PathValue("mode"))
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			http.Error(w, "invalid proposal id", http.StatusBadRequest)
			return
		}
		ctx := r.Context()

		p, err := propStore.Get(ctx, id)
		if err != nil {
			if errors.Is(err, proposals.ErrNotFound) {
				http.Error(w, "no proposal with that id", http.StatusBadRequest)
				return
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		// SECURITY: same ordering as GET — lock check before any other answer.
		if p.Mode == mode.Adult && denyIfAdultLocked(w, r) {
			return
		}
		if p.Mode != m {
			http.Error(w, "proposal does not belong to that mode", http.StatusBadRequest)
			return
		}

		// Layer 2 — needed for sess.Identify (catalog confirm) and
		// sess.NotifyPlayers (both branches). mode.Build rejects Adult when locked.
		sess, err := mode.Build(ctx, connStore, scStore, settingsStore, nil, nil, p.Mode)
		if err != nil {
			if errors.Is(err, sectionlock.ErrSectionLocked) {
				writeSectionLocked(w, sectionlock.SectionAdultContent)
				return
			}
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		var req struct {
			FileName string `json:"fileName"`
			Box      string `json:"box,omitempty"`
			SceneID  string `json:"sceneId,omitempty"`
			Title    string `json:"title,omitempty"`
			Studio   string `json:"studio,omitempty"`
			Date     string `json:"date,omitempty"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}

		tier := string(autoGrabTier(ctx, settingsStore, p.Mode))
		var changes []mode.PathChange

		if req.Box != "" && req.SceneID != "" {
			// Catalog branch: flip the proposal to Pending with the chosen
			// catalog identity, then run the ordinary ApplyLibraryAdult (which
			// includes alternate fold, give-back, and undo behaviour for free).
			// Claude 2026-08-12: reject reserved local box on the catalog branch.
			// Reason: req.Box is otherwise unvalidated; box=local would write into
			//   the Review local-identity namespace and fold against phash scenes.
			// Troubleshooting: crafted confirm body created a "catalog" row under box=local.
			// Review if: catalog confirm validates box against configured stashbox list.
			if strings.EqualFold(req.Box, library.LocalSceneBox) {
				http.Error(w, `box "local" is reserved for Review local identities — pick a catalog scene`, http.StatusBadRequest)
				return
			}
			if repickErr := propStore.RepickAdultScene(ctx, p.ID, req.Title, req.Studio, req.Date, req.Box, req.SceneID); repickErr != nil {
				if errors.Is(repickErr, proposals.ErrNotFound) {
					http.Error(w, "no proposal with that id", http.StatusBadRequest)
				} else {
					http.Error(w, repickErr.Error(), http.StatusInternalServerError)
				}
				return
			}
			updated, getErr := propStore.Get(ctx, p.ID)
			if getErr != nil {
				http.Error(w, getErr.Error(), http.StatusInternalServerError)
				return
			}
			sceneID, fingerprintSubmitted, applyChanges, applyErr := rename.ApplyLibraryAdult(ctx, sess, libStore, *updated, tier, prober)
			if applyErr != nil {
				http.Error(w, applyErr.Error(), http.StatusBadGateway)
				return
			}
			changes = applyChanges
			if markErr := markAppliedResilient(ctx, propStore, *updated, int(sceneID)); markErr != nil {
				http.Error(w, markErr.Error(), http.StatusInternalServerError)
				return
			}
			if fingerprintSubmitted {
				_ = propStore.MarkFingerprintSubmitted(ctx, p.ID)
			}
		} else {
			// Local branch: rename + mint a local identity.
			if req.FileName == "" {
				http.Error(w, "fileName is required for the local confirm branch", http.StatusBadRequest)
				return
			}
			sceneID, localChanges, confirmErr := rename.ConfirmAdultReviewLocal(ctx, libStore, videoHasher, prober, *p, req.FileName, tier)
			if confirmErr != nil {
				if isEligibilityError(confirmErr) {
					http.Error(w, confirmErr.Error(), http.StatusBadRequest)
					return
				}
				http.Error(w, confirmErr.Error(), http.StatusBadGateway)
				return
			}
			changes = localChanges
			if markErr := markAppliedResilient(ctx, propStore, *p, int(sceneID)); markErr != nil {
				http.Error(w, markErr.Error(), http.StatusInternalServerError)
				return
			}
		}

		sess.NotifyPlayers(ctx, changes)
		organizeevents.Log(ctx, organizeevents.Event{
			Workflow:   "rename",
			Mode:       string(mode.Adult),
			Kind:       organizeevents.KindApply,
			ProposalID: p.ID,
			Message:    "Review confirm for " + p.SourceName,
		})

		// Respond with the refreshed (now-applied) proposal — consistent with
		// the repick handler's shape (proposals.go:984-991).
		final, err := propStore.Get(ctx, p.ID)
		if err != nil {
			final = p // best-effort fallback
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(final)
	}
}

// adultReviewPreviewToDTO converts the rename package's internal preview to the
// apidto shape the frontend consumes. Kept as a local converter rather than
// making rename import apidto — those are different layers.
func adultReviewPreviewToDTO(p rename.AdultReviewPreview) map[string]any {
	out := map[string]any{
		"proposedName": p.ProposedName,
	}
	if p.Studio != "" {
		out["studio"] = p.Studio
	}
	if p.Title != "" {
		out["title"] = p.Title
	}
	if p.Date != "" {
		out["date"] = p.Date
	}
	if p.PHash != "" {
		out["phash"] = p.PHash
	}
	if p.CatalogBox != "" {
		out["catalogBox"] = p.CatalogBox
	}
	if p.CatalogSceneID != "" {
		out["catalogSceneId"] = p.CatalogSceneID
	}
	if p.CatalogTitle != "" {
		out["catalogTitle"] = p.CatalogTitle
	}
	if p.CatalogStudio != "" {
		out["catalogStudio"] = p.CatalogStudio
	}
	if p.CatalogDate != "" {
		out["catalogDate"] = p.CatalogDate
	}
	if p.RecheckError != "" {
		out["recheckError"] = p.RecheckError
	}
	return out
}

// isEligibilityError reports whether err is a proposal-eligibility guard
// failure (wrong mode/workflow/status/scene-id) — these map to 400, not 502.
// Based on the guard messages in rename.adultReviewGuards.
func isEligibilityError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	for _, kw := range []string{"not a Rename proposal", "not an Adult proposal", "not unmatched", "already has a catalog scene id", "empty after sanitisation", "extension mismatch", "file name"} {
		if strings.Contains(msg, kw) {
			return true
		}
	}
	return false
}

// deduprober is a local alias so the handler file doesn't need to import
// internal/dedup directly — the concrete *mediainfo.Prober satisfies both
// rename.Prober and dedup.Prober at the call site (handler.go's NewMux).
type deduprober = rename.Prober
