package api

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/labbersanon/sakms/internal/auth"
)

// --- Config mux (GET/PUT /api/auth/oidc) ---

func TestOIDCConfig_GetPutRoundTrip(t *testing.T) {
	authStore, secretStore := testAuthStore(t)
	srv := httptest.NewServer(NewOIDCMux(authStore, secretStore))
	defer srv.Close()

	putBody, _ := json.Marshal(oidcConfigRequest{
		IssuerURL:    "https://sso.example.com",
		ClientID:     "the-client-id",
		ClientSecret: "the-client-secret",
		RedirectURL:  "https://sak.example.com/api/auth/oidc/callback",
	})
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/auth/oidc", bytes.NewReader(putBody))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204 from PUT, got %d", resp.StatusCode)
	}

	getResp, err := http.Get(srv.URL + "/api/auth/oidc")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer getResp.Body.Close()
	var status oidcStatusResponse
	json.NewDecoder(getResp.Body).Decode(&status)
	if status.IssuerURL != "https://sso.example.com" || status.ClientID != "the-client-id" || status.RedirectURL != "https://sak.example.com/api/auth/oidc/callback" {
		t.Errorf("GET did not reflect the persisted config: %+v", status)
	}
	if !status.HasSecret {
		t.Error("expected hasSecret=true after PUT with a client secret")
	}
}

// TestOIDCConfig_GetNeverLeaksSecret confirms the GET status response has no
// field that could carry the client secret's plaintext or ciphertext.
func TestOIDCConfig_GetNeverLeaksSecret(t *testing.T) {
	authStore, secretStore := testAuthStore(t)
	ctx := context.Background()
	cipher, err := secretStore.Encrypt("super-secret-value")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := authStore.SetOIDCConfig(ctx, "https://sso.example.com", "cid", cipher, "https://x/cb"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	srv := httptest.NewServer(NewOIDCMux(authStore, secretStore))
	defer srv.Close()

	getResp, err := http.Get(srv.URL + "/api/auth/oidc")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer getResp.Body.Close()
	var raw map[string]any
	json.NewDecoder(getResp.Body).Decode(&raw)
	body, _ := json.Marshal(raw)
	if bytes.Contains(body, []byte("super-secret-value")) || bytes.Contains(body, []byte(cipher)) {
		t.Errorf("GET status leaked the client secret (plaintext or ciphertext): %s", body)
	}
}

func TestOIDCConfig_PutMissingFields_400(t *testing.T) {
	cases := []struct {
		name string
		req  oidcConfigRequest
	}{
		{"no issuer", oidcConfigRequest{ClientID: "c", ClientSecret: "s", RedirectURL: "https://x/cb"}},
		{"no client id", oidcConfigRequest{IssuerURL: "https://i", ClientSecret: "s", RedirectURL: "https://x/cb"}},
		{"no secret", oidcConfigRequest{IssuerURL: "https://i", ClientID: "c", RedirectURL: "https://x/cb"}},
		{"no redirect", oidcConfigRequest{IssuerURL: "https://i", ClientID: "c", ClientSecret: "s"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			authStore, secretStore := testAuthStore(t)
			srv := httptest.NewServer(NewOIDCMux(authStore, secretStore))
			defer srv.Close()

			body, _ := json.Marshal(tc.req)
			req, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/auth/oidc", bytes.NewReader(body))
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("expected 400 for %s, got %d", tc.name, resp.StatusCode)
			}
		})
	}
}

// TestOIDCConfig_PutNonURLRedirect_400 covers the exact incident this
// validation exists for: a client-id-shaped string pasted into the redirect
// URL field. It's non-empty (so it clears the required check) but isn't a
// URL, and must be rejected rather than silently persisted.
func TestOIDCConfig_PutNonURLRedirect_400(t *testing.T) {
	authStore, secretStore := testAuthStore(t)
	srv := httptest.NewServer(NewOIDCMux(authStore, secretStore))
	defer srv.Close()

	body, _ := json.Marshal(oidcConfigRequest{
		IssuerURL:    "https://sso.example.com",
		ClientID:     "the-client-id",
		ClientSecret: "the-client-secret",
		RedirectURL:  "the-client-id", // not a URL — the mistake that broke a real instance
	})
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/auth/oidc", bytes.NewReader(body))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for a non-URL redirect, got %d", resp.StatusCode)
	}
}

// TestOIDCMux_ProtectedByMiddleware asserts the config mux carries no auth
// authority of its own — cmd/sakms wraps it in auth.Middleware.
func TestOIDCMux_ProtectedByMiddleware(t *testing.T) {
	authStore, secretStore := testAuthStore(t)
	protected := auth.Middleware(secretStore, authStore, NewOIDCMux(authStore, secretStore))
	srv := httptest.NewServer(protected)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/auth/oidc")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 for an unauthenticated request to the protected oidc config mux, got %d", resp.StatusCode)
	}
}

// --- Full flow (login -> callback) against a local test IdP ---

// testIdP is a minimal in-process OpenID Connect provider: it serves a
// discovery document, a JWKS endpoint (one RSA key), and a token endpoint
// that returns a signed ID token. The per-test knobs (nonce, expiry, audience,
// signWithWrongKey) let a single provider exercise the happy path plus each
// rejection case without a full mock IdP.
type testIdP struct {
	server   *httptest.Server
	key      *rsa.PrivateKey
	wrongKey *rsa.PrivateKey
	kid      string

	// knobs set by the test before driving the callback:
	nonce            string
	audience         string    // defaults to clientID when empty
	expiry           time.Time // defaults to now+1h when zero
	signWithWrongKey bool
}

const testClientID = "the-client-id"

func newTestIdP(t *testing.T) *testIdP {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generating idp key: %v", err)
	}
	wrongKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generating wrong key: %v", err)
	}
	idp := &testIdP{key: key, wrongKey: wrongKey, kid: "test-key-1"}

	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		issuer := idp.server.URL
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"issuer":                                issuer,
			"authorization_endpoint":                issuer + "/authorize",
			"token_endpoint":                        issuer + "/token",
			"jwks_uri":                              issuer + "/jwks",
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"keys": []map[string]any{{
				"kty": "RSA",
				"use": "sig",
				"alg": "RS256",
				"kid": idp.kid,
				"n":   base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
				"e":   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes()),
			}},
		})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		aud := idp.audience
		if aud == "" {
			aud = testClientID
		}
		exp := idp.expiry
		if exp.IsZero() {
			exp = time.Now().Add(time.Hour)
		}
		claims := map[string]any{
			"iss":   idp.server.URL,
			"sub":   "operator-subject",
			"aud":   aud,
			"exp":   exp.Unix(),
			"iat":   time.Now().Unix(),
			"nonce": idp.nonce,
		}
		signingKey := idp.key
		if idp.signWithWrongKey {
			signingKey = idp.wrongKey
		}
		idToken := signRS256(t, signingKey, idp.kid, claims)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"access_token": "test-access-token",
			"token_type":   "Bearer",
			"id_token":     idToken,
		})
	})

	idp.server = httptest.NewServer(mux)
	t.Cleanup(idp.server.Close)
	return idp
}

// signRS256 builds a compact JWS (RS256) by hand — no jose dependency needed
// for a test token.
func signRS256(t *testing.T, key *rsa.PrivateKey, kid string, claims map[string]any) string {
	t.Helper()
	header := map[string]any{"alg": "RS256", "typ": "JWT", "kid": kid}
	hb, _ := json.Marshal(header)
	cb, _ := json.Marshal(claims)
	signingInput := base64.RawURLEncoding.EncodeToString(hb) + "." + base64.RawURLEncoding.EncodeToString(cb)
	sum := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	if err != nil {
		t.Fatalf("signing test id token: %v", err)
	}
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(sig)
}

// oidcFlowServer wires an auth mux backed by a store configured for oidc mode
// against idp, and returns a redirect-suppressing client.
func oidcFlowServer(t *testing.T, idp *testIdP) (*httptest.Server, *http.Client) {
	t.Helper()
	authStore, secretStore := testAuthStore(t)
	ctx := context.Background()
	cipher, err := secretStore.Encrypt("the-client-secret")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := authStore.SetOIDCConfig(ctx, idp.server.URL, testClientID, cipher, "https://sak.example.com/api/auth/oidc/callback"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := authStore.SetAuthMode(ctx, auth.ModeOIDC); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	srv := httptest.NewServer(NewAuthMux(authStore, secretStore))
	t.Cleanup(srv.Close)
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return srv, client
}

// startLogin drives GET /api/auth/oidc/login and returns the flow cookie and
// its decoded {state, nonce}.
func startLogin(t *testing.T, srv *httptest.Server, client *http.Client) (*http.Cookie, oidcFlowState) {
	t.Helper()
	resp, err := client.Get(srv.URL + "/api/auth/oidc/login")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("expected 302 redirect to the IdP, got %d", resp.StatusCode)
	}
	var flowCookie *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == oidcFlowCookie {
			flowCookie = c
		}
	}
	if flowCookie == nil {
		t.Fatal("expected a flow cookie to be set by the login handler")
	}
	data, err := base64.RawURLEncoding.DecodeString(flowCookie.Value)
	if err != nil {
		t.Fatalf("decoding flow cookie: %v", err)
	}
	var flow oidcFlowState
	if err := json.Unmarshal(data, &flow); err != nil {
		t.Fatalf("unmarshaling flow cookie: %v", err)
	}
	if flow.State == "" || flow.Nonce == "" || flow.Verifier == "" {
		t.Fatalf("expected non-empty state/nonce/verifier in the flow cookie, got %+v", flow)
	}
	return flowCookie, flow
}

func callback(t *testing.T, srv *httptest.Server, client *http.Client, flowCookie *http.Cookie, state string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/auth/oidc/callback?code=test-auth-code&state="+state, nil)
	if flowCookie != nil {
		req.AddCookie(flowCookie)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	return resp
}

func hasSessionCookie(resp *http.Response) bool {
	for _, c := range resp.Cookies() {
		if c.Name == auth.CookieName && c.Value != "" {
			return true
		}
	}
	return false
}

// assertAuthErrorRedirect asserts the callback failed closed the way a
// top-level browser navigation needs it to: a 302 back to the SPA with the
// specific ?auth_error=<code> reason (not a plain-text dead end), and no
// session cookie. Checking the exact code — not just "302, no cookie" — keeps
// these tests honest: a failure path that wrongly redirected to "/" (or with
// the wrong reason) would otherwise pass.
func assertAuthErrorRedirect(t *testing.T, resp *http.Response, wantCode string) {
	t.Helper()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("expected 302 redirect on a failed callback, got %d", resp.StatusCode)
	}
	loc := resp.Header.Get("Location")
	if loc != "/?auth_error="+wantCode {
		t.Fatalf("expected redirect to /?auth_error=%s, got %q", wantCode, loc)
	}
	if hasSessionCookie(resp) {
		t.Error("no session cookie must be issued on a failed callback")
	}
}

func TestOIDCFlow_HappyPath(t *testing.T) {
	idp := newTestIdP(t)
	srv, client := oidcFlowServer(t, idp)

	flowCookie, flow := startLogin(t, srv, client)
	idp.nonce = flow.Nonce // the IdP embeds the nonce it received in the ID token

	resp := callback(t, srv, client, flowCookie, flow.State)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("expected 302 back to the app on a successful callback, got %d", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); loc != "/" {
		t.Errorf("expected redirect to \"/\", got %q", loc)
	}
	if !hasSessionCookie(resp) {
		t.Error("expected a session cookie to be issued after a successful OIDC callback")
	}
}

func TestOIDCFlow_StateMismatch_Rejected(t *testing.T) {
	idp := newTestIdP(t)
	srv, client := oidcFlowServer(t, idp)

	flowCookie, flow := startLogin(t, srv, client)
	idp.nonce = flow.Nonce

	resp := callback(t, srv, client, flowCookie, "not-the-real-state")
	defer resp.Body.Close()
	assertAuthErrorRedirect(t, resp, "state_mismatch")
}

func TestOIDCFlow_NonceMismatch_Rejected(t *testing.T) {
	idp := newTestIdP(t)
	srv, client := oidcFlowServer(t, idp)

	flowCookie, flow := startLogin(t, srv, client)
	idp.nonce = "a-different-nonce-than-was-requested" // deliberate mismatch

	resp := callback(t, srv, client, flowCookie, flow.State)
	defer resp.Body.Close()
	// A nonce mismatch fails inside client.Exchange (token verification), so
	// it surfaces as exchange_failed — not a distinct callback branch.
	assertAuthErrorRedirect(t, resp, "exchange_failed")
}

func TestOIDCFlow_ExpiredToken_Rejected(t *testing.T) {
	idp := newTestIdP(t)
	srv, client := oidcFlowServer(t, idp)

	flowCookie, flow := startLogin(t, srv, client)
	idp.nonce = flow.Nonce
	idp.expiry = time.Now().Add(-time.Hour) // already expired

	resp := callback(t, srv, client, flowCookie, flow.State)
	defer resp.Body.Close()
	assertAuthErrorRedirect(t, resp, "exchange_failed")
}

func TestOIDCFlow_BadSignature_Rejected(t *testing.T) {
	idp := newTestIdP(t)
	srv, client := oidcFlowServer(t, idp)

	flowCookie, flow := startLogin(t, srv, client)
	idp.nonce = flow.Nonce
	idp.signWithWrongKey = true // signed with a key not in the JWKS

	resp := callback(t, srv, client, flowCookie, flow.State)
	defer resp.Body.Close()
	assertAuthErrorRedirect(t, resp, "exchange_failed")
}

func TestOIDCFlow_WrongAudience_Rejected(t *testing.T) {
	idp := newTestIdP(t)
	srv, client := oidcFlowServer(t, idp)

	flowCookie, flow := startLogin(t, srv, client)
	idp.nonce = flow.Nonce
	idp.audience = "some-other-client" // audience != our client id

	resp := callback(t, srv, client, flowCookie, flow.State)
	defer resp.Body.Close()
	assertAuthErrorRedirect(t, resp, "exchange_failed")
}

func TestOIDCFlow_NoFlowCookie_Rejected(t *testing.T) {
	idp := newTestIdP(t)
	srv, client := oidcFlowServer(t, idp)

	// Never call startLogin — hit the callback cold, with no flow cookie.
	resp := callback(t, srv, client, nil, "some-state")
	defer resp.Body.Close()
	assertAuthErrorRedirect(t, resp, "no_flow")
}

// TestOIDCFlow_IdPError_Redirects covers the new path where the IdP bounces
// the browser back with its own ?error= (e.g. access_denied) instead of a
// code — checked before the flow/state logic, so even a valid state doesn't
// let it fall through to the token exchange.
func TestOIDCFlow_IdPError_Redirects(t *testing.T) {
	idp := newTestIdP(t)
	srv, client := oidcFlowServer(t, idp)

	flowCookie, flow := startLogin(t, srv, client)

	// A valid flow cookie and matching state, but the IdP returned an error
	// instead of a code — must still redirect to idp_error, not attempt an
	// exchange with an empty code.
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/auth/oidc/callback?error=access_denied&error_description=user+cancelled&state="+flow.State, nil)
	req.AddCookie(flowCookie)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	assertAuthErrorRedirect(t, resp, "idp_error")
}
