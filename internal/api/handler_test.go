package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/curtiswtaylorjr/sakms/internal/adultnewest"
	"github.com/curtiswtaylorjr/sakms/internal/allowlist"
	"github.com/curtiswtaylorjr/sakms/internal/connections"
	"github.com/curtiswtaylorjr/sakms/internal/db"
	"github.com/curtiswtaylorjr/sakms/internal/discoversliders"
	"github.com/curtiswtaylorjr/sakms/internal/grabs"
	"github.com/curtiswtaylorjr/sakms/internal/library"
	"github.com/curtiswtaylorjr/sakms/internal/mediainfo"
	"github.com/curtiswtaylorjr/sakms/internal/proposals"
	"github.com/curtiswtaylorjr/sakms/internal/rssfeeds"
	"github.com/curtiswtaylorjr/sakms/internal/secrets"
	"github.com/curtiswtaylorjr/sakms/internal/settings"
	"github.com/curtiswtaylorjr/sakms/internal/trakt"
)

// testProber returns a real *mediainfo.Prober — its Probe method is only
// ever exercised by tests that actually run Dedup's Scan against real
// on-disk video files, so a real ffprobe binary is only needed there.
func testProber(t *testing.T) *mediainfo.Prober {
	t.Helper()
	return mediainfo.New()
}

// constantPHasher hashes every path to the same valid scheme-tagged string,
// so the end-to-end Movies dedup scan's phash refinement keeps every same-TMDB
// candidate (identical hashes are within any threshold) without a real ffmpeg
// binary or video content. It satisfies dedup.PHasher.
type constantPHasher struct{}

func (constantPHasher) Hash(ctx context.Context, path string) (string, error) {
	return "phash64/5f:" + strings.Repeat("0", 80), nil // 40 zero bytes = 5 frames × 8
}

// testPHasher returns a fake perceptual hasher for NewMux — mirrors testProber,
// so a handler test never shells out to ffmpeg. Only the Movies dedup scan
// actually consults it; every other route ignores it.
func testPHasher(t *testing.T) constantPHasher {
	t.Helper()
	return constantPHasher{}
}

// constantVideoHasher hashes every path to the same fixed hex string, standing
// in for NewMux's videophash hasher (Adult Rename's phash-first identification)
// so a handler test never shells out to ffmpeg. It satisfies rename.PHasher.
// Distinct from constantPHasher: that one is Movies/Series Dedup's incompatible
// scheme-tagged algorithm — the two must not be blurred.
type constantVideoHasher struct{}

func (constantVideoHasher) Hash(ctx context.Context, path string) (string, error) {
	return "ffffffffffffffff", nil
}

// testVideoHasher returns a fake StashDB-compatible video hasher for NewMux —
// mirrors testPHasher. Only Adult's Rename scan actually consults it.
func testVideoHasher(t *testing.T) constantVideoHasher {
	t.Helper()
	return constantVideoHasher{}
}

// testStores builds real connections.Store, proposals.Store,
// allowlist.Store, settings.Store, grabs.Store, library.Store,
// discoversliders.Store, trakt.Store, adultnewest.Store,
// adultnewest.ReleaseStore, and rssfeeds.Store instances against one
// freshly migrated temp-file database, the same way each package's own
// tests do — handler tests exercise the real stack, not a mock.
func testStores(t *testing.T) (*connections.Store, *proposals.Store, *allowlist.Store, *settings.Store, *grabs.Store, *library.Store, *discoversliders.Store, *trakt.Store, *adultnewest.Store, *adultnewest.ReleaseStore, *rssfeeds.Store) {
	t.Helper()
	sqlDB, err := db.Open(filepath.Join(t.TempDir(), "sakms.db"))
	if err != nil {
		t.Fatalf("opening db: %v", err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	secretStore, err := secrets.New(make([]byte, 32))
	if err != nil {
		t.Fatalf("building secret store: %v", err)
	}
	return connections.New(sqlDB, secretStore), proposals.New(sqlDB), allowlist.New(sqlDB), settings.New(sqlDB), grabs.New(sqlDB), library.New(sqlDB), discoversliders.New(sqlDB), trakt.NewStore(sqlDB, secretStore), adultnewest.New(sqlDB), adultnewest.NewReleaseStore(sqlDB), rssfeeds.New(sqlDB)
}

// TestConnectionsTestHandler_EndToEnd exercises the real path a Settings
// "Test connection" click takes: an HTTP POST into SAK's own server,
// which itself makes a real HTTP call out to the configured service (here, a
// second httptest server standing in for a live Jellyfin) and reports back
// over JSON. This is the thing actually wiring identify/ollama/stashapi/
// jellyfin into cmd/sakms is meant to prove works, not just that each
// package compiles in isolation.
func TestConnectionsTestHandler_EndToEnd(t *testing.T) {
	fakeJellyfin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"Version":"10.9.0"}`))
	}))
	defer fakeJellyfin.Close()

	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	sakSrv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore, nil, nil, nil, nil, nil))
	defer sakSrv.Close()

	reqBody, _ := json.Marshal(ConnectionTestRequest{
		Service: "jellyfin", URL: fakeJellyfin.URL, APIKey: "test-key",
	})
	resp, err := http.Post(sakSrv.URL+"/api/connections/test", "application/json", bytes.NewReader(reqBody))
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
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore, nil, nil, nil, nil, nil))
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
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore, nil, nil, nil, nil, nil))
	defer srv.Close()

	// Save a connection.
	key := "abcd1234"
	body, _ := json.Marshal(upsertConnectionRequest{URL: "http://192.168.1.12:7878", APIKey: &key})
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


// TestUpsertConnectionHandler_OmittedAPIKeyPreservesSecret locks the data-loss
// fix: a PUT with apiKey absent from the JSON entirely (what the frontend sends
// when the operator edits only the URL, leaving the blank "unchanged (••••)"
// key field untouched) must preserve the stored secret, not wipe it.
func TestUpsertConnectionHandler_OmittedAPIKeyPreservesSecret(t *testing.T) {
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore, nil, nil, nil, nil, nil))
	defer srv.Close()

	if err := connStore.Upsert(context.Background(), "radarr", "http://old:7878", "my-secret-key"); err != nil {
		t.Fatalf("seeding connection: %v", err)
	}

	// apiKey nil → omitted from the JSON body (omitempty on a nil pointer).
	body, _ := json.Marshal(upsertConnectionRequest{URL: "http://new:7878"})
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/connections/radarr", bytes.NewReader(body))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204 from PUT, got %d", resp.StatusCode)
	}

	got, err := connStore.Get(context.Background(), "radarr")
	if err != nil {
		t.Fatalf("loading connection: %v", err)
	}
	if got.URL != "http://new:7878" {
		t.Errorf("expected url to update, got %q", got.URL)
	}
	if got.APIKey != "my-secret-key" {
		t.Errorf("expected the stored secret to be preserved when apiKey is omitted, got %q", got.APIKey)
	}
}

// TestUpsertConnectionHandler_ExplicitEmptyAPIKeyClearsSecret regression-locks
// today's existing behavior: apiKey present as "" still clears the stored
// secret (e.g. switching a service to one that needs no key).
func TestUpsertConnectionHandler_ExplicitEmptyAPIKeyClearsSecret(t *testing.T) {
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore, nil, nil, nil, nil, nil))
	defer srv.Close()

	if err := connStore.Upsert(context.Background(), "radarr", "http://radarr:7878", "my-secret-key"); err != nil {
		t.Fatalf("seeding connection: %v", err)
	}

	empty := ""
	body, _ := json.Marshal(upsertConnectionRequest{URL: "http://radarr:7878", APIKey: &empty})
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/connections/radarr", bytes.NewReader(body))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204 from PUT, got %d", resp.StatusCode)
	}

	got, err := connStore.Get(context.Background(), "radarr")
	if err != nil {
		t.Fatalf("loading connection: %v", err)
	}
	if got.APIKey != "" {
		t.Errorf("expected an explicit empty apiKey to clear the stored secret, got %q", got.APIKey)
	}
}
