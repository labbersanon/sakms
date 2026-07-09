package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/curtiswtaylorjr/sak/internal/auth"
)

// NewAuthMux returns the handful of routes that must stay reachable without
// a session — setup, login, logout, and status — kept on their OWN mux,
// deliberately separate from NewMux's business-logic routes. cmd/sak
// wraps NewMux's result in auth.Middleware but mounts this one unwrapped;
// keeping them apart means that middleware never needs an exemption list,
// and NewMux's own large existing test suite never has to know auth exists
// at all.
func NewAuthMux(authStore *auth.Store, tokenEnc auth.TokenEncryptor) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/auth/setup", authSetupHandler(authStore, tokenEnc))
	mux.HandleFunc("POST /api/auth/login", authLoginHandler(authStore, tokenEnc))
	mux.HandleFunc("POST /api/auth/logout", authLogoutHandler())
	mux.HandleFunc("GET /api/auth/status", authStatusHandler(authStore, tokenEnc))
	return mux
}

type authCredentialsRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// authSetupHandler creates SAK's one login — refuses once a login
// already exists (checked fresh on every call, not cached) so a visitor who
// reaches an already-configured instance can't silently take it over by
// "setting up" a login of their own; they need /api/auth/login instead.
func authSetupHandler(authStore *auth.Store, tokenEnc auth.TokenEncryptor) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		configured, err := authStore.Configured(ctx)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if configured {
			http.Error(w, "a login is already configured — use /api/auth/login instead", http.StatusConflict)
			return
		}

		var req authCredentialsRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		if err := authStore.SetCredentials(ctx, req.Username, req.Password); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		token, err := auth.IssueToken(tokenEnc)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		auth.SetSessionCookie(w, token)
		w.WriteHeader(http.StatusNoContent)
	}
}

func authLoginHandler(authStore *auth.Store, tokenEnc auth.TokenEncryptor) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req authCredentialsRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}

		ok, err := authStore.Verify(r.Context(), req.Username, req.Password)
		if err != nil && !errors.Is(err, auth.ErrNotConfigured) {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if !ok {
			http.Error(w, "invalid username or password", http.StatusUnauthorized)
			return
		}

		token, err := auth.IssueToken(tokenEnc)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		auth.SetSessionCookie(w, token)
		w.WriteHeader(http.StatusNoContent)
	}
}

// authLogoutHandler always succeeds — clearing a cookie that may not exist
// is harmless, and there's no server-side session state to invalidate (see
// session.go's doc comment on why tokens are stateless).
func authLogoutHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		auth.ClearSessionCookie(w)
		w.WriteHeader(http.StatusNoContent)
	}
}

type authStatusResponse struct {
	Configured    bool `json:"configured"`
	Authenticated bool `json:"authenticated"`
}

// authStatusHandler is the one endpoint the frontend calls before it knows
// anything else about the instance — it decides which of "create your
// login," "log in," or "proceed" to show.
func authStatusHandler(authStore *auth.Store, tokenEnc auth.TokenEncryptor) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		configured, err := authStore.Configured(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(authStatusResponse{
			Configured:    configured,
			Authenticated: auth.Authenticated(tokenEnc, r),
		})
	}
}
