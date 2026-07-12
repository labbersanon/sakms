package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/curtiswtaylorjr/sakms/internal/whisparrimport"
)

// TestWhisparrImport_ResolvesViaStashDBThroughTheMux drives the whole route
// end-to-end: a fake Whisparr + a fake StashDB, both configured in connStore,
// imported into libStore via POST /api/adult/import-from-whisparr.
func TestWhisparrImport_ResolvesViaStashDBThroughTheMux(t *testing.T) {
	root := t.TempDir()
	sceneFile := filepath.Join(root, "scene.mp4")
	if err := os.WriteFile(sceneFile, []byte("x"), 0o644); err != nil {
		t.Fatalf("writing scene file: %v", err)
	}

	fakeWhisparr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v3/movie" {
			t.Fatalf("unexpected whisparr request: %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[{"id":1,"title":"Whisparr Title","path":"` + sceneFile + `","rootFolderPath":"` + root + `","foreignId":"uuid-1"}]`))
	}))
	defer fakeWhisparr.Close()

	fakeStashDB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"data":{"findScene":{"id":"uuid-1","title":"StashDB Title","release_date":"2020-01-01","studio":{"name":"StashDB Studio"}}}}`))
	}))
	defer fakeStashDB.Close()

	connStore, propStore, allowStore, settingsStore, grabsStore, libStore := testStores(t)
	ctx := context.Background()
	if err := connStore.Upsert(ctx, "whisparr", fakeWhisparr.URL, "test-key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := connStore.Upsert(ctx, "stashdb", fakeStashDB.URL, "test-key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore))
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/api/adult/import-from-whisparr", "application/json", nil)
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var result whisparrimport.Result
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if len(result.Scenes) != 1 || !result.Scenes[0].Imported {
		t.Fatalf("unexpected result: %+v", result)
	}

	got, err := libStore.GetScene(ctx, "stashdb", "uuid-1")
	if err != nil {
		t.Fatalf("scene not recorded: %v", err)
	}
	if got.Title != "StashDB Title" || got.Studio != "StashDB Studio" {
		t.Errorf("scene metadata not taken from StashDB: %+v", got)
	}
}

func TestWhisparrImport_MissingConnection(t *testing.T) {
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore := testStores(t)
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore))
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/api/adult/import-from-whisparr", "application/json", nil)
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 when whisparr isn't configured, got %d", resp.StatusCode)
	}
}
