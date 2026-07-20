package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestLibraryRootFolder_Adult_PutThenGet_RoundTrip proves Adult now shares
// Movies/Series' free-typed library-root-folder route: a PUT stores the path
// and a subsequent GET reads it back. Adult previously 400'd on this route
// (no key existed); adultLibraryRootFolderKey now makes it work. The old
// generic /root-folders LISTING route (once a proxy to each mode's *arr app)
// has been removed entirely — see TestRootFolders_RouteRemoved in
// rootfolders_test.go.
func TestLibraryRootFolder_Adult_PutThenGet_RoundTrip(t *testing.T) {
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore, nil, nil, nil, nil))
	defer srv.Close()

	const rootPath = "/media/Adult"

	putBody, _ := json.Marshal(libraryRootFolderRequest{Path: rootPath})
	putReq, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/modes/adult/library/root-folder", bytes.NewReader(putBody))
	putResp, err := http.DefaultClient.Do(putReq)
	if err != nil {
		t.Fatalf("PUT failed: %v", err)
	}
	putResp.Body.Close()
	if putResp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204 from PUT, got %d", putResp.StatusCode)
	}

	getResp, err := http.Get(srv.URL + "/api/modes/adult/library/root-folder")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer getResp.Body.Close()
	if getResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 from GET, got %d", getResp.StatusCode)
	}
	var got libraryRootFolderResponse
	if err := json.NewDecoder(getResp.Body).Decode(&got); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if got.Path != rootPath {
		t.Fatalf("expected round-tripped path %q, got %q", rootPath, got.Path)
	}
}

// TestQualityPrefs_DefaultsWhenUnset proves GET returns quality.Default
// ("high"), maxResolution=0 (no cap), and protocol="" (no preference) for a
// mode that's never had a PUT — matching quality.ProfileFor's own
// zero-config fallback for the first two, and the natural "no opinion yet"
// default for the third.
func TestQualityPrefs_DefaultsWhenUnset(t *testing.T) {
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore, nil, nil, nil, nil))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/modes/movies/quality-prefs")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var got qualityPrefsResponse
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if got.Tier != "high" || got.MaxResolution != 0 || got.Protocol != "" {
		t.Errorf("expected defaults {tier:high maxResolution:0 protocol:\"\"}, got %+v", got)
	}
}

// TestQualityPrefs_PutThenGet_RoundTrip_IncludingProtocol_AllModes proves the
// full PUT-then-GET round trip for all three fields, for Movies, Series, AND
// Adult — Adult previously had no meaningful quality-prefs route (the
// handlers have no mode guard, so it never 400'd, but nothing in the product
// wrote or read those keys); the Discover detail popup's availability grid
// now applies to Adult too, so this proves it works exactly like the other
// two modes with zero special-casing.
func TestQualityPrefs_PutThenGet_RoundTrip_IncludingProtocol_AllModes(t *testing.T) {
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore, nil, nil, nil, nil))
	defer srv.Close()

	for _, m := range []string{"movies", "series", "adult"} {
		t.Run(m, func(t *testing.T) {
			putBody, _ := json.Marshal(qualityPrefsRequest{Tier: "lossless", MaxResolution: 1080, Protocol: "usenet"})
			putReq, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/modes/"+m+"/quality-prefs", bytes.NewReader(putBody))
			putResp, err := http.DefaultClient.Do(putReq)
			if err != nil {
				t.Fatalf("PUT failed: %v", err)
			}
			putResp.Body.Close()
			if putResp.StatusCode != http.StatusNoContent {
				t.Fatalf("expected 204 from PUT, got %d", putResp.StatusCode)
			}

			getResp, err := http.Get(srv.URL + "/api/modes/" + m + "/quality-prefs")
			if err != nil {
				t.Fatalf("GET failed: %v", err)
			}
			defer getResp.Body.Close()
			var got qualityPrefsResponse
			if err := json.NewDecoder(getResp.Body).Decode(&got); err != nil {
				t.Fatalf("decoding response: %v", err)
			}
			if got.Tier != "lossless" || got.MaxResolution != 1080 || got.Protocol != "usenet" {
				t.Errorf("expected round-tripped {tier:lossless maxResolution:1080 protocol:usenet}, got %+v", got)
			}
		})
	}
}

// TestQualityPrefs_PutValidation covers all three fields' rejection paths,
// including the new protocol field.
func TestQualityPrefs_PutValidation(t *testing.T) {
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore, nil, nil, nil, nil))
	defer srv.Close()

	cases := []struct {
		name string
		req  qualityPrefsRequest
	}{
		{"bad tier", qualityPrefsRequest{Tier: "ultra", MaxResolution: 0, Protocol: ""}},
		{"bad maxResolution", qualityPrefsRequest{Tier: "high", MaxResolution: 999, Protocol: ""}},
		{"bad protocol", qualityPrefsRequest{Tier: "high", MaxResolution: 0, Protocol: "bittorrent"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body, _ := json.Marshal(tc.req)
			req, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/modes/movies/quality-prefs", bytes.NewReader(body))
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("PUT failed: %v", err)
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusBadRequest {
				t.Errorf("expected 400 for %+v, got %d", tc.req, resp.StatusCode)
			}
		})
	}
}
