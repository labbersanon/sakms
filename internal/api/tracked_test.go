package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/curtiswtaylorjr/sakms/internal/servarr"
)

func TestListTracked_ReturnsItemsFromTheRealApp(t *testing.T) {
	fakeRadarr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v3/movie" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[{"id":1,"title":"A Beautiful Mind","tmdbId":453,"tags":[2,3]}]`))
	}))
	defer fakeRadarr.Close()

	connStore, propStore, allowStore, settingsStore, grabsStore := testStores(t)
	ctx := context.Background()
	if err := connStore.Upsert(ctx, "radarr", fakeRadarr.URL, "test-key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), settingsStore, grabsStore))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/modes/movies/tracked")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var got []servarr.TrackedItem
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if len(got) != 1 || got[0].Title != "A Beautiful Mind" || len(got[0].TagIDs) != 2 {
		t.Fatalf("unexpected response: %+v", got)
	}
}

func TestListTracked_MissingConnection(t *testing.T) {
	connStore, propStore, allowStore, settingsStore, grabsStore := testStores(t)
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), settingsStore, grabsStore))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/modes/movies/tracked")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 when radarr isn't configured, got %d", resp.StatusCode)
	}
}
