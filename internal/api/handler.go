package api

import (
	"encoding/json"
	"net/http"

	"github.com/curtiswtaylorjr/tidyarr/internal/allowlist"
	"github.com/curtiswtaylorjr/tidyarr/internal/connections"
	"github.com/curtiswtaylorjr/tidyarr/internal/dedup"
	"github.com/curtiswtaylorjr/tidyarr/internal/proposals"
)

// NewMux returns an http.ServeMux with Tidyarr's API routes mounted.
// httpClient is shared across every outbound call the API makes (Test,
// Scan, Apply), so its timeout and transport settings apply uniformly.
// connStore persists what's actually configured — Test and Save are
// deliberately separate actions, matching Settings' own "Test connection"
// then "Save" flow. propStore backs every workflow's review queue (Rename,
// Purge, Dedup); allowStore backs Purge's per-mode tag allowlist; prober
// backs Dedup's direct ffprobe reads (a real *mediainfo.Prober in
// production, anything satisfying dedup.Prober in tests).
func NewMux(httpClient *http.Client, connStore *connections.Store, propStore *proposals.Store, allowStore *allowlist.Store, prober dedup.Prober) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/connections/test", connectionsTestHandler(httpClient))
	mux.HandleFunc("GET /api/connections", listConnectionsHandler(connStore))
	mux.HandleFunc("PUT /api/connections/{service}", upsertConnectionHandler(connStore))
	mux.HandleFunc("DELETE /api/connections/{service}", deleteConnectionHandler(connStore))

	mux.HandleFunc("POST /api/modes/{mode}/rename/scan", renameScanHandler(httpClient, connStore, propStore))
	mux.HandleFunc("GET /api/modes/{mode}/rename/proposals", listProposalsHandler(propStore, proposals.Rename))

	mux.HandleFunc("POST /api/modes/{mode}/purge/scan", purgeScanHandler(httpClient, connStore, propStore, allowStore))
	mux.HandleFunc("GET /api/modes/{mode}/purge/proposals", listProposalsHandler(propStore, proposals.Purge))
	mux.HandleFunc("GET /api/modes/{mode}/purge/allowlist", listAllowlistHandler(allowStore))
	mux.HandleFunc("POST /api/modes/{mode}/purge/allowlist", addAllowlistTagHandler(allowStore))
	mux.HandleFunc("DELETE /api/modes/{mode}/purge/allowlist/{tag}", removeAllowlistTagHandler(allowStore))

	mux.HandleFunc("POST /api/modes/{mode}/dedup/scan", dedupScanHandler(httpClient, connStore, propStore, prober))
	mux.HandleFunc("GET /api/modes/{mode}/dedup/proposals", listProposalsHandler(propStore, proposals.Dedup))

	mux.HandleFunc("GET /api/modes/{mode}/tags", listTagsHandler(httpClient, connStore))
	mux.HandleFunc("POST /api/modes/{mode}/items/{itemId}/tags", addItemTagHandler(httpClient, connStore))
	mux.HandleFunc("DELETE /api/modes/{mode}/items/{itemId}/tags/{tagId}", removeItemTagHandler(httpClient, connStore))

	mux.HandleFunc("POST /api/proposals/{id}/apply", applyProposalHandler(httpClient, connStore, propStore))
	mux.HandleFunc("POST /api/proposals/{id}/dismiss", dismissProposalHandler(propStore))
	return mux
}

func connectionsTestHandler(httpClient *http.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req ConnectionTestRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}

		result := TestConnection(r.Context(), httpClient, req)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(result)
	}
}

func listConnectionsHandler(store *connections.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		list, err := store.List(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(list)
	}
}

type upsertConnectionRequest struct {
	URL    string `json:"url"`
	APIKey string `json:"apiKey,omitempty"`
}

func upsertConnectionHandler(store *connections.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		service := r.PathValue("service")
		var req upsertConnectionRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		if req.URL == "" {
			http.Error(w, "url is required", http.StatusBadRequest)
			return
		}
		if err := store.Upsert(r.Context(), service, req.URL, req.APIKey); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func deleteConnectionHandler(store *connections.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		service := r.PathValue("service")
		if err := store.Delete(r.Context(), service); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}
