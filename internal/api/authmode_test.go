package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labbersanon/sakms/internal/auth"
)

func TestGetMode_ReturnsEffective(t *testing.T) {
	authStore, _ := testAuthStore(t)
	srv := httptest.NewServer(NewAuthModeMux(authStore))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/auth/mode")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	var got authModeResponse
	json.NewDecoder(resp.Body).Decode(&got)
	if got.Mode != auth.ModePassword {
		t.Errorf("expected default mode %q, got %q", auth.ModePassword, got.Mode)
	}

	if err := authStore.SetAuthMode(context.Background(), auth.ModeNone); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resp2, err := http.Get(srv.URL + "/api/auth/mode")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp2.Body.Close()
	var got2 authModeResponse
	json.NewDecoder(resp2.Body).Decode(&got2)
	if got2.Mode != auth.ModeNone {
		t.Errorf("expected mode %q, got %q", auth.ModeNone, got2.Mode)
	}
}

func TestPutMode_NoneRequiresAck_400(t *testing.T) {
	authStore, _ := testAuthStore(t)
	srv := httptest.NewServer(NewAuthModeMux(authStore))
	defer srv.Close()

	body, _ := json.Marshal(authModeRequest{Mode: auth.ModeNone})
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/auth/mode", bytes.NewReader(body))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 without acknowledgeInsecure, got %d", resp.StatusCode)
	}

	mode, err := authStore.AuthMode(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mode != auth.ModePassword {
		t.Errorf("expected the rejected switch to leave mode unchanged (%q), got %q", auth.ModePassword, mode)
	}
}

func TestPutMode_NoneWithAck_204(t *testing.T) {
	authStore, _ := testAuthStore(t)
	srv := httptest.NewServer(NewAuthModeMux(authStore))
	defer srv.Close()

	body, _ := json.Marshal(authModeRequest{Mode: auth.ModeNone, AcknowledgeInsecure: true})
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/auth/mode", bytes.NewReader(body))
	resp, err := http.DefaultClient.Do(req)
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
	if mode != auth.ModeNone {
		t.Errorf("expected mode %q, got %q", auth.ModeNone, mode)
	}
}

func TestPutMode_PasswordWithoutHash_400(t *testing.T) {
	authStore, _ := testAuthStore(t)
	srv := httptest.NewServer(NewAuthModeMux(authStore))
	defer srv.Close()

	body, _ := json.Marshal(authModeRequest{Mode: auth.ModePassword})
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/auth/mode", bytes.NewReader(body))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 switching to password with no password hash configured, got %d", resp.StatusCode)
	}
}

// TestPutMode_OIDCWithoutConfig_400 covers the G4 switch-into precondition
// for oidc: switching to "oidc" before its config exists must 400, since a
// switch could otherwise strand the instance with an unusable mode.
func TestPutMode_OIDCWithoutConfig_400(t *testing.T) {
	authStore, _ := testAuthStore(t)
	srv := httptest.NewServer(NewAuthModeMux(authStore))
	defer srv.Close()

	body, _ := json.Marshal(authModeRequest{Mode: auth.ModeOIDC})
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/auth/mode", bytes.NewReader(body))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 switching to oidc with no config configured, got %d", resp.StatusCode)
	}
}

// TestPutMode_OIDCWithConfig_204 covers the positive precondition: once OIDC
// config is set, the switch succeeds.
func TestPutMode_OIDCWithConfig_204(t *testing.T) {
	authStore, secretStore := testAuthStore(t)
	ctx := context.Background()
	cipher, err := secretStore.Encrypt("the-client-secret")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := authStore.SetOIDCConfig(ctx, "https://sso.example.com", "the-client-id", cipher, "https://sak.example.com/api/auth/oidc/callback"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	srv := httptest.NewServer(NewAuthModeMux(authStore))
	defer srv.Close()

	body, _ := json.Marshal(authModeRequest{Mode: auth.ModeOIDC})
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/auth/mode", bytes.NewReader(body))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204 switching to oidc with config present, got %d", resp.StatusCode)
	}
}

// TestPutMode_SwitchAwayKeepsConfig covers G4/AC6: switching password ->
// none -> password must never wipe the password hash — the operator's
// original credentials must still verify after the round trip, with no
// re-setup required.
func TestPutMode_SwitchAwayKeepsConfig(t *testing.T) {
	authStore, _ := testAuthStore(t)
	ctx := context.Background()
	if err := authStore.SetCredentials(ctx, "wade", "correct-horse-battery-staple"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := authStore.SetAuthMode(ctx, auth.ModePassword); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	srv := httptest.NewServer(NewAuthModeMux(authStore))
	defer srv.Close()

	// Switch to none.
	noneBody, _ := json.Marshal(authModeRequest{Mode: auth.ModeNone, AcknowledgeInsecure: true})
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/auth/mode", bytes.NewReader(noneBody))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204 switching to none, got %d", resp.StatusCode)
	}

	// Switch back to password — must succeed because the hash was never
	// wiped.
	passwordBody, _ := json.Marshal(authModeRequest{Mode: auth.ModePassword})
	req2, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/auth/mode", bytes.NewReader(passwordBody))
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204 switching back to password, got %d", resp2.StatusCode)
	}

	ok, err := authStore.Verify(ctx, "wade", "correct-horse-battery-staple")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("expected the original password to still verify after switching away and back")
	}
}

// TestAuthModeMux_ProtectedByMiddleware asserts the mux itself carries no
// auth authority — it's cmd/sakms's job to wrap it in auth.Middleware, and
// an unwrapped instance would be reachable by anyone. This test exercises
// the wrapped composition directly, the same way cmd/sakms wires it.
func TestAuthModeMux_ProtectedByMiddleware(t *testing.T) {
	authStore, tokenEnc := testAuthStore(t)
	protected := auth.Middleware(tokenEnc, authStore, NewAuthModeMux(authStore))
	srv := httptest.NewServer(protected)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/auth/mode")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 for an unauthenticated request to a protected mode mux, got %d", resp.StatusCode)
	}
}
