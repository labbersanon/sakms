package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/curtiswtaylorjr/tidyarr/internal/allowlist"
	"github.com/curtiswtaylorjr/tidyarr/internal/connections"
	"github.com/curtiswtaylorjr/tidyarr/internal/db"
	"github.com/curtiswtaylorjr/tidyarr/internal/mediainfo"
	"github.com/curtiswtaylorjr/tidyarr/internal/proposals"
	"github.com/curtiswtaylorjr/tidyarr/internal/secrets"
	"github.com/curtiswtaylorjr/tidyarr/internal/settings"
)

// testProber returns a real *mediainfo.Prober — its Probe method is only
// ever exercised by tests that actually run Dedup's Scan against real
// on-disk video files, so a real ffprobe binary is only needed there.
func testProber(t *testing.T) *mediainfo.Prober {
	t.Helper()
	return mediainfo.New()
}

// testStores builds real connections.Store, proposals.Store,
// allowlist.Store, and settings.Store instances against one freshly
// migrated temp-file database, the same way each package's own tests do —
// handler tests exercise the real stack, not a mock.
func testStores(t *testing.T) (*connections.Store, *proposals.Store, *allowlist.Store, *settings.Store) {
	t.Helper()
	sqlDB, err := db.Open(filepath.Join(t.TempDir(), "tidyarr.db"))
	if err != nil {
		t.Fatalf("opening db: %v", err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	secretStore, err := secrets.New(make([]byte, 32))
	if err != nil {
		t.Fatalf("building secret store: %v", err)
	}
	return connections.New(sqlDB, secretStore), proposals.New(sqlDB), allowlist.New(sqlDB), settings.New(sqlDB)
}

// TestConnectionsTestHandler_EndToEnd exercises the real path a Settings
// "Test connection" click takes: an HTTP POST into Tidyarr's own server,
// which itself makes a real HTTP call out to the configured service (here, a
// second httptest server standing in for a live Radarr) and reports back
// over JSON. This is the thing actually wiring identify/servarr/ollama/
// stashapi into cmd/tidyarr is meant to prove works, not just that each
// package compiles in isolation.
func TestConnectionsTestHandler_EndToEnd(t *testing.T) {
	fakeRadarr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[{"id":1,"path":"/media/Movies","accessible":true,"freeSpace":123}]`))
	}))
	defer fakeRadarr.Close()

	connStore, propStore, allowStore, settingsStore := testStores(t)
	tidyarrSrv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), settingsStore))
	defer tidyarrSrv.Close()

	reqBody, _ := json.Marshal(ConnectionTestRequest{
		Service: "radarr", URL: fakeRadarr.URL, APIKey: "test-key",
	})
	resp, err := http.Post(tidyarrSrv.URL+"/api/connections/test", "application/json", bytes.NewReader(reqBody))
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
	if !result.OK {
		t.Fatalf("expected ok=true, got %+v", result)
	}
}

func TestConnectionsTestHandler_MalformedBody(t *testing.T) {
	connStore, propStore, allowStore, settingsStore := testStores(t)
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), settingsStore))
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/api/connections/test", "application/json", bytes.NewReader([]byte("not json")))
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for a malformed body, got %d", resp.StatusCode)
	}
}

// TestConnectionsCRUD_EndToEnd exercises the real Save/List/Delete path a
// Settings screen actually drives, hitting the real HTTP handlers backed by
// a real migrated SQLite file — not just the connections package in
// isolation.
func TestConnectionsCRUD_EndToEnd(t *testing.T) {
	connStore, propStore, allowStore, settingsStore := testStores(t)
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), settingsStore))
	defer srv.Close()

	// Save a connection.
	body, _ := json.Marshal(upsertConnectionRequest{URL: "http://192.168.1.12:7878", APIKey: "abcd1234"})
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/connections/radarr", bytes.NewReader(body))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204 from PUT, got %d", resp.StatusCode)
	}

	// It shows up in the list, redacted.
	listResp, err := http.Get(srv.URL + "/api/connections")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer listResp.Body.Close()
	var list []connections.Summary
	if err := json.NewDecoder(listResp.Body).Decode(&list); err != nil {
		t.Fatalf("decoding list: %v", err)
	}
	if len(list) != 1 || list[0].Service != "radarr" || !list[0].HasAPIKey || list[0].KeySuffix != "1234" {
		t.Fatalf("unexpected list: %+v", list)
	}

	// Delete it.
	delReq, _ := http.NewRequest(http.MethodDelete, srv.URL+"/api/connections/radarr", nil)
	delResp, err := http.DefaultClient.Do(delReq)
	if err != nil {
		t.Fatalf("DELETE failed: %v", err)
	}
	delResp.Body.Close()
	if delResp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204 from DELETE, got %d", delResp.StatusCode)
	}

	afterResp, err := http.Get(srv.URL + "/api/connections")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer afterResp.Body.Close()
	var afterList []connections.Summary
	json.NewDecoder(afterResp.Body).Decode(&afterList)
	if len(afterList) != 0 {
		t.Fatalf("expected no connections after delete, got %+v", afterList)
	}
}

func TestUpsertConnectionHandler_RequiresURL(t *testing.T) {
	connStore, propStore, allowStore, settingsStore := testStores(t)
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), settingsStore))
	defer srv.Close()

	body, _ := json.Marshal(upsertConnectionRequest{APIKey: "key-with-no-url"})
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/connections/radarr", bytes.NewReader(body))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 when url is missing, got %d", resp.StatusCode)
	}
}
