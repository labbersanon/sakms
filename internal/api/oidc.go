package api

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"time"

	"golang.org/x/oauth2"

	"github.com/curtiswtaylorjr/sakms/internal/auth"
)

// oidcFlowCookie carries the per-login CSRF/PKCE state across the redirect to
// the IdP and back. It is scoped to /api/auth/oidc so it never rides along on
// ordinary API requests, short-lived (the whole login round-trip is seconds),
// and HttpOnly+Secure+SameSite=Lax: Lax is required (not Strict) so the
// cookie is still sent on the top-level GET navigation the IdP bounces the
// browser back with. It carries no secret that needs encryption — state,
// nonce, and the PKCE verifier are all single-use random values whose only
// requirement is that they're server-set and unguessable (a double-submit
// pattern: the callback compares the cookie's state against the query-param
// state).
const oidcFlowCookie = "sakms_oidc_flow"

// oidcFlowTTL bounds how long a started-but-uncompleted login stays valid.
const oidcFlowTTL = 10 * time.Minute

// oidcFlowState is the JSON payload stashed in the flow cookie (base64url of
// this struct). The PKCE verifier lives here, never in a query param, so it
// is never exposed to the IdP or the browser's address bar.
type oidcFlowState struct {
	State    string `json:"state"`
	Nonce    string `json:"nonce"`
	Verifier string `json:"verifier"`
	// IssuedAt lets the callback enforce oidcFlowTTL server-side, not just
	// via the cookie's own Expires/MaxAge (Finding 3, 2026-07-11 OIDC
	// security review) — a cookie jar that ignores expiry (or a stored/
	// replayed cookie value) can't extend a flow past its real TTL.
	IssuedAt int64 `json:"issuedAt"`
}

// NewOIDCMux returns the oidc-mode config routes: GET status, PUT
// issuer/client id/client secret/redirect. Kept on its own small mux,
// mirroring NewAuthModeMux/NewAPIKeyMux's precedent — this mutates
// security-relevant state (or, for GET, exposes config that must never
// include the secret itself), so it must be session-protected. cmd/sakms
// wraps it in the same auth.Middleware as the other protected muxes.
//
// This is SEPARATE from the first-run bootstrap path (authSetupHandler's
// "oidc" branch in auth.go) and from the public login/callback redirect legs
// (NewAuthMux). These routes change an ALREADY-configured instance's OIDC
// config (the Settings panel's "switch to OIDC" or "rotate credentials"
// actions), reachable only once the operator already holds a session cookie
// or the universal API key.
func NewOIDCMux(authStore *auth.Store, secretEnc auth.TokenEncryptor) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/auth/oidc", oidcGetHandler(authStore))
	mux.HandleFunc("PUT /api/auth/oidc", oidcPutHandler(authStore, secretEnc))
	return mux
}

// oidcStatusResponse never includes the client secret itself (G6) — only the
// issuer, client id, redirect URL, and whether a secret is currently
// configured.
type oidcStatusResponse struct {
	IssuerURL   string `json:"issuerUrl"`
	ClientID    string `json:"clientId"`
	RedirectURL string `json:"redirectUrl"`
	HasSecret   bool   `json:"hasSecret"`
}

func oidcGetHandler(authStore *auth.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		issuerURL, clientID, cipher, redirectURL, err := authStore.OIDCConfig(r.Context())
		if err != nil {
			log.Printf("oidc status: %v", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(oidcStatusResponse{
			IssuerURL:   issuerURL,
			ClientID:    clientID,
			RedirectURL: redirectURL,
			HasSecret:   cipher != "",
		})
	}
}

type oidcConfigRequest struct {
	IssuerURL    string `json:"issuerUrl"`
	ClientID     string `json:"clientId"`
	ClientSecret string `json:"clientSecret"`
	RedirectURL  string `json:"redirectUrl"`
}

// oidcPutHandler encrypts ClientSecret via secretEnc before it ever reaches
// settings.Set — the client secret is an outbound credential SAK presents to
// the IdP at token-exchange time (encrypted at rest, decrypted only then),
// not a one-way hash like the password.
func oidcPutHandler(authStore *auth.Store, secretEnc auth.TokenEncryptor) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req oidcConfigRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		issuerURL := strings.TrimSpace(req.IssuerURL)
		clientID := strings.TrimSpace(req.ClientID)
		clientSecret := req.ClientSecret
		redirectURL := strings.TrimSpace(req.RedirectURL)
		if issuerURL == "" || clientID == "" || clientSecret == "" || redirectURL == "" {
			http.Error(w, "issuerUrl, clientId, clientSecret, and redirectUrl are all required", http.StatusBadRequest)
			return
		}
		cipher, err := secretEnc.Encrypt(clientSecret)
		if err != nil {
			log.Printf("oidc config encrypt: %v", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if err := authStore.SetOIDCConfig(r.Context(), issuerURL, clientID, cipher, redirectURL); err != nil {
			log.Printf("oidc config set: %v", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// oidcLoginHandler starts the Authorization Code flow: it mints a fresh
// state, nonce, and PKCE verifier (all crypto/rand-backed), stashes them in
// the short-lived flow cookie, then redirects the browser to the IdP's
// authorization endpoint. Public (no session) by necessity — the whole point
// is to establish a session where none exists yet.
func oidcLoginHandler(authStore *auth.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		client, err := authStore.OIDCClient(r.Context())
		if err != nil {
			log.Printf("oidc login: %v", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if client == nil {
			http.Error(w, "oidc is not configured", http.StatusBadRequest)
			return
		}

		state, err := randToken()
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		nonce, err := randToken()
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		verifier := oauth2.GenerateVerifier()

		flow := oidcFlowState{State: state, Nonce: nonce, Verifier: verifier, IssuedAt: time.Now().Unix()}
		if err := setFlowCookie(w, flow); err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, client.AuthCodeURL(state, nonce, verifier), http.StatusFound)
	}
}

// oidcCallbackHandler completes the flow: it reads and clears the flow
// cookie, verifies the returned state matches (CSRF), exchanges the code for
// tokens using the PKCE verifier, verifies the ID token (issuer/audience/
// signature/expiry) and that its nonce matches, and on success issues the
// SAME signed session cookie password mode uses, then redirects to the app.
// Any failure at any step fails closed: a clear error, no session cookie.
func oidcCallbackHandler(authStore *auth.Store, tokenEnc auth.TokenEncryptor) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		flow, err := readFlowCookie(r)
		clearFlowCookie(w) // single-use: clear regardless of outcome
		if err != nil {
			http.Error(w, "no active login flow — start again at /api/auth/oidc/login", http.StatusBadRequest)
			return
		}
		// Defense-in-depth (Finding 4, 2026-07-11 review): a well-formed but
		// degenerate flow cookie (any field empty) is rejected outright,
		// rather than relying on the state-compare or the IdP exchange to
		// catch it incidentally.
		if flow.State == "" || flow.Nonce == "" || flow.Verifier == "" {
			http.Error(w, "no active login flow — start again at /api/auth/oidc/login", http.StatusBadRequest)
			return
		}
		// Server-side TTL enforcement (Finding 3) — don't rely solely on the
		// cookie's own Expires/MaxAge.
		if time.Since(time.Unix(flow.IssuedAt, 0)) > oidcFlowTTL {
			http.Error(w, "login flow expired — start again at /api/auth/oidc/login", http.StatusBadRequest)
			return
		}
		// CSRF: the state echoed back by the IdP must match the one we planted
		// in the cookie. Compared before any expensive/outbound work.
		if r.URL.Query().Get("state") != flow.State {
			http.Error(w, "state mismatch", http.StatusBadRequest)
			return
		}
		code := r.URL.Query().Get("code")
		if code == "" {
			http.Error(w, "missing authorization code", http.StatusBadRequest)
			return
		}

		client, err := authStore.OIDCClient(r.Context())
		if err != nil {
			log.Printf("oidc callback: %v", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if client == nil {
			http.Error(w, "oidc is not configured", http.StatusBadRequest)
			return
		}

		if _, err := client.Exchange(r.Context(), code, flow.Verifier, flow.Nonce); err != nil {
			// Fail closed: a bad code, a signature/issuer/audience/expiry
			// failure, or a nonce mismatch all land here with no session
			// issued. The identity in the token is deliberately not inspected
			// beyond verification — completing the flow IS the one operator
			// authenticating (single-operator model; who may complete the IdP
			// login is the IdP's job).
			log.Printf("oidc callback verify: %v", err)
			http.Error(w, "authentication failed", http.StatusUnauthorized)
			return
		}

		token, err := auth.IssueToken(tokenEnc)
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		auth.SetSessionCookie(w, token, true)
		http.Redirect(w, r, "/", http.StatusFound)
	}
}

// randToken returns 32 bytes of crypto/rand, base64url-encoded — an
// unguessable single-use value for the CSRF state and OIDC nonce.
func randToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func setFlowCookie(w http.ResponseWriter, flow oidcFlowState) error {
	data, err := json.Marshal(flow)
	if err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name:     oidcFlowCookie,
		Value:    base64.RawURLEncoding.EncodeToString(data),
		Path:     "/api/auth/oidc",
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
		Expires:  time.Now().Add(oidcFlowTTL),
		MaxAge:   int(oidcFlowTTL.Seconds()),
	})
	return nil
}

func readFlowCookie(r *http.Request) (oidcFlowState, error) {
	var flow oidcFlowState
	cookie, err := r.Cookie(oidcFlowCookie)
	if err != nil {
		return flow, err
	}
	data, err := base64.RawURLEncoding.DecodeString(cookie.Value)
	if err != nil {
		return flow, err
	}
	if err := json.Unmarshal(data, &flow); err != nil {
		return flow, err
	}
	return flow, nil
}

func clearFlowCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     oidcFlowCookie,
		Value:    "",
		Path:     "/api/auth/oidc",
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
		Expires:  time.Unix(0, 0),
		MaxAge:   -1,
	})
}
