package api

import (
	"encoding/json"
	"log"
	"net/http"

	"github.com/labbersanon/sakms/internal/auth"
)

// NewAuthModeMux returns the routes for reading and switching the active
// auth mode (GET/PUT /api/auth/mode). Kept on its own small mux, mirroring
// NewAPIKeyMux's precedent: switching auth modes mutates security-relevant
// state, so — unlike NewAuthMux's setup/login/logout/status routes, which
// must stay reachable without a session — this mux must be wrapped in
// auth.Middleware by the caller (cmd/sakms). A logged-in operator (or
// holder of the universal API key) both reads and writes here; a first-run
// visitor with neither cookie nor key never reaches it, which is why
// first-run mode selection goes through the public /api/auth/setup body
// instead (see the plan's §0.7 first-run bootstrap fix).
func NewAuthModeMux(authStore *auth.Store) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/auth/mode", getAuthModeHandler(authStore))
	mux.HandleFunc("PUT /api/auth/mode", putAuthModeHandler(authStore))
	return mux
}

type authModeResponse struct {
	Mode string `json:"mode"`
}

func getAuthModeHandler(authStore *auth.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		mode, err := authStore.AuthMode(r.Context())
		if err != nil {
			log.Printf("auth mode status: %v", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(authModeResponse{Mode: mode})
	}
}

type authModeRequest struct {
	Mode string `json:"mode"`
	// AcknowledgeInsecure must be true to switch INTO "none" — the second
	// of G2's two required entry points (the first is /api/auth/setup's
	// first-run "none" branch).
	AcknowledgeInsecure bool `json:"acknowledgeInsecure"`
}

// putAuthModeHandler switches the active auth mode, enforcing G4's
// switch-into preconditions before writing anything:
//   - "password": a password hash must already exist (PasswordConfigured) —
//     otherwise a switch could strand the instance with no way back in.
//   - "oidc": issuer/client id/client secret/redirect URL must already exist
//     (OIDCConfigured) — reachable post-setup because the operator is already
//     authenticated some other way (password or the universal API key) by the
//     time they're switching modes from Settings.
//   - "none": requires acknowledgeInsecure:true (G2).
//
// On success, SetAuthMode writes ONLY auth_mode — the departed mode's
// config (e.g. a password hash) is never wiped, so switching away and back
// requires no reconfiguration (G4, AC6).
func putAuthModeHandler(authStore *auth.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		var req authModeRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}

		switch req.Mode {
		case auth.ModePassword:
			ok, err := authStore.PasswordConfigured(ctx)
			if err != nil {
				log.Printf("auth mode switch (password precondition): %v", err)
				http.Error(w, "internal error", http.StatusInternalServerError)
				return
			}
			if !ok {
				http.Error(w, "password auth is not configured yet — set a password before switching to it", http.StatusBadRequest)
				return
			}
		case auth.ModeOIDC:
			ok, err := authStore.OIDCConfigured(ctx)
			if err != nil {
				log.Printf("auth mode switch (oidc precondition): %v", err)
				http.Error(w, "internal error", http.StatusInternalServerError)
				return
			}
			if !ok {
				http.Error(w, "oidc auth is not configured yet — set issuer/client id/client secret/redirect URL before switching to it", http.StatusBadRequest)
				return
			}
		case auth.ModeNone:
			if !req.AcknowledgeInsecure {
				http.Error(w, "acknowledgeInsecure must be true to switch to the none auth mode", http.StatusBadRequest)
				return
			}
		default:
			http.Error(w, "unknown auth mode", http.StatusBadRequest)
			return
		}

		if err := authStore.SetAuthMode(ctx, req.Mode); err != nil {
			log.Printf("auth mode switch: %v", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}
