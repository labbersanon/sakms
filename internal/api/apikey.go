package api

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"

	"github.com/curtiswtaylorjr/sakms/internal/auth"
)

// NewAPIKeyMux returns the API-key management routes (status + regenerate).
// Kept on its own dedicated mux, separate from NewMux, for the same reason
// NewAuthMux is separate: these must be session-protected (unlike
// NewAuthMux's routes), but NewMux has 20 existing test call sites and the
// house convention is "NewMux stays unaware auth exists" — adding these
// routes here instead of to NewMux keeps that convention intact. cmd/sakms
// wraps this mux in the SAME auth.Middleware as NewMux's, so either a
// session cookie or a valid API key reaches it.
func NewAPIKeyMux(authStore *auth.Store) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/apikey", apikeyStatusHandler(authStore))
	mux.HandleFunc("POST /api/apikey/regenerate", apikeyRegenerateHandler(authStore))
	return mux
}

func apikeyStatusHandler(authStore *auth.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		status, err := authStore.APIKeyStatus(r.Context())
		if err != nil {
			log.Printf("apikey status: %v", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(status)
	}
}

// apikeyRegenerateResponse is the one place the full API key crosses the
// API boundary — shown once, never retrievable again afterward.
type apikeyRegenerateResponse struct {
	APIKey    string `json:"apiKey"`
	KeySuffix string `json:"keySuffix"`
}

func apikeyRegenerateHandler(authStore *auth.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		raw, keySuffix, err := authStore.Regenerate(r.Context())
		if errors.Is(err, auth.ErrEnvManaged) {
			http.Error(w, "API key is managed by the SAKMS_API_KEY environment variable; unset it to manage the key here", http.StatusConflict)
			return
		}
		if err != nil {
			log.Printf("apikey regenerate: %v", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(apikeyRegenerateResponse{APIKey: raw, KeySuffix: keySuffix})
	}
}
