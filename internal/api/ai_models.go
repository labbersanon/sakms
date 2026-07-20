package api

import (
	"encoding/json"
	"net/http"

	"github.com/labbersanon/sakms/internal/ollama"
)

// ollamaModelsHandler live-fetches the model names actually installed on an
// operator-supplied Ollama instance, via ollama.ListModels — the same
// live-fetch pattern netscan.go uses for LAN service probing. Auth-gated
// like every other route on this mux (it lives on NewMux, not the public
// setup/login mux). A missing/unreachable instance or an unexpected
// response shape is reported as a clean 4xx, never a 500 — the frontend
// treats this the same as any other "couldn't reach X" connection error.
func ollamaModelsHandler(httpClient *http.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		url := r.URL.Query().Get("url")
		if url == "" {
			http.Error(w, "url is required", http.StatusBadRequest)
			return
		}
		c := ollama.New(url, "", httpClient)
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
