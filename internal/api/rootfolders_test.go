package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestListRootFolders_NotApplicableToAnyMode confirms every mode gets a clear
// 400 pointing at the library root-folder setting, instead of a nil-Servarr
// crash — no mode has a *arr app to ask anymore (Movies/Series since their
// Radarr/Sonarr eliminations, Adult since Stage 4's Whisparr elimination).
func TestListRootFolders_NotApplicableToAnyMode(t *testing.T) {
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore := testStores(t)
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore))
	defer srv.Close()

	for _, m := range []string{"movies", "series", "adult"} {
		resp, err := http.Get(srv.URL + "/api/modes/" + m + "/root-folders")
		if err != nil {
			t.Fatalf("GET failed: %v", err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("expected 400 for %s, got %d", m, resp.StatusCode)
		}
	}
}
