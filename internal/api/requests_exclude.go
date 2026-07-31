package api

import (
	"encoding/json"
	"net/http"

	"github.com/labbersanon/sakms/internal/apidto"
	"github.com/labbersanon/sakms/internal/excludes"
	"github.com/labbersanon/sakms/internal/grabs"
	"github.com/labbersanon/sakms/internal/library"
)

// NewRequestsMux mounts the Requests worklist + its exclusion endpoints on their
// own mux, mounted exact-match ("/api/requests") + subtree ("/api/requests/") on
// the top mux in cmd/sakms/main.go.
//
// Why not NewMux: the exclusion feature needs an *excludes.Store, a dependency
// NewMux doesn't otherwise carry — same situation (and same resolution) as
// NewRecheckTriggerMux/NewAPIKeyMux, whose routes live here rather than threading
// a new store through NewMux's ~245 call sites. GET /api/requests moved here with
// them (it now reads the exclusion list to suppress removed titles), so it is
// deliberately NOT registered in NewMux anymore.
func NewRequestsMux(grabsStore *grabs.Store, libStore *library.Store, excludesStore *excludes.Store) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/requests", requestsHandler(grabsStore, libStore, excludesStore))
	mux.HandleFunc("POST /api/requests/exclude", excludeTitleHandler(excludesStore))
	mux.HandleFunc("POST /api/requests/exclude-batch", excludeTitlesBatchHandler(excludesStore))
	return mux
}

// excludeTitleHandler backs POST /api/requests/exclude — permanently remove one
// title from the Requests worklist. Idempotent (re-excluding is a no-op, 204).
func excludeTitleHandler(excludesStore *excludes.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req apidto.ExcludeTitleRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		if err := excludesStore.Add(r.Context(), req.Mode, req.TMDBID, req.Title); err != nil {
			if err == excludes.ErrInvalid {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// excludeTitlesBatchHandler backs POST /api/requests/exclude-batch — exclude
// several titles in one call, applied per item with skip-and-continue semantics
// (one item's failure never blocks the rest), matching this project's
// established bulk-apply convention (see apidto.ApplyBatchResponse). Always HTTP
// 200 (except the empty-body 400) — per-item success/failure lives in Results.
func excludeTitlesBatchHandler(excludesStore *excludes.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req apidto.ExcludeTitlesBatchRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		if len(req.Items) == 0 {
			http.Error(w, "items is required", http.StatusBadRequest)
			return
		}
		ctx := r.Context()
		results := make([]apidto.ExcludeTitleResult, 0, len(req.Items))
		for i, item := range req.Items {
			res := apidto.ExcludeTitleResult{Index: i, Mode: item.Mode, Title: item.Title}
			if err := excludesStore.Add(ctx, item.Mode, item.TMDBID, item.Title); err != nil {
				res.Error = err.Error()
			} else {
				res.OK = true
			}
			results = append(results, res)
		}
		writeJSON(w, apidto.ExcludeTitlesBatchResponse{Results: results})
	}
}
