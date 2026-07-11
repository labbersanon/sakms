package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/curtiswtaylorjr/sakms/internal/auth"
)

func TestAPIKeyStatus_NoKey(t *testing.T) {
	authStore, _ := testAuthStore(t)
	srv := httptest.NewServer(NewAPIKeyMux(authStore))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/apikey")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var status auth.APIKeyStatus
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if status.HasKey || status.Source != "none" {
		t.Fatalf("expected hasKey=false source=none on a fresh store, got %+v", status)
	}
}

func TestAPIKeyStatus_SettingsKey(t *testing.T) {
	authStore, _ := testAuthStore(t)
	if _, err := authStore.EnsureAPIKey(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	srv := httptest.NewServer(NewAPIKeyMux(authStore))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/apikey")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	var status auth.APIKeyStatus
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if !status.HasKey || status.Source != "settings" || status.KeySuffix == "" {
		t.Fatalf("expected hasKey=true source=settings with a suffix, got %+v", status)
	}
}

func TestAPIKeyStatus_EnvKey(t *testing.T) {
	authStore, _ := testAuthStore(t)
	authStore.UseEnvAPIKey("env-supplied-key-value")
	srv := httptest.NewServer(NewAPIKeyMux(authStore))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/apikey")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	var status auth.APIKeyStatus
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if !status.HasKey || status.Source != "env" {
		t.Fatalf("expected hasKey=true source=env, got %+v", status)
	}
}

// TestAPIKeyRegenerate_ReturnsFullKeyOnce covers AC6: the full key appears
// in the regenerate response body exactly once, the suffix matches it, and
// a subsequent status call shows only the masked suffix — never the value.
func TestAPIKeyRegenerate_ReturnsFullKeyOnce(t *testing.T) {
	authStore, _ := testAuthStore(t)
	srv := httptest.NewServer(NewAPIKeyMux(authStore))
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/api/apikey/regenerate", "application/json", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var regen struct {
		APIKey    string `json:"apiKey"`
		KeySuffix string `json:"keySuffix"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&regen); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if regen.APIKey == "" {
		t.Fatal("expected a non-empty full API key in the regenerate response")
	}
	if regen.KeySuffix == "" || regen.KeySuffix != regen.APIKey[len(regen.APIKey)-4:] {
		t.Fatalf("expected keySuffix to be the last 4 chars of apiKey, got suffix=%q key=%q", regen.KeySuffix, regen.APIKey)
	}

	statusResp, err := http.Get(srv.URL + "/api/apikey")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer statusResp.Body.Close()
	var status auth.APIKeyStatus
	if err := json.NewDecoder(statusResp.Body).Decode(&status); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if !status.HasKey || status.KeySuffix != regen.KeySuffix {
		t.Fatalf("expected status to show only the matching suffix, got %+v", status)
	}
}

func TestAPIKeyRegenerate_EnvManaged_409(t *testing.T) {
	authStore, _ := testAuthStore(t)
	authStore.UseEnvAPIKey("env-supplied-key-value")
	srv := httptest.NewServer(NewAPIKeyMux(authStore))
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/api/apikey/regenerate", "application/json", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("expected 409 while env-managed, got %d", resp.StatusCode)
	}
}
