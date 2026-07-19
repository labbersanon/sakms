package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGetIdentifyEnabledHandler_DefaultsToTrue(t *testing.T) {
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore, nil, nil, nil, nil, nil))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/modes/adult/identify-enabled")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var got identifyEnabledResponse
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if !got.Enabled {
		t.Errorf("expected identify-enabled to default to true when unset, got %v", got.Enabled)
	}
}

func TestPutThenGetIdentifyEnabled_RoundTrips(t *testing.T) {
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore, nil, nil, nil, nil, nil))
	defer srv.Close()

	body, _ := json.Marshal(identifyEnabledRequest{Enabled: false})
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/modes/adult/identify-enabled", bytes.NewReader(body))
	putResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT failed: %v", err)
	}
	putResp.Body.Close()
	if putResp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204 from PUT, got %d", putResp.StatusCode)
	}

	getResp, err := http.Get(srv.URL + "/api/modes/adult/identify-enabled")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer getResp.Body.Close()
	var got identifyEnabledResponse
	if err := json.NewDecoder(getResp.Body).Decode(&got); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if got.Enabled {
		t.Errorf("expected the stored false value to round-trip, got %v", got.Enabled)
	}
}

// The toggle is Adult-only (only Adult runs rename.Scan) — non-Adult modes 400,
// mirroring the kids-root-path applicability guard.
func TestIdentifyEnabledHandler_NonAdultMode_400(t *testing.T) {
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore, nil, nil, nil, nil, nil))
	defer srv.Close()

	getResp, err := http.Get(srv.URL + "/api/modes/movies/identify-enabled")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	getResp.Body.Close()
	if getResp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 from GET on a non-adult mode, got %d", getResp.StatusCode)
	}

	body, _ := json.Marshal(identifyEnabledRequest{Enabled: true})
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/modes/series/identify-enabled", bytes.NewReader(body))
	putResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT failed: %v", err)
	}
	putResp.Body.Close()
	if putResp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 from PUT on a non-adult mode, got %d", putResp.StatusCode)
	}
}
