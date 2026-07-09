package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestListRootFolders_ReturnsPathsFromTheRealApp proves the settings-UI
// contract: the picker gets the mode's ACTUAL root folders, not a free-text
// field a user could mistype or let go stale.
func TestListRootFolders_ReturnsPathsFromTheRealApp(t *testing.T) {
	fakeRadarr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v3/rootfolder" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[
			{"id":1,"path":"/media/Movies","accessible":true,"freeSpace":1,"unmappedFolders":[]},
			{"id":2,"path":"/media/Movies (Kids)","accessible":false,"freeSpace":0,"unmappedFolders":[]}
		]`))
	}))
	defer fakeRadarr.Close()

	connStore, propStore, allowStore, settingsStore, grabsStore := testStores(t)
	ctx := context.Background()
	if err := connStore.Upsert(ctx, "radarr", fakeRadarr.URL, "test-key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), settingsStore, grabsStore))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/modes/movies/root-folders")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var got []rootFolderSummary
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 root folders, got %+v", got)
	}
	if got[0].Path != "/media/Movies" || !got[0].Accessible {
		t.Errorf("unexpected first entry: %+v", got[0])
	}
	if got[1].Path != "/media/Movies (Kids)" || got[1].Accessible {
		t.Errorf("unexpected second entry: %+v", got[1])
	}
}

// TestListRootFolders_MissingConnection confirms an unconfigured mode fails
// fast with a clear error rather than a confusing empty list.
func TestListRootFolders_MissingConnection(t *testing.T) {
	connStore, propStore, allowStore, settingsStore, grabsStore := testStores(t)
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), settingsStore, grabsStore))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/modes/movies/root-folders")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 when radarr isn't configured, got %d", resp.StatusCode)
	}
}
