package api

// Claude 2026-08-03: new file — HTTP surface for propose-only pruning rules
// (B5, plan .omc/plans/autopilot-impl-pruning-rules.md §13.1 + §9.6).
// Reason: CRUD mirrors discover_sliders.go handler-for-handler (that store is
// internal/pruning's direct template), and the preview endpoint is the Critic
// safety amendment's SOFT warning — it reports what a draft rule WOULD match
// and never stages, blocks, or persists anything.
// Troubleshooting: pruningStore MAY BE NIL (test call sites that don't
// exercise rules pass nil to NewMux). Every handler here answers 503 rather
// than panicking in that case.

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/labbersanon/sakms/internal/apidto"
	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/pruning"
	"github.com/labbersanon/sakms/internal/purge"
)

// toDTOPruningRule maps an internal pruning.Rule onto the exported
// apidto.PruningRule wire DTO (field-for-field, since apidto.PruningRule
// mirrors Rule's JSON tags exactly) — direct sibling of toDTOSlider.
func toDTOPruningRule(r pruning.Rule) apidto.PruningRule {
	return apidto.PruningRule{
		ID:               r.ID,
		Name:             r.Name,
		Mode:             r.Mode,
		AgeDays:          r.AgeDays,
		SizeBytes:        r.SizeBytes,
		QualityTierFloor: r.QualityTierFloor,
		Tags:             r.Tags,
		Enabled:          r.Enabled,
		CreatedAt:        r.CreatedAt,
		UpdatedAt:        r.UpdatedAt,
	}
}

func toDTOPruningRules(rules []pruning.Rule) []apidto.PruningRule {
	out := make([]apidto.PruningRule, len(rules))
	for i, r := range rules {
		out[i] = toDTOPruningRule(r)
	}
	return out
}

// ruleFromUpsert builds an internal Rule from an upsert body. id is 0 for a
// create.
func ruleFromUpsert(id int64, req apidto.PruningRuleUpsertRequest) pruning.Rule {
	return pruning.Rule{
		ID:               id,
		Name:             req.Name,
		Mode:             req.Mode,
		AgeDays:          req.AgeDays,
		SizeBytes:        req.SizeBytes,
		QualityTierFloor: req.QualityTierFloor,
		Tags:             req.Tags,
		Enabled:          req.Enabled,
	}
}

// pruningRuleStoreError maps a pruning.Store validation/lookup error onto an
// HTTP status, exactly as discoverSliderStoreError does for sliders: the fixed
// enum/threshold/"at least one condition" errors are always a bad request
// body, never a server fault; ErrNotFound is a 404.
func pruningRuleStoreError(w http.ResponseWriter, err error) {
	switch {
	case err == nil:
		return
	case errors.Is(err, pruning.ErrNotFound):
		http.Error(w, err.Error(), http.StatusNotFound)
	case errors.Is(err, pruning.ErrNameRequired),
		errors.Is(err, pruning.ErrInvalidMode),
		errors.Is(err, pruning.ErrNoConditions),
		errors.Is(err, pruning.ErrInvalidTierFloor),
		errors.Is(err, pruning.ErrNegativeThreshold),
		errors.Is(err, pruning.ErrBlankTag):
		http.Error(w, err.Error(), http.StatusBadRequest)
	default:
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// pruningStoreUnavailable reports (and answers) the nil-store case. It is not
// reachable in production — main.go always constructs the store — only from a
// test mux built without it.
func pruningStoreUnavailable(w http.ResponseWriter, store *pruning.Store) bool {
	if store != nil {
		return false
	}
	http.Error(w, "pruning rules are not available on this server", http.StatusServiceUnavailable)
	return true
}

// listPruningRulesHandler returns every pruning rule ordered by id —
// GET /api/pruning-rules. Rules for all three modes come back in one list;
// each carries its own Mode, and the Settings surface groups them client-side.
func listPruningRulesHandler(store *pruning.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if pruningStoreUnavailable(w, store) {
			return
		}
		rules, err := store.List(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, toDTOPruningRules(rules))
	}
}

// createPruningRuleHandler is POST /api/pruning-rules — validated by
// pruning.Validate inside Store.Create (name, mode enum, tier-floor enum,
// non-negative thresholds, and AC1's "at least one condition configured").
func createPruningRuleHandler(store *pruning.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if pruningStoreUnavailable(w, store) {
			return
		}
		var req apidto.PruningRuleUpsertRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		created, err := store.Create(r.Context(), ruleFromUpsert(0, req))
		if err != nil {
			pruningRuleStoreError(w, err)
			return
		}
		writeJSON(w, toDTOPruningRule(created))
	}
}

// updatePruningRuleHandler is PUT /api/pruning-rules/{id} — overwrites every
// editable field (the upsert body is deliberately whole-rule, not a patch, so
// clearing a condition is expressible as its unset sentinel).
func updatePruningRuleHandler(store *pruning.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if pruningStoreUnavailable(w, store) {
			return
		}
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			http.Error(w, "id path parameter must be an integer", http.StatusBadRequest)
			return
		}
		var req apidto.PruningRuleUpsertRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		updated, err := store.Update(r.Context(), ruleFromUpsert(id, req))
		if err != nil {
			pruningRuleStoreError(w, err)
			return
		}
		writeJSON(w, toDTOPruningRule(updated))
	}
}

// deletePruningRuleHandler is DELETE /api/pruning-rules/{id}. Unlike the
// slider sibling, deleting an id that is already gone IS a 404 — see
// pruning.Store.Delete's doc for why that direction was chosen here.
func deletePruningRuleHandler(store *pruning.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if pruningStoreUnavailable(w, store) {
			return
		}
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			http.Error(w, "id path parameter must be an integer", http.StatusBadRequest)
			return
		}
		if err := store.Delete(r.Context(), id); err != nil {
			pruningRuleStoreError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// previewPruningRuleHandler is POST /api/pruning-rules/preview — the §13.1
// SOFT warning: it answers "how many library items would this rule match right
// now?" for a draft that need not exist yet, and the UI shows the number
// without ever blocking a save.
//
// It counts by running the mode's real purge.Scan* propose function with this
// one rule, then returning how many proposals came back. That is deliberate
// rather than lazy, and MORE load-bearing now than when it was written: it
// guarantees the count uses the identical Subject projection and Series
// episode aggregation the actual Scan uses (plan §2.4), which a second
// hand-written projection here would drift from the first time either changed.
// The Scan functions are pure — they return proposals, they never persist — so
// nothing is staged and no Apply becomes possible from this endpoint.
//
// Claude 2026-08-11: the `nil` allowlist argument these three calls used to
// carry is gone with the mechanism, and the per-item tag read it made pointless
// is no longer pointless — it feeds the tags condition. So preview now genuinely
// previews TAG matches too, a capability the old Settings tab could not offer.
//
// Enabled is forced true regardless of the body: the operator is asking what
// the conditions match, and a draft still toggled off would otherwise always
// answer 0.
func previewPruningRuleHandler(store *pruning.Store, libStore *library.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if pruningStoreUnavailable(w, store) {
			return
		}
		var req apidto.PruningRuleUpsertRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		draft := ruleFromUpsert(0, req)
		draft.Enabled = true
		if draft.Name == "" {
			// Validate rejects a blank name, but a name has no bearing on what
			// a rule MATCHES — an operator previewing before they have typed
			// one should still get a count. Everything else (mode enum, tier
			// floor, non-negative thresholds, at-least-one-condition) is
			// enforced below exactly as it is on save.
			draft.Name = "preview"
		}
		if err := pruning.Validate(draft); err != nil {
			pruningRuleStoreError(w, err)
			return
		}

		ctx := r.Context()
		rules := []pruning.Rule{draft}
		var (
			matched int
			err     error
		)
		switch mode.Mode(draft.Mode) {
		case mode.Movies:
			list, scanErr := purge.ScanLibrary(ctx, libStore, rules)
			matched, err = len(list), scanErr
		case mode.Series:
			list, scanErr := purge.ScanLibrarySeries(ctx, libStore, rules)
			matched, err = len(list), scanErr
		case mode.Adult:
			list, scanErr := purge.ScanLibraryAdult(ctx, libStore, rules, "")
			matched, err = len(list), scanErr
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, apidto.PruningRulePreviewResponse{MatchCount: matched})
	}
}
