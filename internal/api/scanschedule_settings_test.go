package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestScanInterval_RoundTrip drives the real mux for one representative
// scan-scheduler key (dedup): GET on a blank install returns the 24h
// on-by-default cadence (this assertion used to expect 0/off, changed
// 2026-08-10 — see defaultScanIntervalSeconds), a PUT above the floor stores
// and reads back.
func TestScanInterval_RoundTrip(t *testing.T) {
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, nil, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, testFeedHealth(), rssFeedsStore, nil, nil, nil, nil, nil, nil, nil, nil))
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
	if got.IntervalSeconds != defaultScanIntervalSeconds {
		t.Errorf("expected the unset default interval to be %d (24h, on by default), got %d", defaultScanIntervalSeconds, got.IntervalSeconds)
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
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, nil, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, testFeedHealth(), rssFeedsStore, nil, nil, nil, nil, nil, nil, nil, nil))
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

// TestScanEnabled_RoundTrip covers the three new per-workflow master switches:
// unset reads as TRUE (the on-by-default mechanism), and an explicit false
// round-trips. Driven through the real mux for all three keys, since each is a
// separate route registration and a copy-paste slip there is exactly the kind
// of bug a single-key test would miss.
func TestScanEnabled_RoundTrip(t *testing.T) {
	for _, path := range []string{"rename-scan-enabled", "purge-scan-enabled", "dedup-scan-enabled"} {
		t.Run(path, func(t *testing.T) {
			connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
			srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, nil, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, testFeedHealth(), rssFeedsStore, nil, nil, nil, nil, nil, nil, nil, nil))
			defer srv.Close()

			resp, err := http.Get(srv.URL + "/api/settings/" + path)
			if err != nil {
				t.Fatalf("GET failed: %v", err)
			}
			var got scanEnabledResponse
			json.NewDecoder(resp.Body).Decode(&got)
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("expected 200 on unset GET, got %d", resp.StatusCode)
			}
			if !got.Enabled {
				t.Error("expected an unset key to read as enabled (on by default)")
			}

			body, _ := json.Marshal(scanEnabledRequest{Enabled: false})
			req, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/settings/"+path, bytes.NewReader(body))
			putResp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("PUT failed: %v", err)
			}
			putResp.Body.Close()
			if putResp.StatusCode != http.StatusNoContent {
				t.Fatalf("expected 204, got %d", putResp.StatusCode)
			}

			resp2, _ := http.Get(srv.URL + "/api/settings/" + path)
			var got2 scanEnabledResponse
			json.NewDecoder(resp2.Body).Decode(&got2)
			resp2.Body.Close()
			if got2.Enabled {
				t.Error("expected an explicit false to round-trip, not fall back to the default")
			}
		})
	}
}

// TestScanEnabled_DoesNotDisturbInterval is the spec's decoupling requirement:
// toggling a workflow off must leave its stored interval untouched, so
// re-enabling restores the operator's configured cadence rather than making
// them retype it. This is the whole reason enabled is a separate key and a
// separate endpoint instead of a "0 = off" reinterpretation of the interval.
func TestScanEnabled_DoesNotDisturbInterval(t *testing.T) {
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, nil, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, testFeedHealth(), rssFeedsStore, nil, nil, nil, nil, nil, nil, nil, nil))
	defer srv.Close()

	put := func(path string, body any) {
		t.Helper()
		b, _ := json.Marshal(body)
		req, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/settings/"+path, bytes.NewReader(b))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("PUT %s: %v", path, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNoContent {
			t.Fatalf("PUT %s: expected 204, got %d", path, resp.StatusCode)
		}
	}

	put("rename-scan-interval", scanIntervalRequest{IntervalSeconds: 7200})
	put("rename-scan-enabled", scanEnabledRequest{Enabled: false})
	put("rename-scan-enabled", scanEnabledRequest{Enabled: true})

	resp, _ := http.Get(srv.URL + "/api/settings/rename-scan-interval")
	var got scanIntervalResponse
	json.NewDecoder(resp.Body).Decode(&got)
	resp.Body.Close()
	if got.IntervalSeconds != 7200 {
		t.Errorf("toggling enabled off/on clobbered the stored interval: got %d, want 7200", got.IntervalSeconds)
	}
}

// TestScanInterval_ExplicitZeroSuppressesDefault pins the unset-vs-stored
// distinction at the API layer, the half of it a client can actually observe.
// An operator who zeroes the picker means "off," and that must survive — it
// must NOT be re-read as the 24h unset default on the next GET, which would
// silently re-enable a schedule they just turned off.
func TestScanInterval_ExplicitZeroSuppressesDefault(t *testing.T) {
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, nil, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, testFeedHealth(), rssFeedsStore, nil, nil, nil, nil, nil, nil, nil, nil))
	defer srv.Close()

	body, _ := json.Marshal(scanIntervalRequest{IntervalSeconds: 0})
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/settings/purge-scan-interval", bytes.NewReader(body))
	putResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT failed: %v", err)
	}
	putResp.Body.Close()

	resp, _ := http.Get(srv.URL + "/api/settings/purge-scan-interval")
	var got scanIntervalResponse
	json.NewDecoder(resp.Body).Decode(&got)
	resp.Body.Close()
	if got.IntervalSeconds != 0 {
		t.Errorf("an explicitly-stored 0 read back as %d — the unset default leaked over a deliberate off", got.IntervalSeconds)
	}
}

// TestDedupVMAFScanEnabled_RoundTrip covers the independent bool toggle.
func TestDedupVMAFScanEnabled_RoundTrip(t *testing.T) {
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, nil, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, testFeedHealth(), rssFeedsStore, nil, nil, nil, nil, nil, nil, nil, nil))
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
