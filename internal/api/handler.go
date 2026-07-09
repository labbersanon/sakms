package api

import (
	"encoding/json"
	"net/http"

	"github.com/curtiswtaylorjr/sakms/internal/allowlist"
	"github.com/curtiswtaylorjr/sakms/internal/connections"
	"github.com/curtiswtaylorjr/sakms/internal/dedup"
	"github.com/curtiswtaylorjr/sakms/internal/grabs"
	"github.com/curtiswtaylorjr/sakms/internal/mode"
	"github.com/curtiswtaylorjr/sakms/internal/proposals"
	"github.com/curtiswtaylorjr/sakms/internal/settings"
)

// NewMux returns an http.ServeMux with SAK's API routes mounted.
// httpClient is shared across every outbound call the API makes (Test,
// Scan, Apply), so its timeout and transport settings apply uniformly.
// connStore persists what's actually configured — Test and Save are
// deliberately separate actions, matching Settings' own "Test connection"
// then "Save" flow. propStore backs every workflow's review queue (Rename,
// Purge, Dedup); allowStore backs Purge's per-mode tag allowlist; prober
// backs Dedup's direct ffprobe reads (a real *mediainfo.Prober in
// production, anything satisfying dedup.Prober in tests); settingsStore
// backs the setup wizard's dismissed flag; grabsStore backs Search's grab
// tracking (a separate concept from propStore's Scan-stage-Apply queue —
// see internal/grabs' package doc for why).
func NewMux(httpClient *http.Client, connStore *connections.Store, propStore *proposals.Store, allowStore *allowlist.Store, prober dedup.Prober, settingsStore *settings.Store, grabsStore *grabs.Store) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/connections/test", connectionsTestHandler(httpClient))
	mux.HandleFunc("GET /api/connections", listConnectionsHandler(connStore))
	mux.HandleFunc("PUT /api/connections/{service}", upsertConnectionHandler(connStore))
	mux.HandleFunc("DELETE /api/connections/{service}", deleteConnectionHandler(connStore))

	mux.HandleFunc("GET /api/modes/{mode}/root-folders", listRootFoldersHandler(httpClient, connStore, settingsStore))
	mux.HandleFunc("GET /api/modes/{mode}/tracked", listTrackedHandler(httpClient, connStore, settingsStore))

	mux.HandleFunc("POST /api/modes/{mode}/rename/scan", renameScanHandler(httpClient, connStore, settingsStore, propStore))
	mux.HandleFunc("GET /api/modes/{mode}/rename/proposals", listProposalsHandler(propStore, proposals.Rename))
	mux.HandleFunc("GET /api/modes/{mode}/rename/kids-root-path", getKidsRootPathHandler(settingsStore))
	mux.HandleFunc("PUT /api/modes/{mode}/rename/kids-root-path", putKidsRootPathHandler(settingsStore))

	mux.HandleFunc("POST /api/modes/{mode}/purge/scan", purgeScanHandler(httpClient, connStore, settingsStore, propStore, allowStore))
	mux.HandleFunc("GET /api/modes/{mode}/purge/proposals", listProposalsHandler(propStore, proposals.Purge))
	mux.HandleFunc("GET /api/modes/{mode}/purge/allowlist", listAllowlistHandler(allowStore))
	mux.HandleFunc("POST /api/modes/{mode}/purge/allowlist", addAllowlistTagHandler(allowStore))
	mux.HandleFunc("DELETE /api/modes/{mode}/purge/allowlist/{tag}", removeAllowlistTagHandler(allowStore))

	mux.HandleFunc("POST /api/modes/{mode}/dedup/scan", dedupScanHandler(httpClient, connStore, settingsStore, propStore, prober))
	mux.HandleFunc("GET /api/modes/{mode}/dedup/proposals", listProposalsHandler(propStore, proposals.Dedup))

	// Discover is a read-only proxy against TMDB (trending/popular titles,
	// poster art) — the browse entry point into Search. Search itself is a
	// read-only proxy+score against Prowlarr — nothing staged or persisted
	// (see searchHandler's doc comment). Grab is the one mutating action,
	// tracked in grabsStore rather than propStore (see internal/grabs'
	// package doc for why this isn't a proposals.Kind).
	mux.HandleFunc("GET /api/modes/{mode}/discover", discoverHandler(httpClient, connStore, settingsStore))
	mux.HandleFunc("GET /api/modes/{mode}/discover/tvdb-id", resolveTVDBIDHandler(httpClient, connStore, settingsStore))
	mux.HandleFunc("GET /api/modes/{mode}/search", searchHandler(httpClient, connStore, settingsStore))
	mux.HandleFunc("POST /api/modes/{mode}/search/grab", grabHandler(httpClient, connStore, settingsStore, grabsStore))
	mux.HandleFunc("GET /api/modes/{mode}/grabs", listGrabsHandler(grabsStore))
	mux.HandleFunc("POST /api/grabs/{id}/check-import", checkImportHandler(httpClient, connStore, settingsStore, grabsStore))

	mux.HandleFunc("GET /api/modes/{mode}/tags", listTagsHandler(httpClient, connStore, settingsStore))
	mux.HandleFunc("POST /api/modes/{mode}/items/{itemId}/tags", addItemTagHandler(httpClient, connStore, settingsStore))
	mux.HandleFunc("DELETE /api/modes/{mode}/items/{itemId}/tags/{tagId}", removeItemTagHandler(httpClient, connStore, settingsStore))

	mux.HandleFunc("GET /api/setup/status", setupStatusHandler(connStore, allowStore, settingsStore))
	mux.HandleFunc("PUT /api/setup/dismissed", dismissSetupHandler(settingsStore))

	// One shared AI provider+model pair for every AI-assisted feature (Adult
	// identification AND Movies/Series Rename's AI fallback) — see
	// mode.AIModelKey's doc comment for why this isn't split per mode.
	mux.HandleFunc("GET /api/settings/ai-provider", getAIProviderHandler(settingsStore))
	mux.HandleFunc("PUT /api/settings/ai-provider", putAIProviderHandler(settingsStore))
	mux.HandleFunc("GET /api/settings/ai-model", getOllamaModelHandler(settingsStore, mode.AIModelKey))
	mux.HandleFunc("PUT /api/settings/ai-model", putOllamaModelHandler(settingsStore, mode.AIModelKey))

	mux.HandleFunc("POST /api/proposals/{id}/apply", applyProposalHandler(httpClient, connStore, settingsStore, propStore))
	mux.HandleFunc("POST /api/proposals/{id}/submit-draft", submitDraftHandler(httpClient, connStore, settingsStore, propStore))
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
	URL      string `json:"url"`
	Username string `json:"username,omitempty"` // only qbittorrent/nzbget use this
	APIKey   string `json:"apiKey,omitempty"`
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
		if err := store.UpsertWithUsername(r.Context(), service, req.URL, req.Username, req.APIKey); err != nil {
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
