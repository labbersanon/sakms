package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestAdultNewestScanInterval_UnsetDefaultsTo24Hours is the regression test
// for a real bug caught during this feature's own live deploy verification:
// this handler originally defaulted to 0 (off) on an unset key, out of sync
// with adultnewest.LoadInterval's actual 24h default — Settings showed "0"
// while the background job was really running every 24h. Unlike
// TestRecheckInterval_RoundTrip's unset case (which expects 0, correctly —
// recheck IS off by default), this endpoint's unset case must return the
// 24h-in-seconds default.
func TestAdultNewestScanInterval_UnsetDefaultsTo24Hours(t *testing.T) {
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore, nil, nil, nil, nil, nil))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/settings/adult-newest-scan-interval")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	var got adultNewestScanIntervalResponse
	json.NewDecoder(resp.Body).Decode(&got)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 on unset GET, got %d", resp.StatusCode)
	}
	if got.IntervalSeconds != adultNewestScanDefaultSeconds {
		t.Errorf("expected the unset default to be %d (24h), got %d", adultNewestScanDefaultSeconds, got.IntervalSeconds)
	}
}

// TestAdultNewestScanInterval_ExplicitZeroIsOffNotDefault confirms an
// operator explicitly saving 0 means off, not "fall back to the 24h
// default" — the exact distinction the bug above collapsed.
func TestAdultNewestScanInterval_ExplicitZeroIsOffNotDefault(t *testing.T) {
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore, nil, nil, nil, nil, nil))
	defer srv.Close()

	body, _ := json.Marshal(adultNewestScanIntervalRequest{IntervalSeconds: 0})
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/settings/adult-newest-scan-interval", bytes.NewReader(body))
	putResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT failed: %v", err)
	}
	putResp.Body.Close()
	if putResp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204 storing 0 (off), got %d", putResp.StatusCode)
	}

	resp, err := http.Get(srv.URL + "/api/settings/adult-newest-scan-interval")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	var got adultNewestScanIntervalResponse
	json.NewDecoder(resp.Body).Decode(&got)
	if got.IntervalSeconds != 0 {
		t.Errorf("expected an explicitly-saved 0 to stay 0, got %d", got.IntervalSeconds)
	}
}

// TestAdultNewestScanInterval_StoredValueRoundTrips confirms a normal
// positive value round-trips unchanged.
func TestAdultNewestScanInterval_StoredValueRoundTrips(t *testing.T) {
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore, nil, nil, nil, nil, nil))
	defer srv.Close()

	body, _ := json.Marshal(adultNewestScanIntervalRequest{IntervalSeconds: 3600})
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/settings/adult-newest-scan-interval", bytes.NewReader(body))
	putResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT failed: %v", err)
	}
	putResp.Body.Close()
	if putResp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204 on PUT, got %d", putResp.StatusCode)
	}

	resp, err := http.Get(srv.URL + "/api/settings/adult-newest-scan-interval")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	var got adultNewestScanIntervalResponse
	json.NewDecoder(resp.Body).Decode(&got)
	if got.IntervalSeconds != 3600 {
		t.Errorf("expected the stored interval to round-trip, got %d", got.IntervalSeconds)
	}
}

// TestAdultNewestScanInterval_NegativeRejected confirms a negative interval
// is a 400 — mirrors TestRecheckInterval_NegativeRejected.
func TestAdultNewestScanInterval_NegativeRejected(t *testing.T) {
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore, nil, nil, nil, nil, nil))
	defer srv.Close()

	body, _ := json.Marshal(adultNewestScanIntervalRequest{IntervalSeconds: -1})
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/settings/adult-newest-scan-interval", bytes.NewReader(body))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for a negative interval, got %d", resp.StatusCode)
	}
}

// TestAdultNewestScanInterval_InvalidBody confirms a malformed JSON body is
// a 400 — mirrors TestRecheckInterval_InvalidBody.
func TestAdultNewestScanInterval_InvalidBody(t *testing.T) {
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore, nil, nil, nil, nil, nil))
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/settings/adult-newest-scan-interval", bytes.NewReader([]byte("not json")))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for a malformed body, got %d", resp.StatusCode)
	}
}
