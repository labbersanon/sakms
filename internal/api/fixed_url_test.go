package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestUpsertConnection_FixedURLServicesRequireNoURL confirms that fixed-URL
// services (tmdb, tvdb, stashdb, fansdb, tpdb) can be saved without a url
// field — their base URL is a hardcoded package constant, not operator-supplied.
func TestUpsertConnection_FixedURLServicesRequireNoURL(t *testing.T) {
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore, nil, nil, nil, nil, nil))
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
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore, nil, nil, nil, nil, nil))
	defer srv.Close()

	key := "some-api-key"
	for _, service := range []string{"prowlarr", "qbittorrent", "jellyfin", "stash", "ollama"} {
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
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore, nil, nil, nil, nil, nil))
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
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore, nil, nil, nil, nil, nil))
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

// TestAdultDiscover_StashDBIgnoresStoredConnectionURL is the stash-box
// (per-name lookup) counterpart: a bogus stored stashdb URL is ignored in favor
// of stashbox.StashDBURL.
func TestAdultDiscover_StashDBIgnoresStoredConnectionURL(t *testing.T) {
	var hit bool
	box := fakeStashBox(t, func(w http.ResponseWriter, r *http.Request) {
		hit = true
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"data":{"queryScenes":{"scenes":[{"id":"sb1","title":"S",` +
			`"release_date":"2024-06-06","studio":{"name":"Blacked","parent":null},` +
			`"images":[],"duration":1200,"fingerprints":[]}]}}}`))
	})

	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	overrideFixedURL(t, "stashdb", box.URL)
	if err := connStore.Upsert(context.Background(), "stashdb", "http://wrong.invalid/nope", "key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore, nil, nil, nil, nil, nil))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/modes/adult/discover/stashdb/recent")
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
