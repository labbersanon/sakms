package whisparrimport

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/curtiswtaylorjr/sakms/internal/db"
	"github.com/curtiswtaylorjr/sakms/internal/identify"
	"github.com/curtiswtaylorjr/sakms/internal/library"
	"github.com/curtiswtaylorjr/sakms/internal/servarr"
	"github.com/curtiswtaylorjr/sakms/internal/stashbox"
)

func newTestLibStore(t *testing.T) *library.Store {
	t.Helper()
	sqlDB, err := db.Open(filepath.Join(t.TempDir(), "sakms.db"))
	if err != nil {
		t.Fatalf("opening db: %v", err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	return library.New(sqlDB)
}

func newFakeWhisparr(t *testing.T, moviesJSON string) *servarr.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v3/movie" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(moviesJSON))
	}))
	t.Cleanup(srv.Close)
	return servarr.New(servarr.Config{BaseURL: srv.URL, APIKey: "test-key", App: servarr.Whisparr}, srv.Client())
}

// findSceneID pulls the requested scene id out of a stash-box FindScene
// GraphQL request body, so a fake box can answer differently per id.
func findSceneID(t *testing.T, r *http.Request) string {
	t.Helper()
	var body struct {
		Variables struct {
			ID string `json:"id"`
		} `json:"variables"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		t.Fatalf("decoding graphql request: %v", err)
	}
	return body.Variables.ID
}

// stashBoxResolving returns a fake stash-box that resolves exactly wantID to a
// fixed scene, and reports "no such scene" (findScene: null) for anything else.
func stashBoxResolving(t *testing.T, wantID, title, studio, date string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if findSceneID(t, r) == wantID {
			resp := map[string]any{"data": map[string]any{"findScene": map[string]any{
				"id": wantID, "title": title, "release_date": date,
				"studio": map[string]any{"name": studio},
			}}}
			json.NewEncoder(w).Encode(resp)
			return
		}
		w.Write([]byte(`{"data":{"findScene":null}}`))
	}
}

func newBoxSearcher(t *testing.T, stashdb, fansdb http.HandlerFunc) *identify.BoxSearcher {
	t.Helper()
	boxes := map[string]*stashbox.Client{}
	if stashdb != nil {
		srv := httptest.NewServer(stashdb)
		t.Cleanup(srv.Close)
		boxes["stashdb"] = stashbox.New(stashbox.Config{Endpoint: srv.URL, APIKey: "k"}, &http.Client{Timeout: 5 * time.Second})
	}
	if fansdb != nil {
		srv := httptest.NewServer(fansdb)
		t.Cleanup(srv.Close)
		boxes["fansdb"] = stashbox.New(stashbox.Config{Endpoint: srv.URL, APIKey: "k"}, &http.Client{Timeout: 5 * time.Second})
	}
	return identify.NewBoxSearcher(boxes, nil)
}

// writeSceneFile creates a placeholder on-disk file and returns its path, so a
// tracked item's Path passes the importer's os.Stat existence gate.
func writeSceneFile(t *testing.T, dir, name string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatalf("writing scene file: %v", err)
	}
	return path
}

func TestImport_BoxAttributionAndFallback(t *testing.T) {
	root := t.TempDir()
	stashdbFile := writeSceneFile(t, root, "a.mp4")
	fansdbFile := writeSceneFile(t, root, "b.mp4")
	tpdbFile := writeSceneFile(t, root, "c.mp4")
	orphanFile := writeSceneFile(t, root, "d.mp4")

	moviesJSON := `[
		{"id":1,"title":"Whisparr Title A","path":"` + stashdbFile + `","rootFolderPath":"` + root + `","foreignId":"uuid-stashdb"},
		{"id":2,"title":"Whisparr Title B","path":"` + fansdbFile + `","rootFolderPath":"` + root + `","foreignId":"uuid-fansdb"},
		{"id":3,"title":"Whisparr Title C","path":"` + tpdbFile + `","rootFolderPath":"` + root + `","foreignId":"tpdbId:77"},
		{"id":4,"title":"Whisparr Title D","path":"` + orphanFile + `","rootFolderPath":"` + root + `","foreignId":"uuid-orphan"}
	]`
	whisparr := newFakeWhisparr(t, moviesJSON)

	boxes := newBoxSearcher(t,
		stashBoxResolving(t, "uuid-stashdb", "StashDB Title", "StashDB Studio", "2020-01-01"),
		stashBoxResolving(t, "uuid-fansdb", "FansDB Title", "FansDB Studio", "2021-02-02"),
	)

	libStore := newTestLibStore(t)
	ctx := context.Background()
	result, err := Import(ctx, whisparr, boxes, libStore)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Scenes) != 4 {
		t.Fatalf("expected 4 scene results, got %d", len(result.Scenes))
	}
	for _, sr := range result.Scenes {
		if !sr.Imported {
			t.Fatalf("expected all scenes imported, %q wasn't: %q", sr.Title, sr.Reason)
		}
	}

	// StashDB-resolved: box + metadata from StashDB, not from Whisparr's title.
	got, err := libStore.GetScene(ctx, "stashdb", "uuid-stashdb")
	if err != nil {
		t.Fatalf("stashdb scene not recorded: %v", err)
	}
	if got.Title != "StashDB Title" || got.Studio != "StashDB Studio" || got.Date != "2020-01-01" {
		t.Errorf("stashdb scene metadata not taken from the box: %+v", got)
	}
	if got.FilePath != stashdbFile {
		t.Errorf("stashdb scene FilePath = %q, want %q", got.FilePath, stashdbFile)
	}

	// FansDB-resolved: StashDB missed, FansDB attributed it.
	fans, err := libStore.GetScene(ctx, "fansdb", "uuid-fansdb")
	if err != nil {
		t.Fatalf("fansdb scene not recorded: %v", err)
	}
	if fans.Title != "FansDB Title" || fans.Studio != "FansDB Studio" {
		t.Errorf("fansdb scene metadata not taken from the box: %+v", fans)
	}

	// tpdbId:-prefixed: box "tpdb", id stripped, no probe, Whisparr's title kept.
	tpdb, err := libStore.GetScene(ctx, "tpdb", "77")
	if err != nil {
		t.Fatalf("tpdb scene not recorded: %v", err)
	}
	if tpdb.Title != "Whisparr Title C" {
		t.Errorf("tpdb scene should keep Whisparr's title, got %q", tpdb.Title)
	}

	// Neither box resolved: stored with box="", Whisparr's title kept.
	orphan, err := libStore.GetScene(ctx, "", "uuid-orphan")
	if err != nil {
		t.Fatalf("unresolved scene not recorded with box=\"\": %v", err)
	}
	if orphan.Title != "Whisparr Title D" {
		t.Errorf("unresolved scene should keep Whisparr's title, got %q", orphan.Title)
	}
}

func TestImport_FileGone_SkippedWithReason(t *testing.T) {
	root := t.TempDir()
	gone := filepath.Join(root, "not-there.mp4")

	moviesJSON := `[{"id":1,"title":"Gone Scene","path":"` + gone + `","rootFolderPath":"` + root + `","foreignId":"uuid-gone"}]`
	whisparr := newFakeWhisparr(t, moviesJSON)
	boxes := newBoxSearcher(t, stashBoxResolving(t, "uuid-gone", "X", "Y", "2020-01-01"), nil)

	libStore := newTestLibStore(t)
	ctx := context.Background()
	result, err := Import(ctx, whisparr, boxes, libStore)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Scenes) != 1 {
		t.Fatalf("expected 1 scene result, got %d", len(result.Scenes))
	}
	if result.Scenes[0].Imported {
		t.Fatal("expected Imported=false when the file is gone")
	}
	if result.Scenes[0].Reason != "file no longer exists on disk" {
		t.Errorf("unexpected reason: %q", result.Scenes[0].Reason)
	}

	all, err := libStore.ListScenes(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(all) != 0 {
		t.Errorf("expected no scenes recorded when the file is gone, got %d", len(all))
	}
}

func TestImport_EmptyForeignID_SkippedWithReason(t *testing.T) {
	root := t.TempDir()
	file := writeSceneFile(t, root, "a.mp4")

	moviesJSON := `[{"id":1,"title":"No Foreign Id","path":"` + file + `","rootFolderPath":"` + root + `","foreignId":""}]`
	whisparr := newFakeWhisparr(t, moviesJSON)
	boxes := newBoxSearcher(t, nil, nil)

	libStore := newTestLibStore(t)
	ctx := context.Background()
	result, err := Import(ctx, whisparr, boxes, libStore)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Scenes[0].Imported {
		t.Fatal("expected Imported=false for an item with no ForeignID")
	}
	if result.Scenes[0].Reason == "" {
		t.Error("expected a non-empty skip reason")
	}
	all, err := libStore.ListScenes(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(all) != 0 {
		t.Errorf("expected no scenes recorded, got %d", len(all))
	}
}

func TestImport_Idempotent(t *testing.T) {
	root := t.TempDir()
	stashdbFile := writeSceneFile(t, root, "a.mp4")
	tpdbFile := writeSceneFile(t, root, "b.mp4")

	moviesJSON := `[
		{"id":1,"title":"A","path":"` + stashdbFile + `","rootFolderPath":"` + root + `","foreignId":"uuid-stashdb"},
		{"id":2,"title":"B","path":"` + tpdbFile + `","rootFolderPath":"` + root + `","foreignId":"tpdbId:77"}
	]`
	whisparr := newFakeWhisparr(t, moviesJSON)
	boxes := newBoxSearcher(t, stashBoxResolving(t, "uuid-stashdb", "A Title", "A Studio", "2020-01-01"), nil)

	libStore := newTestLibStore(t)
	ctx := context.Background()

	if _, err := Import(ctx, whisparr, boxes, libStore); err != nil {
		t.Fatalf("first import failed: %v", err)
	}
	first, err := libStore.ListScenes(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(first) != 2 {
		t.Fatalf("expected 2 scenes after first import, got %d", len(first))
	}

	if _, err := Import(ctx, whisparr, boxes, libStore); err != nil {
		t.Fatalf("second import failed: %v", err)
	}
	second, err := libStore.ListScenes(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(second) != 2 {
		t.Fatalf("re-running Import duplicated rows: expected 2 scenes, got %d", len(second))
	}
}
