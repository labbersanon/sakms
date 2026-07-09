package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/curtiswtaylorjr/sak/internal/auth"
	"github.com/curtiswtaylorjr/sak/internal/db"
	"github.com/curtiswtaylorjr/sak/internal/secrets"
	"github.com/curtiswtaylorjr/sak/internal/settings"
)

func testAuthStore(t *testing.T) (*auth.Store, *secrets.Store) {
	t.Helper()
	sqlDB, err := db.Open(filepath.Join(t.TempDir(), "sak.db"))
	if err != nil {
		t.Fatalf("opening db: %v", err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	secretStore, err := secrets.New(make([]byte, 32))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	return auth.New(settings.New(sqlDB)), secretStore
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
