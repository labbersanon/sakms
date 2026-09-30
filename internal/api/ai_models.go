package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/labbersanon/sakms/internal/connections"
	"github.com/labbersanon/sakms/internal/ollama"
)

// ollamaModelsHandler live-fetches the model names actually installed on the
// saved Ollama connection (connections.Get "ollama"), via ollama.ListModels.
// Auth-gated like every other route on this mux. A missing/unreachable
// instance or an unexpected response shape is reported as a clean 4xx, never
// a 500 — the frontend treats this the same as any other "couldn't reach X"
// connection error.
//
// Claude 2026-09-30: URL comes from the saved connection, never ?url=.
// Reason: GET /api/ollama/models?url= was CSRF+SSRF — any authenticated GET
//
//	(and a cross-site img/navigation) could make SAK fetch an attacker URL.
//
// Troubleshooting: 400 "ollama isn't configured" → save the connection first.
// Review if: Settings grows a "test unsaved URL" that stays POST-only.
func ollamaModelsHandler(connStore *connections.Store, httpClient *http.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		conn, err := connStore.Get(r.Context(), "ollama")
		if err != nil {
			if errors.Is(err, connections.ErrNotFound) {
				http.Error(w, "ollama isn't configured yet — add it in Settings first", http.StatusBadRequest)
				return
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if strings.TrimSpace(conn.URL) == "" {
			http.Error(w, "ollama isn't configured yet — add it in Settings first", http.StatusBadRequest)
			return
		}
		c := ollama.New(conn.URL, "", httpClient)
		models, err := c.ListModels(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		if models == nil {
			models = []string{}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(models)
	}
}
