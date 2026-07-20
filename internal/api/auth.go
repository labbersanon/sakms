package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/labbersanon/sakms/internal/auth"
)

// NewAuthMux returns the handful of routes that must stay reachable without
// a session — setup, login, logout, status, and the two OIDC redirect legs
// (login → IdP, callback ← IdP) — kept on their OWN mux, deliberately
// separate from NewMux's business-logic routes. cmd/sakms wraps NewMux's
// result in auth.Middleware but mounts this one unwrapped; keeping them apart
// means that middleware never needs an exemption list, and NewMux's own large
// existing test suite never has to know auth exists at all. The OIDC legs
// belong here (not behind Middleware) for the same reason as login/setup:
// they run BEFORE any session exists — the whole point of the flow is to
// establish one.
func NewAuthMux(authStore *auth.Store, tokenEnc auth.TokenEncryptor) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/auth/setup", authSetupHandler(authStore, tokenEnc))
	mux.HandleFunc("POST /api/auth/login", authLoginHandler(authStore, tokenEnc))
	mux.HandleFunc("POST /api/auth/logout", authLogoutHandler())
	mux.HandleFunc("GET /api/auth/status", authStatusHandler(authStore, tokenEnc))
	mux.HandleFunc("GET /api/auth/oidc/login", oidcLoginHandler(authStore))
	mux.HandleFunc("GET /api/auth/oidc/callback", oidcCallbackHandler(authStore, tokenEnc))
	return mux
}

type authCredentialsRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
	// Mode selects the auth strategy at first run — "" means "password"
	// (today's exact back-compat behavior).
	Mode string `json:"mode"`
	// AcknowledgeInsecure must be true to select Mode "none" — a genuine
	// no-auth instance requires an explicit, unmissable opt-in (G2).
	AcknowledgeInsecure bool `json:"acknowledgeInsecure"`
	// OIDCIssuerURL/OIDCClientID/OIDCClientSecret/OIDCRedirectURL are required
	// together for an "oidc"-mode setup request — carried in this public setup
	// body, not a protected config endpoint, because no credential exists yet
	// to authenticate against one. The redirect URL is operator-supplied (not
	// derived from the request Host, which is spoofable) and must be the IdP's
	// registered callback, e.g.
	// https://media-admin.zaena.us/api/auth/oidc/callback. Ignored for every
	// other mode.
	OIDCIssuerURL    string `json:"oidcIssuerUrl,omitempty"`
	OIDCClientID     string `json:"oidcClientId,omitempty"`
	OIDCClientSecret string `json:"oidcClientSecret,omitempty"`
	OIDCRedirectURL  string `json:"oidcRedirectUrl,omitempty"`
}

// authSetupResponse is the JSON body returned by authSetupHandler for modes
// that must hand something back to the caller — currently just "oidc", whose
// first-run mints a one-time break-glass API key (there is no interactive
// login fallback at setup time, since the browser hasn't completed the
// redirect dance yet). Empty for "password"/"none", which still respond with
// a bare 204.
type authSetupResponse struct {
	// APIKey is the one-time break-glass API key minted during an oidc-mode
	// first-run — revealed ONCE here (G6). Empty when SAKMS_API_KEY is active
	// (see APIKeyNote).
	APIKey string `json:"apiKey,omitempty"`
	// APIKeyNote replaces APIKey when SAKMS_API_KEY is active: no settings key
	// is minted (it would be a no-op under env precedence), so instead point
	// the operator at the env value as their break-glass credential.
	APIKeyNote string `json:"apiKeyNote,omitempty"`
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

		mode := req.Mode
		if mode == "" {
			mode = auth.ModePassword // back-compat: today's exact default
		}

		switch mode {
		case auth.ModePassword:
			if err := authStore.SetCredentials(ctx, req.Username, req.Password); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if err := authStore.SetAuthMode(ctx, auth.ModePassword); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			token, err := auth.IssueToken(tokenEnc)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			auth.SetSessionCookie(w, token, false)
			w.WriteHeader(http.StatusNoContent)
		case auth.ModeNone:
			if !req.AcknowledgeInsecure {
				http.Error(w, "acknowledgeInsecure must be true to select the none auth mode", http.StatusBadRequest)
				return
			}
			// No credentials, no cookie — "none" mode has nothing to
			// authenticate.
			if err := authStore.SetAuthMode(ctx, auth.ModeNone); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		case auth.ModeOIDC:
			// First-run bootstrap: carried in this same public setup body, not
			// a protected config endpoint, because no credential exists yet to
			// authenticate against one. All four fields are required together —
			// SAK has no server-generated fallback here (the operator already
			// holds a client id/secret from the IdP's own UI, and supplies the
			// registered redirect URL explicitly rather than SAK deriving it
			// from a spoofable Host header).
			issuerURL := strings.TrimSpace(req.OIDCIssuerURL)
			clientID := strings.TrimSpace(req.OIDCClientID)
			clientSecret := req.OIDCClientSecret
			redirectURL := strings.TrimSpace(req.OIDCRedirectURL)
			if issuerURL == "" || clientID == "" || clientSecret == "" || redirectURL == "" {
				http.Error(w, "oidcIssuerUrl, oidcClientId, oidcClientSecret, and oidcRedirectUrl are all required", http.StatusBadRequest)
				return
			}
			// Narrow guard against the exact mistake that broke a real
			// instance: pasting the client id (or any bare string) into the
			// redirect URL field, which was accepted silently and only
			// surfaced as an unrecoverable IdP-side rejection at login. A
			// leading http(s):// is the minimum for a usable callback; deeper
			// validation is the IdP's job (it must register the same value).
			if !strings.HasPrefix(redirectURL, "http://") && !strings.HasPrefix(redirectURL, "https://") {
				http.Error(w, "oidcRedirectUrl must be an http:// or https:// URL", http.StatusBadRequest)
				return
			}
			cipher, err := tokenEnc.Encrypt(clientSecret)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			if err := authStore.SetOIDCConfig(ctx, issuerURL, clientID, cipher, redirectURL); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			// Break-glass key: oidc first-run has no interactive login fallback
			// at setup time (the browser hasn't completed the redirect dance
			// yet), so mint a working recovery credential now, while setup is
			// still public and Configured()-guarded. Env-precedence guardrail:
			// if the env key is active, a settings key would never authenticate
			// (activeKeyHash) — so point at the env value instead of minting a
			// dead key (mirrors Regenerate's own ErrEnvManaged refusal).
			//
			// CRITICAL ORDERING: this ENTIRE block runs BEFORE SetAuthMode(ctx,
			// auth.ModeOIDC) below — a mint/persist failure here must leave
			// Configured()==false so the operator gets a clean retry, not a
			// half-configured, permanently-locked instance. Do not move this
			// after SetAuthMode for any reason.
			resp := authSetupResponse{}
			if authStore.EnvKeyActive() {
				resp.APIKeyNote = "SAKMS_API_KEY is set — that environment value is your break-glass credential. Send it as an X-Api-Key header to reach Settings if SSO login is unavailable."
			} else {
				// Regenerate, NOT EnsureAPIKey — boot already persisted a key,
				// so EnsureAPIKey would return "" here and reveal nothing.
				// Regenerate always mints+returns a working key (invalidating
				// the boot-logged one, which only ever hit stdout on this fresh
				// instance).
				rawKey, _, keyErr := authStore.Regenerate(ctx)
				if keyErr != nil {
					http.Error(w, keyErr.Error(), http.StatusInternalServerError)
					return // Configured() still false here — auth_mode not yet written. Clean retry.
				}
				resp.APIKey = rawKey
			}

			if err := authStore.SetAuthMode(ctx, auth.ModeOIDC); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			// No session cookie here — this POST isn't the browser completing
			// the OIDC redirect dance. The frontend navigates to
			// /api/auth/oidc/login next to establish a session; the break-glass
			// key is shown ONCE here (G6), never retrievable again.
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(resp)
		default:
			http.Error(w, "unknown auth mode", http.StatusBadRequest)
		}
	}
}

func authLoginHandler(authStore *auth.Store, tokenEnc auth.TokenEncryptor) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		mode, err := authStore.AuthMode(ctx)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if mode != auth.ModePassword {
			// Username/password form login is only meaningful in password
			// mode. In "oidc" mode the login IS the IdP redirect dance
			// (/api/auth/oidc/login), and "none" has nothing to log into —
			// minting a cookie here for either would be wrong.
			http.Error(w, "login is not applicable in the current auth mode", http.StatusBadRequest)
			return
		}

		var req authCredentialsRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}

		ok, err := authStore.Verify(ctx, req.Username, req.Password)
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
		auth.SetSessionCookie(w, token, false)
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
	Configured    bool   `json:"configured"`
	Authenticated bool   `json:"authenticated"`
	Mode          string `json:"mode"`
}

// authStatusHandler is the one endpoint the frontend calls before it knows
// anything else about the instance — it decides which of "create your
// login," "log in," "log in with SSO," or "proceed" to show. Authenticated is
// computed relative to the active mode: "none" is always true (nothing to
// check); "password" and "oidc" both use the same cheap local session-cookie
// check (oidc mode issues that same cookie once the IdP redirect completes,
// so there's nothing mode-specific to check here and — unlike the old
// authentik introspection heuristic — no outbound call and no amplification
// concern). The real, fully-enforced gate remains auth.Middleware's
// per-request check on every actual protected API request.
func authStatusHandler(authStore *auth.Store, tokenEnc auth.TokenEncryptor) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		configured, err := authStore.Configured(ctx)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		mode, err := authStore.AuthMode(ctx)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		var authenticated bool
		switch mode {
		case auth.ModeNone:
			authenticated = true
		default: // password, oidc, and any unknown mode all gate on the session cookie
			authenticated = auth.Authenticated(tokenEnc, r)
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(authStatusResponse{
			Configured:    configured,
			Authenticated: authenticated,
			Mode:          mode,
		})
	}
}
