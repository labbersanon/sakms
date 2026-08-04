package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labbersanon/sakms/internal/connections"
)

// newStoredTestMux builds the real mux + stores and returns the server plus the
// connections store so a test can seed a saved connection before hitting
// /test-stored. Mirrors handler_test.go's full-stack setup.
func newStoredTestMux(t *testing.T) (*httptest.Server, *connections.Store) {
	t.Helper()
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, nil, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, testFeedHealth(), rssFeedsStore, nil, nil, nil, nil, nil, nil, nil, nil))
	t.Cleanup(srv.Close)
	return srv, connStore
}

// These tests exercise the GENERIC /api/connections/{service}/test-stored
// mechanism and its no-secret-leak contract, not any one service. The fixture
// is Prowlarr rather than the Jellyfin it used to be: migration 0053 moved
// jellyfin into the multi-connection registry, so that route now rejects it
// outright with a 400 naming /api/service-connections (see
// rejectMovedConnectionService and TestConnectionsRoutes_RejectMovedServices).
// Prowlarr is a surviving singleton service with the same URL+key shape, so the
// mechanism under test is unchanged. The registry's own id-keyed equivalent —
// POST /api/service-connections/{id}/test, including its identical detail-free
// failure contract — is covered in serviceconns_test.go.

// TestTestStored_NotConfigured confirms a 404 when the service has no saved
// connection — nothing to test.
func TestTestStored_NotConfigured(t *testing.T) {
	srv, _ := newStoredTestMux(t)

	resp, err := http.Post(srv.URL+"/api/connections/prowlarr/test-stored", "application/json", nil)
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 for an unconfigured service, got %d", resp.StatusCode)
	}
}

// TestTestStored_Unreachable_NoSecretLeak is the load-bearing security test: a
// saved connection pointing at a CLOSED server fails, and the response body
// must contain NEITHER the stored API key NOR the stored URL/host:port. The
// no-URL assertion is what fails if the sanitization (result.Error = "...") is
// removed — a Go dial error echoes the target URL.
func TestTestStored_Unreachable_NoSecretLeak(t *testing.T) {
	srv, connStore := newStoredTestMux(t)

	// A closed server: its URL is a real, unreachable host:port.
	closed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	closedURL := closed.URL
	closed.Close()

	const secretKey = "SUPERSECRETKEY-do-not-leak-123"
	if err := connStore.Upsert(context.Background(), "prowlarr", closedURL, secretKey); err != nil {
		t.Fatalf("seeding connection: %v", err)
	}

	resp, err := http.Post(srv.URL+"/api/connections/prowlarr/test-stored", "application/json", nil)
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 (the test ran, it just failed), got %d", resp.StatusCode)
	}

	raw, _ := io.ReadAll(resp.Body)
	body := string(raw)

	var result ConnectionTestResult
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("decoding response: %v (body=%q)", err, body)
	}
	if result.OK {
		t.Fatal("expected ok=false against a closed server")
	}
	if result.Error == "" {
		t.Error("expected a populated (generic) error message")
	}
	if strings.Contains(body, secretKey) {
		t.Errorf("response body leaked the stored API key: %q", body)
	}
	if strings.Contains(body, closedURL) {
		t.Errorf("response body leaked the stored URL %q: %q", closedURL, body)
	}
}

// TestTestStored_Success confirms the stored path actually works end-to-end:
// a saved connection pointing at a live (fake) Prowlarr tests OK, using the
// stored secret the client never holds.
func TestTestStored_Success(t *testing.T) {
	srv, connStore := newStoredTestMux(t)

	fakeProwlarr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Api-Key") != "stored-prowlarr-key" {
			t.Errorf("expected the stored key to be used, got header %q", r.Header.Get("X-Api-Key"))
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[]`))
	}))
	defer fakeProwlarr.Close()

	if err := connStore.Upsert(context.Background(), "prowlarr", fakeProwlarr.URL, "stored-prowlarr-key"); err != nil {
		t.Fatalf("seeding connection: %v", err)
	}

	resp, err := http.Post(srv.URL+"/api/connections/prowlarr/test-stored", "application/json", nil)
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var result ConnectionTestResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if !result.OK || result.Error != "" {
		t.Fatalf("expected ok=true against a live fake Jellyfin, got %+v", result)
	}
}
