package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/labbersanon/sakms/internal/auth"
	"github.com/labbersanon/sakms/internal/db"
	"github.com/labbersanon/sakms/internal/secrets"
	"github.com/labbersanon/sakms/internal/settings"
)

func testAuthStore(t *testing.T) (*auth.Store, *secrets.Store) {
	t.Helper()
	authStore, secretStore, _ := testAuthStoreWithDB(t)
	return authStore, secretStore
}

// testAuthStoreWithDB is testAuthStore plus the raw *sql.DB handle, needed
// by tests that force a real settings-store write/read error (e.g.
// DROP TABLE settings) rather than exercising the happy path — mirrors
// internal/auth/apikey_test.go's newTestStoreWithDB pattern.
func testAuthStoreWithDB(t *testing.T) (*auth.Store, *secrets.Store, *sql.DB) {
	t.Helper()
	sqlDB, err := db.Open(filepath.Join(t.TempDir(), "sakms.db"))
	if err != nil {
		t.Fatalf("opening db: %v", err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	secretStore, err := secrets.New(make([]byte, 32))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// secretStore doubles as authStore's OIDC-client-secret decryptor,
	// mirroring cmd/sakms/main.go's production wiring (the same secretStore
	// instance is passed to both auth.New and api.NewAuthMux/NewOIDCMux).
	return auth.New(settings.New(sqlDB), secretStore, http.DefaultClient), secretStore, sqlDB
}

func TestAuthSetup_CreatesLoginAndLogsIn(t *testing.T) {
	authStore, tokenEnc := testAuthStore(t)
	srv := httptest.NewServer(NewAuthMux(authStore, tokenEnc))
	defer srv.Close()

	body, _ := json.Marshal(authCredentialsRequest{Username: "wade", Password: "correct-horse-battery-staple"})
	resp, err := http.Post(srv.URL+"/api/auth/setup", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", resp.StatusCode)
	}
	if len(resp.Cookies()) == 0 {
		t.Fatal("expected a session cookie to be set after setup")
	}
}

func TestAuthSetup_RejectsSecondCall(t *testing.T) {
	authStore, tokenEnc := testAuthStore(t)
	srv := httptest.NewServer(NewAuthMux(authStore, tokenEnc))
	defer srv.Close()

	body, _ := json.Marshal(authCredentialsRequest{Username: "wade", Password: "first-password"})
	if _, err := http.Post(srv.URL+"/api/auth/setup", "application/json", bytes.NewReader(body)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	takeoverBody, _ := json.Marshal(authCredentialsRequest{Username: "attacker", Password: "attacker-password"})
	resp, err := http.Post(srv.URL+"/api/auth/setup", "application/json", bytes.NewReader(takeoverBody))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("expected 409 refusing to overwrite an existing login, got %d", resp.StatusCode)
	}
}

func TestAuthLogin_Succeeds(t *testing.T) {
	authStore, tokenEnc := testAuthStore(t)
	srv := httptest.NewServer(NewAuthMux(authStore, tokenEnc))
	defer srv.Close()

	setupBody, _ := json.Marshal(authCredentialsRequest{Username: "wade", Password: "the-password"})
	if _, err := http.Post(srv.URL+"/api/auth/setup", "application/json", bytes.NewReader(setupBody)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	loginBody, _ := json.Marshal(authCredentialsRequest{Username: "wade", Password: "the-password"})
	resp, err := http.Post(srv.URL+"/api/auth/login", "application/json", bytes.NewReader(loginBody))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", resp.StatusCode)
	}
	if len(resp.Cookies()) == 0 {
		t.Fatal("expected a session cookie to be set after login")
	}
}

func TestAuthLogin_WrongPasswordRejected(t *testing.T) {
	authStore, tokenEnc := testAuthStore(t)
	srv := httptest.NewServer(NewAuthMux(authStore, tokenEnc))
	defer srv.Close()

	setupBody, _ := json.Marshal(authCredentialsRequest{Username: "wade", Password: "the-password"})
	if _, err := http.Post(srv.URL+"/api/auth/setup", "application/json", bytes.NewReader(setupBody)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	loginBody, _ := json.Marshal(authCredentialsRequest{Username: "wade", Password: "wrong-password"})
	resp, err := http.Post(srv.URL+"/api/auth/login", "application/json", bytes.NewReader(loginBody))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}
}

func TestAuthLogin_NoLoginConfiguredYetRejected(t *testing.T) {
	authStore, tokenEnc := testAuthStore(t)
	srv := httptest.NewServer(NewAuthMux(authStore, tokenEnc))
	defer srv.Close()

	body, _ := json.Marshal(authCredentialsRequest{Username: "wade", Password: "anything"})
	resp, err := http.Post(srv.URL+"/api/auth/login", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 when nothing is configured yet, got %d", resp.StatusCode)
	}
}

func TestAuthStatus_ReflectsConfiguredAndAuthenticated(t *testing.T) {
	authStore, tokenEnc := testAuthStore(t)
	srv := httptest.NewServer(NewAuthMux(authStore, tokenEnc))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/auth/status")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var status authStatusResponse
	json.NewDecoder(resp.Body).Decode(&status)
	resp.Body.Close()
	if status.Configured || status.Authenticated {
		t.Fatalf("expected a fresh instance to report neither configured nor authenticated, got %+v", status)
	}

	setupBody, _ := json.Marshal(authCredentialsRequest{Username: "wade", Password: "the-password"})
	setupResp, err := http.Post(srv.URL+"/api/auth/setup", "application/json", bytes.NewReader(setupBody))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	cookies := setupResp.Cookies()
	setupResp.Body.Close()

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/auth/status", nil)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	resp2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp2.Body.Close()
	var status2 authStatusResponse
	json.NewDecoder(resp2.Body).Decode(&status2)
	if !status2.Configured || !status2.Authenticated {
		t.Fatalf("expected configured+authenticated after setup with the cookie attached, got %+v", status2)
	}
}

// --- Mode-aware setup/status/login (slice 1) ---

func TestSetup_PasswordWritesMode(t *testing.T) {
	authStore, tokenEnc := testAuthStore(t)
	srv := httptest.NewServer(NewAuthMux(authStore, tokenEnc))
	defer srv.Close()

	body, _ := json.Marshal(authCredentialsRequest{Mode: "password", Username: "wade", Password: "correct-horse-battery-staple"})
	resp, err := http.Post(srv.URL+"/api/auth/setup", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", resp.StatusCode)
	}

	mode, err := authStore.AuthMode(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mode != auth.ModePassword {
		t.Errorf("expected auth_mode to be written as %q, got %q", auth.ModePassword, mode)
	}
}

func TestSetup_NoneRequiresAck_400(t *testing.T) {
	authStore, tokenEnc := testAuthStore(t)
	srv := httptest.NewServer(NewAuthMux(authStore, tokenEnc))
	defer srv.Close()

	body, _ := json.Marshal(authCredentialsRequest{Mode: "none"})
	resp, err := http.Post(srv.URL+"/api/auth/setup", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 without acknowledgeInsecure, got %d", resp.StatusCode)
	}

	configured, err := authStore.Configured(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if configured {
		t.Error("expected a rejected none-mode setup to leave the instance unconfigured")
	}
}

func TestSetup_None_NoCookieNoCreds(t *testing.T) {
	authStore, tokenEnc := testAuthStore(t)
	srv := httptest.NewServer(NewAuthMux(authStore, tokenEnc))
	defer srv.Close()

	body, _ := json.Marshal(authCredentialsRequest{Mode: "none", AcknowledgeInsecure: true})
	resp, err := http.Post(srv.URL+"/api/auth/setup", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", resp.StatusCode)
	}
	if len(resp.Cookies()) != 0 {
		t.Errorf("expected no session cookie to be issued for none mode, got %+v", resp.Cookies())
	}

	mode, err := authStore.AuthMode(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mode != auth.ModeNone {
		t.Errorf("expected auth_mode %q, got %q", auth.ModeNone, mode)
	}
	configured, err := authStore.PasswordConfigured(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if configured {
		t.Error("expected none-mode setup to write no password credentials")
	}
}

// TestSetup_OIDCWritesConfigAndMode is the first-run bootstrap end-to-end
// proof for oidc mode: POST /api/auth/setup with mode:"oidc" and all four
// fields, and NO prior credential, must succeed through the PUBLIC setup
// endpoint, persist the config (client secret encrypted), write auth_mode
// atomically, and mint a break-glass key in the response — with NO session
// cookie (the browser hasn't completed the redirect dance yet).
func TestSetup_OIDCWritesConfigAndMode(t *testing.T) {
	authStore, tokenEnc := testAuthStore(t)
	srv := httptest.NewServer(NewAuthMux(authStore, tokenEnc))
	defer srv.Close()

	configuredBefore, err := authStore.Configured(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if configuredBefore {
		t.Fatal("expected a fresh instance to be unconfigured before setup")
	}

	body, _ := json.Marshal(authCredentialsRequest{
		Mode:             "oidc",
		OIDCIssuerURL:    "https://sso.example.com",
		OIDCClientID:     "the-client-id",
		OIDCClientSecret: "the-client-secret",
		OIDCRedirectURL:  "https://sak.example.com/api/auth/oidc/callback",
	})
	resp, err := http.Post(srv.URL+"/api/auth/setup", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 with the break-glass key in the body, got %d", resp.StatusCode)
	}
	if len(resp.Cookies()) != 0 {
		t.Errorf("expected no session cookie for oidc setup, got %+v", resp.Cookies())
	}

	mode, err := authStore.AuthMode(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mode != auth.ModeOIDC {
		t.Errorf("expected auth_mode to be written as %q, got %q", auth.ModeOIDC, mode)
	}
	configuredAfter, err := authStore.Configured(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !configuredAfter {
		t.Fatal("expected the instance to report Configured=true after oidc setup")
	}

	// The client secret must round-trip: the stored config reports it
	// configured, and OIDCConfigured (the mode-switch precondition) is true.
	ok, err := authStore.OIDCConfigured(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Error("expected OIDCConfigured=true after oidc setup")
	}
}

// TestSetup_OIDCMissingFields_400 covers the "all four fields required
// together" validation: any missing field rejects with 400 and leaves the
// instance unconfigured.
func TestSetup_OIDCMissingFields_400(t *testing.T) {
	cases := []struct {
		name string
		req  authCredentialsRequest
	}{
		{"all blank", authCredentialsRequest{Mode: "oidc"}},
		{"no issuer", authCredentialsRequest{Mode: "oidc", OIDCClientID: "c", OIDCClientSecret: "s", OIDCRedirectURL: "https://x/cb"}},
		{"no client id", authCredentialsRequest{Mode: "oidc", OIDCIssuerURL: "https://i", OIDCClientSecret: "s", OIDCRedirectURL: "https://x/cb"}},
		{"no secret", authCredentialsRequest{Mode: "oidc", OIDCIssuerURL: "https://i", OIDCClientID: "c", OIDCRedirectURL: "https://x/cb"}},
		{"no redirect", authCredentialsRequest{Mode: "oidc", OIDCIssuerURL: "https://i", OIDCClientID: "c", OIDCClientSecret: "s"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			authStore, tokenEnc := testAuthStore(t)
			srv := httptest.NewServer(NewAuthMux(authStore, tokenEnc))
			defer srv.Close()

			body, _ := json.Marshal(tc.req)
			resp, err := http.Post(srv.URL+"/api/auth/setup", "application/json", bytes.NewReader(body))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("expected 400 for %s, got %d", tc.name, resp.StatusCode)
			}
			configured, err := authStore.Configured(context.Background())
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if configured {
				t.Error("expected a rejected oidc setup to leave the instance unconfigured")
			}
		})
	}
}

// TestSetup_OIDCNonURLRedirect_400 covers the first-run bootstrap arm of the
// same redirect-URL guard as TestOIDCConfig_PutNonURLRedirect_400: a non-URL
// (client-id-shaped) redirect is rejected and leaves the instance unconfigured
// for a clean retry.
func TestSetup_OIDCNonURLRedirect_400(t *testing.T) {
	authStore, tokenEnc := testAuthStore(t)
	srv := httptest.NewServer(NewAuthMux(authStore, tokenEnc))
	defer srv.Close()

	body, _ := json.Marshal(authCredentialsRequest{
		Mode:             "oidc",
		OIDCIssuerURL:    "https://sso.example.com",
		OIDCClientID:     "the-client-id",
		OIDCClientSecret: "the-client-secret",
		OIDCRedirectURL:  "the-client-id", // not a URL — the mistake that broke a real instance
	})
	resp, err := http.Post(srv.URL+"/api/auth/setup", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for a non-URL redirect, got %d", resp.StatusCode)
	}
	configured, err := authStore.Configured(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if configured {
		t.Error("expected a rejected oidc setup to leave the instance unconfigured")
	}
}

func TestStatus_ReturnsMode(t *testing.T) {
	authStore, tokenEnc := testAuthStore(t)
	srv := httptest.NewServer(NewAuthMux(authStore, tokenEnc))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/auth/status")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var status authStatusResponse
	json.NewDecoder(resp.Body).Decode(&status)
	resp.Body.Close()
	if status.Mode != auth.ModePassword {
		t.Errorf("expected an unconfigured instance to report the default mode %q, got %q", auth.ModePassword, status.Mode)
	}

	body, _ := json.Marshal(authCredentialsRequest{Mode: "none", AcknowledgeInsecure: true})
	setupResp, err := http.Post(srv.URL+"/api/auth/setup", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	setupResp.Body.Close()

	resp2, err := http.Get(srv.URL + "/api/auth/status")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var status2 authStatusResponse
	json.NewDecoder(resp2.Body).Decode(&status2)
	resp2.Body.Close()
	if status2.Mode != auth.ModeNone {
		t.Errorf("expected mode %q after switching to none, got %q", auth.ModeNone, status2.Mode)
	}
	if !status2.Authenticated {
		t.Error("expected authenticated:true in none mode")
	}
}

func TestLogin_RejectedInNonPasswordMode(t *testing.T) {
	authStore, tokenEnc := testAuthStore(t)
	srv := httptest.NewServer(NewAuthMux(authStore, tokenEnc))
	defer srv.Close()

	body, _ := json.Marshal(authCredentialsRequest{Mode: "none", AcknowledgeInsecure: true})
	setupResp, err := http.Post(srv.URL+"/api/auth/setup", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	setupResp.Body.Close()

	loginBody, _ := json.Marshal(authCredentialsRequest{Username: "wade", Password: "anything"})
	resp, err := http.Post(srv.URL+"/api/auth/login", "application/json", bytes.NewReader(loginBody))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 rejecting login in a non-password mode, got %d", resp.StatusCode)
	}
	if len(resp.Cookies()) != 0 {
		t.Errorf("expected no cookie to be minted, got %+v", resp.Cookies())
	}
}

func TestAuthLogout_ClearsCookie(t *testing.T) {
	authStore, tokenEnc := testAuthStore(t)
	srv := httptest.NewServer(NewAuthMux(authStore, tokenEnc))
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/api/auth/logout", "application/json", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204 even with no prior session, got %d", resp.StatusCode)
	}
	if len(resp.Cookies()) == 0 || resp.Cookies()[0].MaxAge >= 0 {
		t.Fatalf("expected a cookie-clearing response, got %+v", resp.Cookies())
	}
}

// TestSetup_NoneMode_SecondCallRejected_409 closes an AC8 gap found during
// slice 5's final coverage audit: TestConfigured_TrueAfterModeSetOnly
// (internal/auth) proves Configured() flips true at the store level once
// auth_mode is set with no password, and TestAuthSetup_RejectsSecondCall
// proves the setup gate doesn't reappear for the PASSWORD path — but
// nothing exercised the full HTTP round trip for a non-password first-run
// mode: does a REAL second POST to /api/auth/setup actually 409 after a
// none-mode setup, i.e. does authSetupHandler's already-configured guard
// really fire off Configured()'s OR-based redefinition for a mode that
// never wrote auth_username? This is the concrete, end-to-end version of
// AC8 ("Configured() returns true after a non-password mode is chosen at
// first run — the setup gate does not reappear").
func TestSetup_NoneMode_SecondCallRejected_409(t *testing.T) {
	authStore, tokenEnc := testAuthStore(t)
	srv := httptest.NewServer(NewAuthMux(authStore, tokenEnc))
	defer srv.Close()

	body, _ := json.Marshal(authCredentialsRequest{Mode: "none", AcknowledgeInsecure: true})
	resp, err := http.Post(srv.URL+"/api/auth/setup", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204 for the first none-mode setup call, got %d", resp.StatusCode)
	}

	takeoverBody, _ := json.Marshal(authCredentialsRequest{Username: "attacker", Password: "attacker-password"})
	resp2, err := http.Post(srv.URL+"/api/auth/setup", "application/json", bytes.NewReader(takeoverBody))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusConflict {
		t.Fatalf("expected 409 refusing a second setup call after a none-mode first run, got %d", resp2.StatusCode)
	}

	// The rejected second call must not have altered the active mode.
	statusResp, err := http.Get(srv.URL + "/api/auth/status")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer statusResp.Body.Close()
	var status authStatusResponse
	json.NewDecoder(statusResp.Body).Decode(&status)
	if !status.Configured {
		t.Error("expected the instance to still report configured:true")
	}
	if status.Mode != auth.ModeNone {
		t.Errorf("expected mode to remain %q after the rejected takeover attempt, got %q", auth.ModeNone, status.Mode)
	}
}

// --- Break-glass key (oidc first-run) ---

// readSetting reads a raw settings row directly via SQL, bypassing package
// auth's unexported settings-key constants — used by tests that must prove a
// specific persisted value was (or wasn't) touched by a specific code path.
func readSetting(t *testing.T, sqlDB *sql.DB, key string) (string, bool) {
	t.Helper()
	var value string
	err := sqlDB.QueryRow(`SELECT value FROM settings WHERE key = ?`, key).Scan(&value)
	if err == sql.ErrNoRows {
		return "", false
	}
	if err != nil {
		t.Fatalf("reading setting %q: %v", key, err)
	}
	return value, true
}

// TestSetup_OIDCMintsBreakGlassKey covers the break-glass mint: an oidc-mode
// first-run with no env key active mints a working one-time break-glass API
// key, revealed once in the setup response. It also folds in the
// boot-key-invalidation proof: a key already minted at simulated "boot"
// (EnsureAPIKey, mirroring cmd/sakms/main.go) must stop verifying once
// Regenerate runs here — proving the new key is genuinely a distinct, freshly
// minted credential, not just "a key happens to work."
func TestSetup_OIDCMintsBreakGlassKey(t *testing.T) {
	authStore, tokenEnc := testAuthStore(t)
	ctx := context.Background()

	// Simulate the boot-time key mint (main.go:92) that already happened
	// before an operator ever reaches first-run setup in the non-env case.
	bootKey, err := authStore.EnsureAPIKey(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if bootKey == "" {
		t.Fatal("expected a non-empty simulated boot key")
	}
	bootKeyOK, err := authStore.VerifyAPIKey(ctx, bootKey)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !bootKeyOK {
		t.Fatal("expected the simulated boot key to verify before setup")
	}

	srv := httptest.NewServer(NewAuthMux(authStore, tokenEnc))
	defer srv.Close()

	body, _ := json.Marshal(authCredentialsRequest{
		Mode:             "oidc",
		OIDCIssuerURL:    "https://sso.example.com",
		OIDCClientID:     "the-client-id",
		OIDCClientSecret: "the-client-secret",
		OIDCRedirectURL:  "https://sak.example.com/api/auth/oidc/callback",
	})
	resp, err := http.Post(srv.URL+"/api/auth/setup", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var setupResp authSetupResponse
	if err := json.NewDecoder(resp.Body).Decode(&setupResp); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if setupResp.APIKey == "" {
		t.Fatal("expected a non-empty minted break-glass API key in the setup response")
	}
	if setupResp.APIKeyNote != "" {
		t.Errorf("expected no apiKeyNote when a key was actually minted, got %q", setupResp.APIKeyNote)
	}

	// The "working" half of AC4: present the minted key as X-Api-Key against
	// an auth.Middleware-wrapped protected mux and confirm it passes.
	protected := auth.Middleware(tokenEnc, authStore, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	protectedSrv := httptest.NewServer(protected)
	defer protectedSrv.Close()

	req, _ := http.NewRequest(http.MethodGet, protectedSrv.URL+"/", nil)
	req.Header.Set("X-Api-Key", setupResp.APIKey)
	presentResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer presentResp.Body.Close()
	if presentResp.StatusCode != http.StatusOK {
		t.Fatalf("expected the freshly minted break-glass key to authenticate, got %d", presentResp.StatusCode)
	}

	// Boot-key-invalidation proof (Critic gap, closed): the OLD boot key must
	// no longer verify — Regenerate genuinely replaced it, not merely handed
	// back a second working credential alongside the first.
	bootKeyOKAfter, err := authStore.VerifyAPIKey(ctx, bootKey)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if bootKeyOKAfter {
		t.Error("expected the pre-setup boot key to no longer verify after oidc setup's Regenerate call")
	}
}

// TestSetup_OIDCEnvKeyActive_NoMintNote covers the env-precedence case: when
// SAKMS_API_KEY is active, oidc-mode first-run must not mint a settings
// key (it would be dead on arrival under env precedence) — instead the
// response carries a note, and the settings-persisted key (a stand-in for
// "whatever existed before, from an earlier boot") is left byte-identical.
func TestSetup_OIDCEnvKeyActive_NoMintNote(t *testing.T) {
	authStore, tokenEnc, sqlDB := testAuthStoreWithDB(t)
	ctx := context.Background()

	// A settings-persisted key from an earlier boot, before SAKMS_API_KEY
	// was ever set — Regenerate must never touch it while env is active.
	if _, err := authStore.EnsureAPIKey(ctx); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	hashBefore, ok := readSetting(t, sqlDB, "auth_apikey_hash")
	if !ok {
		t.Fatal("expected a persisted apikey hash before env activation")
	}

	authStore.UseEnvAPIKey("env-supplied-key-value")

	srv := httptest.NewServer(NewAuthMux(authStore, tokenEnc))
	defer srv.Close()

	body, _ := json.Marshal(authCredentialsRequest{
		Mode:             "oidc",
		OIDCIssuerURL:    "https://sso.example.com",
		OIDCClientID:     "the-client-id",
		OIDCClientSecret: "the-client-secret",
		OIDCRedirectURL:  "https://sak.example.com/api/auth/oidc/callback",
	})
	resp, err := http.Post(srv.URL+"/api/auth/setup", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var setupResp authSetupResponse
	if err := json.NewDecoder(resp.Body).Decode(&setupResp); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if setupResp.APIKey != "" {
		t.Errorf("expected no apiKey minted while env key is active, got %q", setupResp.APIKey)
	}
	if setupResp.APIKeyNote == "" {
		t.Error("expected a non-empty apiKeyNote while env key is active")
	}

	hashAfter, ok := readSetting(t, sqlDB, "auth_apikey_hash")
	if !ok {
		t.Fatal("expected the persisted apikey hash to still exist after setup")
	}
	if hashBefore != hashAfter {
		t.Errorf("expected the persisted settings key hash to be untouched while env key is active — nothing should ever be minted in this branch, before=%q after=%q", hashBefore, hashAfter)
	}
}

// TestSetup_ForwardMintFailure_LeavesUnconfigured is Critic finding #1's
// load-bearing regression proof: the break-glass mint (Regenerate) runs
// BEFORE SetAuthMode commits (plan §2c). A SQLite BEFORE INSERT trigger
// forces ONLY the apikey-hash settings write (persistKey, inside Regenerate)
// to fail — deliberately narrower than dropping the whole settings table,
// so that Configured(ctx) itself can still be read afterward without erroring,
// which is what this test needs to assert. If the ordering fix in §2c were
// ever reverted (SetAuthMode moved back before the mint), this test would
// start failing: Configured() would report true even though neither the
// forward secret nor a break-glass key was ever revealed — the exact
// unrecoverable lockout this whole feature exists to prevent.
func TestSetup_OIDCMintFailure_LeavesUnconfigured(t *testing.T) {
	authStore, tokenEnc, sqlDB := testAuthStoreWithDB(t)
	srv := httptest.NewServer(NewAuthMux(authStore, tokenEnc))
	defer srv.Close()

	const trigger = `
		CREATE TRIGGER block_apikey_hash_insert
		BEFORE INSERT ON settings
		WHEN NEW.key = 'auth_apikey_hash'
		BEGIN
			SELECT RAISE(ABORT, 'simulated settings write failure for Regenerate');
		END;`
	if _, err := sqlDB.Exec(trigger); err != nil {
		t.Fatalf("installing failure trigger: %v", err)
	}

	body, _ := json.Marshal(authCredentialsRequest{
		Mode:             "oidc",
		OIDCIssuerURL:    "https://sso.example.com",
		OIDCClientID:     "the-client-id",
		OIDCClientSecret: "the-client-secret",
		OIDCRedirectURL:  "https://sak.example.com/api/auth/oidc/callback",
	})
	resp, err := http.Post(srv.URL+"/api/auth/setup", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("expected 500 when the break-glass mint's settings write fails, got %d", resp.StatusCode)
	}

	configured, err := authStore.Configured(context.Background())
	if err != nil {
		t.Fatalf("unexpected error reading Configured after the failed setup call: %v", err)
	}
	if configured {
		t.Fatal("Critic finding #1 regression: a break-glass mint failure must leave the instance unconfigured (auth_mode never written) for a clean retry, not half-configured and permanently locked out")
	}

	mode, err := authStore.AuthMode(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mode != auth.ModePassword {
		t.Errorf("expected auth_mode to remain unwritten (default %q) after the failed mint, got %q", auth.ModePassword, mode)
	}
}
