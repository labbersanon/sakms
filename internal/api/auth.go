package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/curtiswtaylorjr/sakms/internal/auth"
)

// minForwardSecretLen bounds an operator-supplied forward-auth secret
// (Phase 4 fix-up). It's the entire authorization gate for forward mode, so
// a one-character value must not be silently accepted the way the
// crypto/rand-generated default (32 bytes) never would be. Chosen to match
// a reasonable shared-secret floor, not tied to any particular algorithm.
const minForwardSecretLen = 16

// NewAuthMux returns the handful of routes that must stay reachable without
// a session — setup, login, logout, and status — kept on their OWN mux,
// deliberately separate from NewMux's business-logic routes. cmd/sakms
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
	// Mode selects the auth strategy at first run — "" means "password"
	// (today's exact back-compat behavior).
	Mode string `json:"mode"`
	// AcknowledgeInsecure must be true to select Mode "none" — a genuine
	// no-auth instance requires an explicit, unmissable opt-in (G2).
	AcknowledgeInsecure bool `json:"acknowledgeInsecure"`
	// ForwardSecret is optional for a "forward"-mode setup request — if
	// empty, the handler generates one server-side (simpler UX than
	// requiring the frontend to do its own crypto-random generation). If
	// non-empty, the operator-supplied value is persisted as-is. Ignored
	// for every other mode.
	ForwardSecret string `json:"forwardSecret,omitempty"`
	// AuthentikURL/AuthentikClientID/AuthentikClientSecret are required
	// together for a "authentik"-mode setup request (plan §0.7/§3.3b) —
	// carried in this public setup body, not a protected config endpoint,
	// because no credential exists yet to authenticate against one. This
	// mode's real audience is scripts/API clients calling the API
	// directly, not the browser setup wizard (see CLAUDE.md/the slice
	// plan's §4.1 human decision) — the frontend first-run selector never
	// offers it, but the API must still accept it. Ignored for every
	// other mode.
	AuthentikURL          string `json:"authentikUrl,omitempty"`
	AuthentikClientID     string `json:"authentikClientId,omitempty"`
	AuthentikClientSecret string `json:"authentikClientSecret,omitempty"`
}

// authSetupResponse is the JSON body returned by authSetupHandler for modes
// that must hand something back to the caller — currently just "forward",
// whose generated-or-accepted shared secret is revealed here ONCE (G6) so
// the operator can copy it into their reverse-proxy config immediately.
// Empty for "password"/"none", which still respond with a bare 204.
type authSetupResponse struct {
	ForwardSecret string `json:"forwardSecret,omitempty"`
	// APIKey is the one-time break-glass API key minted during a forward-mode
	// first-run (spec §4/Decision #3) — revealed ONCE here, same discipline as
	// ForwardSecret. Empty when SAKMS_API_KEY is active (see APIKeyNote).
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
			auth.SetSessionCookie(w, token)
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
		case auth.ModeForward:
			// First-run bootstrap (plan §0.7/§2.2b): carried in this same
			// public setup body, not a protected config endpoint, because
			// no credential exists yet to authenticate against one. Generate
			// a secret server-side unless the operator supplied their own;
			// persist it, THEN write auth_mode — atomically, one request.
			rawSecret := strings.TrimSpace(req.ForwardSecret)
			if rawSecret == "" {
				generated, genErr := authStore.GenerateForwardSecret(ctx)
				if genErr != nil {
					http.Error(w, genErr.Error(), http.StatusInternalServerError)
					return
				}
				rawSecret = generated
			} else {
				// Phase 4 fix-up: an operator-supplied secret is the entire
				// authorization gate for forward mode — unlike the generated
				// path (32 bytes crypto/rand), nothing else enforces its
				// strength. Reject anything too short to be a meaningful
				// shared secret rather than silently accepting e.g. "x".
				if len(rawSecret) < minForwardSecretLen {
					http.Error(w, fmt.Sprintf("forwardSecret must be at least %d characters", minForwardSecretLen), http.StatusBadRequest)
					return
				}
				if err := authStore.SetForwardSecret(ctx, rawSecret); err != nil {
					http.Error(w, err.Error(), http.StatusInternalServerError)
					return
				}
			}
			// Break-glass key (spec §4/Decision #3): forward first-run has no
			// interactive login fallback, so mint a working recovery credential
			// now, while setup is still public and Configured()-guarded.
			// Env-precedence guardrail: if the env key is active, a settings
			// key would never authenticate (activeKeyHash) — so point at the
			// env value instead of minting a dead key (mirrors Regenerate's own
			// ErrEnvManaged refusal).
			//
			// CRITICAL ORDERING (Critic fix #1): this ENTIRE block runs BEFORE
			// SetAuthMode(ctx, auth.ModeForward) below — a mint/persist failure
			// here must leave Configured()==false so the operator gets a clean
			// retry, not a half-configured, permanently-locked instance. Do not
			// move this after SetAuthMode for any reason.
			resp := authSetupResponse{ForwardSecret: rawSecret}
			if authStore.EnvKeyActive() {
				resp.APIKeyNote = "SAKMS_API_KEY is set — that environment value is your break-glass credential. Send it as an X-Api-Key header to reach Settings if your proxy locks you out."
			} else {
				// Regenerate, NOT EnsureAPIKey — see plan §0: boot already
				// persisted a key (main.go:92), so EnsureAPIKey would return ""
				// here and reveal nothing. Regenerate always mints+returns a
				// working key (invalidating the boot-logged one, which only ever
				// hit stdout on this fresh instance).
				rawKey, _, keyErr := authStore.Regenerate(ctx)
				if keyErr != nil {
					http.Error(w, keyErr.Error(), http.StatusInternalServerError)
					return // Configured() still false here — auth_mode not yet written. Clean retry.
				}
				resp.APIKey = rawKey
			}

			if err := authStore.SetAuthMode(ctx, auth.ModeForward); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			// No cookie — forward mode has no cookie concept. The secret and
			// break-glass key are shown ONCE here (G6); there is no later
			// endpoint that can ever retrieve either again.
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(resp)
		case auth.ModeAuthentik:
			// First-run bootstrap (plan §0.7/§3.3b): carried in this same
			// public setup body, not a protected config endpoint, because no
			// credential exists yet to authenticate against one. All three
			// fields are required together — unlike forward mode, SAK has no
			// server-generated fallback here (the operator already has a
			// client id/secret from Authentik's own UI).
			url := strings.TrimSpace(req.AuthentikURL)
			clientID := strings.TrimSpace(req.AuthentikClientID)
			clientSecret := req.AuthentikClientSecret
			if url == "" || clientID == "" || clientSecret == "" {
				http.Error(w, "authentikUrl, authentikClientId, and authentikClientSecret are all required", http.StatusBadRequest)
				return
			}
			cipher, err := tokenEnc.Encrypt(clientSecret)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			if err := authStore.SetAuthentikConfig(ctx, url, clientID, cipher); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			if err := authStore.SetAuthMode(ctx, auth.ModeAuthentik); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			// No cookie — authentik mode has no cookie concept either. No
			// secret echoed back (G6): unlike forward mode's
			// server-generated secret, the operator already holds their own
			// copy of the client secret from Authentik's own UI, so there's
			// nothing new to reveal here.
			w.WriteHeader(http.StatusNoContent)
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
			// No cookie concept in forward/authentik/none — minting one
			// here would create exactly the stale-cookie path Edge Case #3
			// forbids.
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
	Configured           bool   `json:"configured"`
	Authenticated        bool   `json:"authenticated"`
	Mode                 string `json:"mode"`
	ProxyHeadersDetected bool   `json:"proxyHeadersDetected,omitempty"`
}

// authStatusHandler is the one endpoint the frontend calls before it knows
// anything else about the instance — it decides which of "create your
// login," "log in," or "proceed" to show. Authenticated is computed
// relative to the active mode: "none" is always true (nothing to check),
// "password" is today's cookie check unchanged, "forward" calls
// auth.ForwardAuth directly for a REAL per-request check (plan §3.3's
// critic-fix: safe here because the check is purely local — a settings
// read + constant-time compare, no outbound call, no amplification
// concern). "authentik" is DELIBERATELY DIFFERENT: it uses a presence-only
// heuristic — true iff a non-empty `Authorization: Bearer <token>` header is
// present on THIS status request, false otherwise — and NEVER calls
// auth.AuthentikAuth (which would introspect for real). Calling the real
// check here would let an unauthenticated caller trigger one genuine
// outbound introspection request to Authentik per hit on this public,
// attacker-rate-controlled endpoint — a real amplification vector against
// Authentik itself (plan §3.3's critic-driven fix, scoped to authentik
// only: forward's check has no such concern, since it never leaves the
// process). This is an optimistic signal for boot()-time UI gating only;
// the real, fully-enforced gate remains auth.Middleware's per-request
// AuthentikAuth call on every actual protected API request.
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
		case auth.ModePassword:
			authenticated = auth.Authenticated(tokenEnc, r)
		case auth.ModeForward:
			authenticated, err = auth.ForwardAuth(authStore, r)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		case auth.ModeAuthentik:
			// Presence-only (see doc comment above) — deliberately NOT
			// auth.AuthentikAuth. No store call, no outbound call at all.
			// auth.BearerToken (shared with AuthentikAuth's real check, Phase
			// 4 fix-up) matches the "Bearer" scheme case-insensitively.
			authenticated = auth.BearerToken(r) != ""
		default:
			authenticated = auth.Authenticated(tokenEnc, r)
		}

		// Detection is a first-run-only hint: computed ONLY while unconfigured,
		// so an already-configured instance NEVER discloses it regardless of
		// what headers a caller presents (disclosure-scoping, spec §1). The
		// wizard is the signal's only consumer and is never shown once
		// configured.
		var proxyHeadersDetected bool
		if !configured {
			proxyHeadersDetected = auth.ProxyHeadersPresent(r)
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(authStatusResponse{
			Configured:           configured,
			Authenticated:        authenticated,
			Mode:                 mode,
			ProxyHeadersDetected: proxyHeadersDetected,
		})
	}
}
