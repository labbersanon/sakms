package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestDiscoverRefreshInterval_UnsetDefaultsTo24Hours is T-11: the interval
// endpoint must mirror discoverrefresh.LoadInterval's unset-vs-explicit-zero
// distinction exactly — the same bug class adult_newest_scan.go:16-23
// records (this handler must NOT default to 0 on an unset key).
func TestDiscoverRefreshInterval_UnsetDefaultsTo24Hours(t *testing.T) {
	connStore, propStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, nil, propStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, testFeedHealth(), rssFeedsStore, nil, nil, nil, nil, nil, nil, nil, nil))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/settings/discover-refresh-interval")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	var got discoverRefreshIntervalResponse
	json.NewDecoder(resp.Body).Decode(&got)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 on unset GET, got %d", resp.StatusCode)
	}
	if got.IntervalSeconds != discoverRefreshDefaultSeconds {
		t.Errorf("expected the unset default to be %d (24h), got %d", discoverRefreshDefaultSeconds, got.IntervalSeconds)
	}
}

// TestDiscoverRefreshInterval_ExplicitZeroIsOffNotDefault confirms an
// operator explicitly saving 0 means off, not "fall back to the 24h
// default" — the exact distinction the bug above collapsed.
func TestDiscoverRefreshInterval_ExplicitZeroIsOffNotDefault(t *testing.T) {
	connStore, propStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, nil, propStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, testFeedHealth(), rssFeedsStore, nil, nil, nil, nil, nil, nil, nil, nil))
	defer srv.Close()

	body, _ := json.Marshal(discoverRefreshIntervalRequest{IntervalSeconds: 0})
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/settings/discover-refresh-interval", bytes.NewReader(body))
	putResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT failed: %v", err)
	}
	putResp.Body.Close()
	if putResp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204 storing 0 (off), got %d", putResp.StatusCode)
	}

	resp, err := http.Get(srv.URL + "/api/settings/discover-refresh-interval")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	var got discoverRefreshIntervalResponse
	json.NewDecoder(resp.Body).Decode(&got)
	if got.IntervalSeconds != 0 {
		t.Errorf("expected an explicitly-saved 0 to stay 0, got %d", got.IntervalSeconds)
	}
}

// TestDiscoverRefreshInterval_StoredValueRoundTrips confirms a normal
// positive value round-trips unchanged.
func TestDiscoverRefreshInterval_StoredValueRoundTrips(t *testing.T) {
	connStore, propStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, nil, propStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, testFeedHealth(), rssFeedsStore, nil, nil, nil, nil, nil, nil, nil, nil))
	defer srv.Close()

	body, _ := json.Marshal(discoverRefreshIntervalRequest{IntervalSeconds: 3600})
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/settings/discover-refresh-interval", bytes.NewReader(body))
	putResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT failed: %v", err)
	}
	putResp.Body.Close()
	if putResp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204 on PUT, got %d", putResp.StatusCode)
	}

	resp, err := http.Get(srv.URL + "/api/settings/discover-refresh-interval")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	var got discoverRefreshIntervalResponse
	json.NewDecoder(resp.Body).Decode(&got)
	if got.IntervalSeconds != 3600 {
		t.Errorf("expected the stored interval to round-trip, got %d", got.IntervalSeconds)
	}
}

// TestDiscoverRefreshInterval_NegativeRejected confirms a negative interval
// is a 400 — mirrors TestAdultNewestScanInterval_NegativeRejected.
func TestDiscoverRefreshInterval_NegativeRejected(t *testing.T) {
	connStore, propStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, nil, propStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, testFeedHealth(), rssFeedsStore, nil, nil, nil, nil, nil, nil, nil, nil))
	defer srv.Close()

	body, _ := json.Marshal(discoverRefreshIntervalRequest{IntervalSeconds: -1})
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/settings/discover-refresh-interval", bytes.NewReader(body))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for a negative interval, got %d", resp.StatusCode)
	}
}

// TestDiscoverRefreshInterval_InvalidBody confirms a malformed JSON body is
// a 400 — mirrors TestAdultNewestScanInterval_InvalidBody.
func TestDiscoverRefreshInterval_InvalidBody(t *testing.T) {
	connStore, propStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, nil, propStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, testFeedHealth(), rssFeedsStore, nil, nil, nil, nil, nil, nil, nil, nil))
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/settings/discover-refresh-interval", bytes.NewReader([]byte("not json")))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for a malformed body, got %d", resp.StatusCode)
	}
}
