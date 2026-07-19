package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestRecheckInterval_RoundTrip drives the real mux: GET on a blank install
// returns 0 (off — the opt-in default), PUT stores a value, and a follow-up
// GET reads it back.
func TestRecheckInterval_RoundTrip(t *testing.T) {
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore, nil, nil, nil, nil, nil))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/settings/recheck-interval")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	var got recheckIntervalResponse
	json.NewDecoder(resp.Body).Decode(&got)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 on unset GET, got %d", resp.StatusCode)
	}
	if got.IntervalSeconds != 0 {
		t.Errorf("expected the default interval to be 0 (off), got %d", got.IntervalSeconds)
	}

	body, _ := json.Marshal(recheckIntervalRequest{IntervalSeconds: 900})
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/settings/recheck-interval", bytes.NewReader(body))
	putResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT failed: %v", err)
	}
	putResp.Body.Close()
	if putResp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204 on PUT, got %d", putResp.StatusCode)
	}

	resp2, err := http.Get(srv.URL + "/api/settings/recheck-interval")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp2.Body.Close()
	var got2 recheckIntervalResponse
	json.NewDecoder(resp2.Body).Decode(&got2)
	if got2.IntervalSeconds != 900 {
		t.Errorf("expected the stored interval to round-trip, got %d", got2.IntervalSeconds)
	}
}

// TestRecheckInterval_ZeroDisables confirms 0 is an accepted value (it's how an
// operator turns the job off), not rejected like a negative.
func TestRecheckInterval_ZeroDisables(t *testing.T) {
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore, nil, nil, nil, nil, nil))
	defer srv.Close()

	body, _ := json.Marshal(recheckIntervalRequest{IntervalSeconds: 0})
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/settings/recheck-interval", bytes.NewReader(body))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204 storing 0 (off), got %d", resp.StatusCode)
	}
}

// TestRecheckInterval_NegativeRejected confirms a negative interval is a 400.
func TestRecheckInterval_NegativeRejected(t *testing.T) {
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore, nil, nil, nil, nil, nil))
	defer srv.Close()

	body, _ := json.Marshal(recheckIntervalRequest{IntervalSeconds: -1})
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/settings/recheck-interval", bytes.NewReader(body))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for a negative interval, got %d", resp.StatusCode)
	}
}

// TestRecheckInterval_InvalidBody confirms a malformed JSON body is a 400.
func TestRecheckInterval_InvalidBody(t *testing.T) {
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore, nil, nil, nil, nil, nil))
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/settings/recheck-interval", bytes.NewReader([]byte("not json")))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for a malformed body, got %d", resp.StatusCode)
	}
}
