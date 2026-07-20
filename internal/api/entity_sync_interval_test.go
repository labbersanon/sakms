package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestEntitySyncInterval_RoundTrip drives the real mux: GET on a blank
// install returns 0 (off — entity sync was purely manual before this job
// existed, so an unset key must not read as active), PUT stores a value, and
// a follow-up GET reads it back.
func TestEntitySyncInterval_RoundTrip(t *testing.T) {
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore, nil, nil, nil, nil))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/settings/entity-sync-interval")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	var got entitySyncIntervalResponse
	json.NewDecoder(resp.Body).Decode(&got)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 on unset GET, got %d", resp.StatusCode)
	}
	if got.IntervalSeconds != 0 {
		t.Errorf("expected the default interval to be 0 (off), got %d", got.IntervalSeconds)
	}

	body, _ := json.Marshal(entitySyncIntervalRequest{IntervalSeconds: 21600})
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/settings/entity-sync-interval", bytes.NewReader(body))
	putResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT failed: %v", err)
	}
	putResp.Body.Close()
	if putResp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204 on PUT, got %d", putResp.StatusCode)
	}

	resp2, err := http.Get(srv.URL + "/api/settings/entity-sync-interval")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp2.Body.Close()
	var got2 entitySyncIntervalResponse
	json.NewDecoder(resp2.Body).Decode(&got2)
	if got2.IntervalSeconds != 21600 {
		t.Errorf("expected the stored interval to round-trip, got %d", got2.IntervalSeconds)
	}
}

// TestEntitySyncInterval_ZeroDisables confirms 0 is an accepted value (it's
// how an operator turns the job off), not rejected like a negative.
func TestEntitySyncInterval_ZeroDisables(t *testing.T) {
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore, nil, nil, nil, nil))
	defer srv.Close()

	body, _ := json.Marshal(entitySyncIntervalRequest{IntervalSeconds: 0})
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/settings/entity-sync-interval", bytes.NewReader(body))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204 storing 0 (off), got %d", resp.StatusCode)
	}
}

// TestEntitySyncInterval_NegativeRejected confirms a negative interval is a 400.
func TestEntitySyncInterval_NegativeRejected(t *testing.T) {
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore, nil, nil, nil, nil))
	defer srv.Close()

	body, _ := json.Marshal(entitySyncIntervalRequest{IntervalSeconds: -1})
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/settings/entity-sync-interval", bytes.NewReader(body))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for a negative interval, got %d", resp.StatusCode)
	}
}

// TestEntitySyncInterval_InvalidBody confirms a malformed JSON body is a 400.
func TestEntitySyncInterval_InvalidBody(t *testing.T) {
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore, nil, nil, nil, nil))
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/settings/entity-sync-interval", bytes.NewReader([]byte("not json")))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for a malformed body, got %d", resp.StatusCode)
	}
}
