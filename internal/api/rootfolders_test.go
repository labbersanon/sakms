package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestRootFolders_RouteRemoved confirms the deprecated
// GET /api/modes/{mode}/root-folders route (a leftover proxy to each mode's
// *arr app root-folder list, dead since every mode now owns its own library)
// is genuinely gone from the mux — a 404 from Go's own ServeMux, not just an
// unreferenced handler function that could still be silently re-wired. Every
// mode's root folder now comes exclusively from
// GET/PUT /api/modes/{mode}/library/root-folder.
func TestRootFolders_RouteRemoved(t *testing.T) {
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore, nil, nil, nil, nil, nil))
	defer srv.Close()

	for _, m := range []string{"movies", "series", "adult"} {
		resp, err := http.Get(srv.URL + "/api/modes/" + m + "/root-folders")
		if err != nil {
			t.Fatalf("GET failed: %v", err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("expected 404 for %s, got %d", m, resp.StatusCode)
		}
	}
}
