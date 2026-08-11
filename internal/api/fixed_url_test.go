package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestUpsertConnection_FixedURLServicesRequireNoURL confirms that fixed-URL
// services (tmdb, tvdb, stashdb, fansdb, tpdb) can be saved without a url
// field — their base URL is a hardcoded package constant, not operator-supplied.
func TestUpsertConnection_FixedURLServicesRequireNoURL(t *testing.T) {
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, nil, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, testFeedHealth(), rssFeedsStore, nil, nil, nil, nil, nil, nil, nil, nil))
	defer srv.Close()

	key := "some-api-key"
	for service := range fixedURLServices {
		body, _ := json.Marshal(upsertConnectionRequest{APIKey: &key})
		req, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/connections/"+service, bytes.NewReader(body))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("PUT %s failed: %v", service, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNoContent {
			t.Errorf("expected 204 saving fixed-URL service %s without a url, got %d", service, resp.StatusCode)
		}
	}
}

// TestUpsertConnection_NonFixedURLServiceRequiresURL confirms that services
// with operator-supplied URLs (e.g. prowlarr) return 400 when saved without one.
func TestUpsertConnection_NonFixedURLServiceRequiresURL(t *testing.T) {
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, nil, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, testFeedHealth(), rssFeedsStore, nil, nil, nil, nil, nil, nil, nil, nil))
	defer srv.Close()

	key := "some-api-key"
	for _, service := range []string{"prowlarr", "jellyfin", "stash", "ollama"} {
		body, _ := json.Marshal(upsertConnectionRequest{APIKey: &key})
		req, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/connections/"+service, bytes.NewReader(body))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("PUT %s failed: %v", service, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("expected 400 saving %s without a url, got %d", service, resp.StatusCode)
		}
	}
}

// TestUpsertConnection_RejectsRemovedDownloadClientServices confirms
// PUT /api/connections/{qbittorrent,nzbget} is refused with 400, naming the
// removed clients — the write-path half of the 2026-08-10 removal
// (removedConnectionServices/rejectRemovedConnectionService, handler.go).
// Deleting the connections table's existing rows (migration 0007) isn't
// enough on its own: connections.Store accepts any service key generically,
// so without this guard a stale frontend or a saved curl one-liner could
// still write a row nothing reads. Mirrors TestConnectionsRoutes_RejectMovedServices'
// coverage of the sibling movedConnectionServices guard.
func TestUpsertConnection_RejectsRemovedDownloadClientServices(t *testing.T) {
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, nil, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, testFeedHealth(), rssFeedsStore, nil, nil, nil, nil, nil, nil, nil, nil))
	defer srv.Close()

	key := "some-api-key"
	for _, service := range []string{"qbittorrent", "nzbget"} {
		body, _ := json.Marshal(upsertConnectionRequest{URL: "http://moved:1234", APIKey: &key})
		req, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/connections/"+service, bytes.NewReader(body))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("PUT %s failed: %v", service, err)
		}
		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("expected 400 for removed service %s, got %d: %s", service, resp.StatusCode, respBody)
		}
		if !bytes.Contains(respBody, []byte("no longer supported")) {
			t.Errorf("expected rejection to explain %s is no longer supported, got %q", service, respBody)
		}
	}

	// A surviving singleton service is unaffected by the guard.
	body, _ := json.Marshal(upsertConnectionRequest{URL: "http://prowlarr:9696", APIKey: &key})
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/connections/prowlarr", bytes.NewReader(body))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT prowlarr failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("expected the guard to leave prowlarr alone, got %d", resp.StatusCode)
	}
}

// TestDiscover_TMDBIgnoresStoredConnectionURL proves TMDB's outbound calls hit
// tmdb.DefaultBaseURL, not Connection.URL: the stored connection points at a
// bogus, unreachable host, yet the discover request succeeds by reaching the
// fake the package var was redirected to.
func TestDiscover_TMDBIgnoresStoredConnectionURL(t *testing.T) {
	var hit bool
	fake := fakeTMDB(t, func(w http.ResponseWriter, r *http.Request) {
		hit = true
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"results":[{"id":1,"title":"Some Movie","poster_path":"/x.jpg","vote_average":7.5}]}`))
	})

	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	overrideFixedURL(t, "tmdb", fake.URL)
	// Deliberately bogus stored URL — must be ignored entirely.
	if err := connStore.Upsert(context.Background(), "tmdb", "http://wrong.invalid/nope", "key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, nil, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, testFeedHealth(), rssFeedsStore, nil, nil, nil, nil, nil, nil, nil, nil))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/modes/movies/discover")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 (fixed URL used, bogus stored URL ignored), got %d", resp.StatusCode)
	}
	if !hit {
		t.Error("expected the fixed-URL fake to be hit, not the stored Connection.URL")
	}
}

// TestAdultDiscover_TPDBIgnoresStoredConnectionURL is the TPDB-REST counterpart:
// a bogus stored tpdb URL is ignored in favor of tpdbrest.DefaultBaseURL.
func TestAdultDiscover_TPDBIgnoresStoredConnectionURL(t *testing.T) {
	var hit bool
	tpdb := fakeTPDB(t, func(w http.ResponseWriter, r *http.Request) {
		hit = true
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"data":[{"_id":"s1","title":"Scene","date":"2024-01-01","site":{"name":"Studio"}}]}`))
	})

	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	// fakeTPDB already redirected tpdbrest.DefaultBaseURL; store a bogus URL.
	_ = tpdb
	if err := connStore.Upsert(context.Background(), "tpdb", "http://wrong.invalid/nope", "key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, nil, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, testFeedHealth(), rssFeedsStore, nil, nil, nil, nil, nil, nil, nil, nil))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/modes/adult/discover?page=1&perPage=10")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 (fixed URL used, bogus stored URL ignored), got %d", resp.StatusCode)
	}
	if !hit {
		t.Error("expected the fixed-URL fake to be hit, not the stored Connection.URL")
	}
}

// TestStashDBIgnoresStoredConnectionURL is the stash-box (per-name lookup)
// counterpart: a bogus stored stashdb URL is ignored in favor of
// stashbox.StashDBURL.
//
// Claude 2026-08-02: re-pointed from GET /api/modes/adult/discover/stashdb/recent
// to POST /api/connections/{service}/test-stored, and renamed off the
// "AdultDiscover_" prefix, which no longer describes it.
// Reason: the stash-box Adult Discover browse routes were deleted with the
// structural stash-box rows, so the old target now 404s. test-stored is the
// closest surviving seam — it reads the STORED connection (preserving this
// test's whole point) and, for stashdb/fansdb, hands TestConnection a conn.URL
// it then discards in favor of stashbox.URLForBox (connections.go's
// case "stashdb", "fansdb"). Other live non-HTTP callers of that same
// fixed-endpoint lookup remain at mode.go:595, entity_sync.go:117,
// connections.go:78 and parseentity/schedule.go:135.
// Troubleshooting: without the re-point this test 404s and the stashdb leg of
// the fixed-URL invariant loses HTTP-level coverage entirely.
// Review if: an Adult read path that consumes stashdb over HTTP comes back —
// it would be a more representative target than a connection-test route.
// Related files: internal/api/handler.go, internal/api/connections.go
func TestStashDBIgnoresStoredConnectionURL(t *testing.T) {
	var hit bool
	box := fakeStashBox(t, func(w http.ResponseWriter, r *http.Request) {
		hit = true
		w.Header().Set("Content-Type", "application/json")
		// test-stored drives stashbox.Client.Me, not queryScenes.
		w.Write([]byte(`{"data":{"me":{"id":"u1","name":"tester"}}}`))
	})

	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	overrideFixedURL(t, "stashdb", box.URL)
	if err := connStore.Upsert(context.Background(), "stashdb", "http://wrong.invalid/nope", "key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, nil, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, testFeedHealth(), rssFeedsStore, nil, nil, nil, nil, nil, nil, nil, nil))
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/connections/stashdb/test-stored", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	// The handler writes 200 even for a FAILED test, so the status alone proves
	// nothing — decode and assert OK, or a broken fake passes vacuously.
	var result ConnectionTestResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if !result.OK {
		t.Fatalf("expected a passing connection test (fixed URL used, bogus stored URL ignored), got error %q", result.Error)
	}
	if !hit {
		t.Error("expected the fixed-URL fake to be hit, not the stored Connection.URL")
	}
}
