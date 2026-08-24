package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labbersanon/sakms/internal/apidto"
)

// TestRowHidden_DefaultsToEmptyKeys proves a fresh install (nothing saved
// yet) returns an empty key list, not a 404 — no rows hidden is a normal
// state, not an error.
func TestRowHidden_DefaultsToEmptyKeys(t *testing.T) {
	connStore, propStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, nil, propStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, testFeedHealth(), rssFeedsStore, nil, nil, nil, nil, nil, nil, nil, nil, nil))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/discover/row-hidden/mainstream")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var got apidto.RowHiddenResponse
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if got.Keys == nil || len(got.Keys) != 0 {
		t.Errorf("expected an empty (not null) key list, got %+v", got.Keys)
	}
}

// TestRowHidden_SaveAndReload proves a saved hidden set round-trips, and that
// the two screens ("mainstream"/"adult") are stored independently.
func TestRowHidden_SaveAndReload(t *testing.T) {
	connStore, propStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, nil, propStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, testFeedHealth(), rssFeedsStore, nil, nil, nil, nil, nil, nil, nil, nil, nil))
	defer srv.Close()

	body, _ := json.Marshal(apidto.RowHiddenRequest{Keys: []string{"studios", "stashbox:stashdb:trending"}})
	putReq, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/discover/row-hidden/adult", bytes.NewReader(body))
	putResp, err := http.DefaultClient.Do(putReq)
	if err != nil {
		t.Fatalf("PUT failed: %v", err)
	}
	defer putResp.Body.Close()
	if putResp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", putResp.StatusCode)
	}

	getResp, err := http.Get(srv.URL + "/api/discover/row-hidden/adult")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer getResp.Body.Close()
	var got apidto.RowHiddenResponse
	if err := json.NewDecoder(getResp.Body).Decode(&got); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	want := []string{"studios", "stashbox:stashdb:trending"}
	if len(got.Keys) != len(want) {
		t.Fatalf("unexpected keys: %+v", got.Keys)
	}
	for i, k := range want {
		if got.Keys[i] != k {
			t.Errorf("key %d: expected %q, got %q", i, k, got.Keys[i])
		}
	}

	// The mainstream screen's hidden state is stored independently and stays empty.
	mainstreamResp, err := http.Get(srv.URL + "/api/discover/row-hidden/mainstream")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer mainstreamResp.Body.Close()
	var mainstreamGot apidto.RowHiddenResponse
	if err := json.NewDecoder(mainstreamResp.Body).Decode(&mainstreamGot); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if len(mainstreamGot.Keys) != 0 {
		t.Errorf("expected mainstream screen's hidden state to be unaffected, got %+v", mainstreamGot.Keys)
	}
}

// TestRowHidden_RejectsUnknownScreen proves the {screen} path parameter is
// validated against the fixed "mainstream"/"adult" set.
func TestRowHidden_RejectsUnknownScreen(t *testing.T) {
	connStore, propStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, nil, propStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, testFeedHealth(), rssFeedsStore, nil, nil, nil, nil, nil, nil, nil, nil, nil))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/discover/row-hidden/bogus")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for an unknown screen, got %d", resp.StatusCode)
	}
}
