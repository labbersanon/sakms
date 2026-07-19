package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestKidsRootPath_RoundTrip(t *testing.T) {
	for _, m := range []string{"movies", "series"} {
		t.Run(m, func(t *testing.T) {
			connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
			srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore, nil, nil, nil, nil, nil))
			defer srv.Close()

			path := "/api/modes/" + m + "/rename/kids-root-path"

			resp, err := http.Get(srv.URL + path)
			if err != nil {
				t.Fatalf("GET failed: %v", err)
			}
			var got kidsRootPathResponse
			json.NewDecoder(resp.Body).Decode(&got)
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("expected 200 on unset GET, got %d", resp.StatusCode)
			}
			if got.Path != "" {
				t.Errorf("expected an empty path before anything is set, got %q", got.Path)
			}

			body, _ := json.Marshal(kidsRootPathRequest{Path: "/media/Movies (Kids)"})
			req, _ := http.NewRequest(http.MethodPut, srv.URL+path, bytes.NewReader(body))
			putResp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("PUT failed: %v", err)
			}
			putResp.Body.Close()
			if putResp.StatusCode != http.StatusNoContent {
				t.Fatalf("expected 204 on PUT, got %d", putResp.StatusCode)
			}

			resp2, err := http.Get(srv.URL + path)
			if err != nil {
				t.Fatalf("GET failed: %v", err)
			}
			defer resp2.Body.Close()
			var got2 kidsRootPathResponse
			json.NewDecoder(resp2.Body).Decode(&got2)
			if got2.Path != "/media/Movies (Kids)" {
				t.Errorf("expected the stored path to round-trip, got %q", got2.Path)
			}
		})
	}
}

// TestKidsRootPath_EmptyPathTurnsFeatureOff confirms an empty path is
// accepted (unlike the AI model setting) — "off" is a normal choice here.
func TestKidsRootPath_EmptyPathTurnsFeatureOff(t *testing.T) {
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore, nil, nil, nil, nil, nil))
	defer srv.Close()

	body, _ := json.Marshal(kidsRootPathRequest{Path: ""})
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/modes/movies/rename/kids-root-path", bytes.NewReader(body))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected an empty path to be accepted, got %d", resp.StatusCode)
	}
}

// TestKidsRootPath_RejectsAdultMode confirms Adult has no kids/general split
// concept — both GET and PUT reject it.
func TestKidsRootPath_RejectsAdultMode(t *testing.T) {
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore, nil, nil, nil, nil, nil))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/modes/adult/rename/kids-root-path")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for adult GET, got %d", resp.StatusCode)
	}

	body, _ := json.Marshal(kidsRootPathRequest{Path: "/media/Adult (Kids)"})
	putReq, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/modes/adult/rename/kids-root-path", bytes.NewReader(body))
	putResp, err := http.DefaultClient.Do(putReq)
	if err != nil {
		t.Fatalf("PUT failed: %v", err)
	}
	defer putResp.Body.Close()
	if putResp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for adult PUT, got %d", putResp.StatusCode)
	}
}
