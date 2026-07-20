package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labbersanon/sakms/internal/apidto"
)

// TestRowOrder_DefaultsToEmptyKeys proves a fresh install (nothing saved
// yet) returns an empty key list, not a 404 — a missing display-order hint
// is a normal state, not an error.
func TestRowOrder_DefaultsToEmptyKeys(t *testing.T) {
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore, nil, nil, nil, nil, nil))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/discover/row-order/mainstream")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var got apidto.RowOrderResponse
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if got.Keys == nil || len(got.Keys) != 0 {
		t.Errorf("expected an empty (not null) key list, got %+v", got.Keys)
	}
}

// TestRowOrder_SaveAndReload proves a saved order round-trips, and that the
// two screens ("mainstream"/"adult") are stored independently.
func TestRowOrder_SaveAndReload(t *testing.T) {
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore, nil, nil, nil, nil, nil))
	defer srv.Close()

	body, _ := json.Marshal(apidto.RowOrderRequest{Keys: []string{"builtin:trending-movies", "slider:4", "rssfeed:2"}})
	putReq, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/discover/row-order/mainstream", bytes.NewReader(body))
	putResp, err := http.DefaultClient.Do(putReq)
	if err != nil {
		t.Fatalf("PUT failed: %v", err)
	}
	defer putResp.Body.Close()
	if putResp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", putResp.StatusCode)
	}

	getResp, err := http.Get(srv.URL + "/api/discover/row-order/mainstream")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer getResp.Body.Close()
	var got apidto.RowOrderResponse
	if err := json.NewDecoder(getResp.Body).Decode(&got); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	want := []string{"builtin:trending-movies", "slider:4", "rssfeed:2"}
	if len(got.Keys) != len(want) {
		t.Fatalf("unexpected keys: %+v", got.Keys)
	}
	for i, k := range want {
		if got.Keys[i] != k {
			t.Errorf("key %d: expected %q, got %q", i, k, got.Keys[i])
		}
	}

	// The adult screen's order is stored independently and stays empty.
	adultResp, err := http.Get(srv.URL + "/api/discover/row-order/adult")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer adultResp.Body.Close()
	var adultGot apidto.RowOrderResponse
	if err := json.NewDecoder(adultResp.Body).Decode(&adultGot); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if len(adultGot.Keys) != 0 {
		t.Errorf("expected adult screen's order to be unaffected, got %+v", adultGot.Keys)
	}
}

// TestRowOrder_RejectsUnknownScreen proves the {screen} path parameter is
// validated against the fixed "mainstream"/"adult" set.
func TestRowOrder_RejectsUnknownScreen(t *testing.T) {
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore, nil, nil, nil, nil, nil))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/discover/row-order/bogus")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for an unknown screen, got %d", resp.StatusCode)
	}
}
