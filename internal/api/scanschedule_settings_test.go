package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestScanInterval_RoundTrip drives the real mux for one representative
// scan-scheduler key (dedup): GET on a blank install returns 0 (off — the
// opt-in default), a PUT above the floor stores and reads back.
func TestScanInterval_RoundTrip(t *testing.T) {
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, testFeedHealth(), rssFeedsStore, nil, nil, nil, nil, nil, nil))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/settings/dedup-scan-interval")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	var got scanIntervalResponse
	json.NewDecoder(resp.Body).Decode(&got)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 on unset GET, got %d", resp.StatusCode)
	}
	if got.IntervalSeconds != 0 {
		t.Errorf("expected the default interval to be 0 (off), got %d", got.IntervalSeconds)
	}

	body, _ := json.Marshal(scanIntervalRequest{IntervalSeconds: 3600})
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/settings/dedup-scan-interval", bytes.NewReader(body))
	putResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT failed: %v", err)
	}
	putResp.Body.Close()
	if putResp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", putResp.StatusCode)
	}

	resp2, _ := http.Get(srv.URL + "/api/settings/dedup-scan-interval")
	var got2 scanIntervalResponse
	json.NewDecoder(resp2.Body).Decode(&got2)
	resp2.Body.Close()
	if got2.IntervalSeconds != 3600 {
		t.Errorf("expected round-tripped interval 3600, got %d", got2.IntervalSeconds)
	}
}

// TestScanInterval_BelowFloorRejected proves the 60s floor (unlike
// recheck/adult-newest-scan, which accept any positive value) is actually
// enforced end-to-end through the HTTP handler, not just in
// storeIntervalSeconds' own unit-level logic.
func TestScanInterval_BelowFloorRejected(t *testing.T) {
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, testFeedHealth(), rssFeedsStore, nil, nil, nil, nil, nil, nil))
	defer srv.Close()

	for _, tc := range []struct {
		name    string
		seconds int
		wantOK  bool
	}{
		{"zero (off) is always allowed", 0, true},
		{"below floor rejected", 30, false},
		{"at floor allowed", 60, true},
		{"above floor allowed", 120, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body, _ := json.Marshal(scanIntervalRequest{IntervalSeconds: tc.seconds})
			req, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/settings/rename-scan-interval", bytes.NewReader(body))
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("PUT failed: %v", err)
			}
			resp.Body.Close()
			gotOK := resp.StatusCode == http.StatusNoContent
			if gotOK != tc.wantOK {
				t.Errorf("intervalSeconds=%d: got status %d (ok=%v), want ok=%v", tc.seconds, resp.StatusCode, gotOK, tc.wantOK)
			}
			if !tc.wantOK && resp.StatusCode != http.StatusBadRequest {
				t.Errorf("intervalSeconds=%d: expected 400 on rejection, got %d", tc.seconds, resp.StatusCode)
			}
		})
	}
}

// TestDedupVMAFScanEnabled_RoundTrip covers the independent bool toggle.
func TestDedupVMAFScanEnabled_RoundTrip(t *testing.T) {
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, testFeedHealth(), rssFeedsStore, nil, nil, nil, nil, nil, nil))
	defer srv.Close()

	resp, _ := http.Get(srv.URL + "/api/settings/dedup-vmaf-scan-enabled")
	var got dedupVMAFScanEnabledResponse
	json.NewDecoder(resp.Body).Decode(&got)
	resp.Body.Close()
	if got.Enabled {
		t.Error("expected eager VMAF to default to disabled")
	}

	body, _ := json.Marshal(dedupVMAFScanEnabledRequest{Enabled: true})
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/settings/dedup-vmaf-scan-enabled", bytes.NewReader(body))
	putResp, _ := http.DefaultClient.Do(req)
	putResp.Body.Close()
	if putResp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", putResp.StatusCode)
	}

	resp2, _ := http.Get(srv.URL + "/api/settings/dedup-vmaf-scan-enabled")
	var got2 dedupVMAFScanEnabledResponse
	json.NewDecoder(resp2.Body).Decode(&got2)
	resp2.Body.Close()
	if !got2.Enabled {
		t.Error("expected enabled=true to round-trip")
	}
}
