package rename

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/labbersanon/sakms/internal/naming"
	"github.com/labbersanon/sakms/internal/stashbox"
)

// TestOrganizeImportedAdult_RenamesAndTracks is the grab-import organize path:
// a confident phash match relocates via AdultFileName and UpsertScenes without
// a Rename proposal.
func TestOrganizeImportedAdult_RenamesAndTracks(t *testing.T) {
	root := t.TempDir()
	scenePath := writeSceneFile(t, root, "[Familylust.com] Raw Title.mp4")

	hasher := &fakeHasher{hashes: map[string]string{scenePath: "hash1"}}
	prober := &fakeProber{durations: map[string]float64{scenePath: 1800}}
	stashdb := newFakeAdultBox(t, map[string]struct{ id, title string }{
		"hash1": {id: "box-scene-1", title: "Cascade Scene"},
	}, nil, nil)
	sess := adultTestSession(t, &countingAI{}, map[string]*stashbox.Client{"stashdb": stashdb})
	libStore := newTestLibraryStore(t)

	final, changes, err := OrganizeImportedAdult(context.Background(), sess, libStore, hasher, prober, scenePath, root, "bluray")
	if err != nil {
		t.Fatalf("OrganizeImportedAdult: %v", err)
	}
	if final == "" {
		t.Fatal("expected a final path after successful organize")
	}
	if !naming.MatchesAdultSchema(final) {
		t.Errorf("expected AdultFileName schema on %q", final)
	}
	if !strings.Contains(filepath.Base(final), "Cascade Scene") {
		t.Errorf("expected catalog title in filename, got %q", filepath.Base(final))
	}
	if _, err := os.Stat(scenePath); !os.IsNotExist(err) {
		t.Errorf("expected source removed after relocate, stat err=%v", err)
	}
	if len(changes) == 0 {
		t.Error("expected Deleted+Created path changes")
	}
	sc, err := libStore.GetScene(context.Background(), "stashdb", "box-scene-1")
	if err != nil {
		t.Fatalf("GetScene: %v", err)
	}
	if sc.FilePath != final || sc.QualityTier != "bluray" {
		t.Errorf("library row = %+v, want path %q tier bluray", sc, final)
	}
}

func TestOrganizeImportedAdult_SoftSkipWhenUnmatched(t *testing.T) {
	root := t.TempDir()
	scenePath := writeSceneFile(t, root, "unknown-scene.mp4")

	hasher := &fakeHasher{hashes: map[string]string{scenePath: "nope"}}
	prober := &fakeProber{durations: map[string]float64{scenePath: 100}}
	stashdb := newFakeAdultBox(t, map[string]struct{ id, title string }{}, nil, nil)
	sess := adultTestSession(t, &countingAI{}, map[string]*stashbox.Client{"stashdb": stashdb})
	libStore := newTestLibraryStore(t)

	final, changes, err := OrganizeImportedAdult(context.Background(), sess, libStore, hasher, prober, scenePath, root, "web")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if final != "" || changes != nil {
		t.Fatalf("expected soft skip (empty), got final=%q changes=%v", final, changes)
	}
	if _, err := os.Stat(scenePath); err != nil {
		t.Errorf("source should remain when organize soft-skips: %v", err)
	}
	scenes, err := libStore.ListScenes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(scenes) != 0 {
		t.Errorf("expected no library rows, got %d", len(scenes))
	}
}

func TestOrganizeImportedAdult_SkipsAlreadySchemaConformant(t *testing.T) {
	root := t.TempDir()
	name := naming.AdultFileName("Studio", "Title", "2020-01-01", "abc", ".mp4")
	scenePath := writeSceneFile(t, root, name)

	final, changes, err := OrganizeImportedAdult(context.Background(), nil, newTestLibraryStore(t), nil, nil, scenePath, root, "web")
	if err != nil {
		t.Fatal(err)
	}
	if final != scenePath || changes != nil {
		t.Fatalf("expected no-op return of same path, got final=%q changes=%v", final, changes)
	}
}
