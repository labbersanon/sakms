package rename

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/naming"
	"github.com/labbersanon/sakms/internal/proposals"
	"github.com/labbersanon/sakms/internal/stashbox"
)

func TestScanImportMovies_DestIsLibraryRootNotSource(t *testing.T) {
	source := t.TempDir()
	dest := t.TempDir()
	seedMovieRelease(t, source, "A.Beautiful.Mind.2001.1080p.BluRay.x264-GROUP")

	sess := &mode.Session{Mode: mode.Movies, TMDB: fakeTMDBSearch(t, map[string]string{
		"A Beautiful Mind 2001": `{"results":[{"id":453,"title":"A Beautiful Mind","overview":"...","release_date":"2001-12-21"}]}`,
	})}
	libStore := newTestLibraryStore(t)

	got, err := ScanImportMovies(context.Background(), sess, libStore, source, dest, DefaultMatchConfig(), nil)
	if err != nil {
		t.Fatalf("ScanImportMovies: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 proposal, got %d: %+v", len(got), got)
	}
	p := got[0]
	if p.Status != proposals.Pending || p.Title != "A Beautiful Mind" || p.TMDBID != 453 {
		t.Fatalf("unexpected proposal: %+v", p)
	}
	if p.RootFolderPath != dest {
		t.Fatalf("RootFolderPath = %q, want library dest %q", p.RootFolderPath, dest)
	}
	wantDest := ImportDestPath(p, naming.Jellyfin)
	if wantDest == "" || filepath.Dir(filepath.Dir(wantDest)) != dest {
		t.Fatalf("ImportDestPath = %q, want under %q", wantDest, dest)
	}
}

func TestScanImportMovies_ApplyMovesFileIntoLibrary(t *testing.T) {
	source := t.TempDir()
	dest := t.TempDir()
	seedMovieRelease(t, source, "A.Beautiful.Mind.2001.1080p.BluRay.x264-GROUP")
	srcFile := filepath.Join(source, "A.Beautiful.Mind.2001.1080p.BluRay.x264-GROUP", "movie.mkv")

	sess := &mode.Session{Mode: mode.Movies, TMDB: fakeTMDBSearch(t, map[string]string{
		"A Beautiful Mind 2001": `{"results":[{"id":453,"title":"A Beautiful Mind","overview":"...","release_date":"2001-12-21"}]}`,
	})}
	libStore := newTestLibraryStore(t)

	got, err := ScanImportMovies(context.Background(), sess, libStore, source, dest, DefaultMatchConfig(), nil)
	if err != nil {
		t.Fatalf("ScanImportMovies: %v", err)
	}
	if len(got) != 1 || got[0].Status != proposals.Pending {
		t.Fatalf("scan = %+v", got)
	}

	id, changes, err := ApplyLibrary(context.Background(), libStore, got[0], naming.Jellyfin, "high", nil)
	if err != nil {
		t.Fatalf("ApplyLibrary: %v", err)
	}
	if id == 0 {
		t.Fatal("expected a library item id")
	}
	if _, err := os.Stat(srcFile); !os.IsNotExist(err) {
		t.Fatalf("source should be gone after move, stat err=%v", err)
	}
	item, err := libStore.GetByTMDBID(context.Background(), mode.Movies, 453)
	if err != nil {
		t.Fatalf("GetByTMDBID: %v", err)
	}
	if item.RootFolderPath != dest {
		t.Fatalf("library root = %q, want %q", item.RootFolderPath, dest)
	}
	if _, err := os.Stat(item.FilePath); err != nil {
		t.Fatalf("dest missing: %v", err)
	}
	if len(changes) == 0 {
		t.Fatal("expected Deleted+Created path changes for the move")
	}
}

func TestImportDestPath_EmptyWhenUnmatched(t *testing.T) {
	p := proposals.Proposal{Mode: mode.Movies, Status: proposals.Unmatched, Title: "X", RootFolderPath: "/media/movies"}
	if got := ImportDestPath(p, naming.Jellyfin); got != "" {
		t.Fatalf("unmatched dest = %q, want empty", got)
	}
}

func TestScanImportAdult_DestIsLibraryRootNotSource(t *testing.T) {
	source := t.TempDir()
	dest := t.TempDir()
	scenePath := writeSceneFile(t, source, "raw-scene.mp4")

	hasher := &fakeHasher{hashes: map[string]string{scenePath: "hash1"}}
	prober := &fakeProber{durations: map[string]float64{scenePath: 1800}}
	stashdb := newFakeAdultBox(t, map[string]fakeAdultScene{
		"hash1": {id: "box-scene-1", title: "Cascade Scene"},
	}, nil, nil)
	sess := adultTestSession(t, &countingAI{}, map[string]*stashbox.Client{"stashdb": stashdb})
	libStore := newTestLibraryStore(t)

	got, err := ScanImportAdult(context.Background(), sess, libStore, hasher, prober, source, dest, DefaultMatchConfig())
	if err != nil {
		t.Fatalf("ScanImportAdult: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 proposal, got %d: %+v", len(got), got)
	}
	p := got[0]
	if p.Status != proposals.Pending || p.Title != "Cascade Scene" {
		t.Fatalf("unexpected proposal: %+v", p)
	}
	if p.RootFolderPath != dest {
		t.Fatalf("RootFolderPath = %q, want library dest %q", p.RootFolderPath, dest)
	}
	wantDest := ImportDestPath(p, naming.Jellyfin)
	if wantDest == "" || filepath.Dir(wantDest) != dest {
		t.Fatalf("ImportDestPath = %q, want under %q", wantDest, dest)
	}
}

func TestScanImportAdult_UnmatchedMintsLocalAndApplyMoves(t *testing.T) {
	source := t.TempDir()
	dest := t.TempDir()
	scenePath := writeSceneFile(t, source, "unknown-dump.mp4")

	hasher := &fakeHasher{hashes: map[string]string{scenePath: "localhash"}}
	prober := &fakeProber{durations: map[string]float64{scenePath: 900}}
	sess := adultTestSession(t, &countingAI{}, map[string]*stashbox.Client{})
	libStore := newTestLibraryStore(t)

	got, err := ScanImportAdult(context.Background(), sess, libStore, hasher, prober, source, dest, DefaultMatchConfig())
	if err != nil {
		t.Fatalf("ScanImportAdult: %v", err)
	}
	if len(got) != 1 || got[0].Status != proposals.Pending {
		t.Fatalf("scan = %+v", got)
	}
	p := got[0]
	if p.GiveBackBox != library.LocalSceneBox || p.GiveBackSceneID != library.LocalSceneID("localhash") {
		t.Fatalf("expected local identity, got box=%q scene=%q", p.GiveBackBox, p.GiveBackSceneID)
	}
	if p.Title != "unknown-dump" {
		t.Fatalf("title = %q, want stem", p.Title)
	}

	sceneID, _, changes, err := ApplyLibraryAdult(context.Background(), sess, libStore, p, "high", nil)
	if err != nil {
		t.Fatalf("ApplyLibraryAdult: %v", err)
	}
	if sceneID == 0 {
		t.Fatal("expected a scene id")
	}
	if _, err := os.Stat(scenePath); !os.IsNotExist(err) {
		t.Fatalf("source should be gone after move, stat err=%v", err)
	}
	scene, err := libStore.GetScene(context.Background(), library.LocalSceneBox, library.LocalSceneID("localhash"))
	if err != nil {
		t.Fatalf("GetScene: %v", err)
	}
	if scene.RootFolderPath != dest {
		t.Fatalf("library root = %q, want %q", scene.RootFolderPath, dest)
	}
	if _, err := os.Stat(scene.FilePath); err != nil {
		t.Fatalf("dest missing: %v", err)
	}
	if len(changes) == 0 {
		t.Fatal("expected Deleted+Created path changes for the move")
	}
}

func TestScanImportAdult_AlreadyTrackedIsPendingAlternate(t *testing.T) {
	source := t.TempDir()
	dest := t.TempDir()
	scenePath := writeSceneFile(t, source, "new-copy.mp4")

	hasher := &fakeHasher{hashes: map[string]string{scenePath: "newhash"}}
	prober := &fakeProber{durations: map[string]float64{scenePath: 1800}}
	stashdb := newFakeAdultBox(t, map[string]fakeAdultScene{
		"newhash": {id: "box-scene-1", title: "Cascade Scene"},
	}, nil, nil)
	sess := adultTestSession(t, &countingAI{}, map[string]*stashbox.Client{"stashdb": stashdb})
	libStore := newTestLibraryStore(t)
	if _, err := libStore.UpsertScene(context.Background(), library.Scene{
		Box: "stashdb", SceneID: "box-scene-1", Title: "Cascade Scene",
		Studio: "Best Studio", Date: "2022-01-01",
		FilePath: "/elsewhere/original.mp4", RootFolderPath: "/elsewhere",
	}); err != nil {
		t.Fatal(err)
	}

	got, err := ScanImportAdult(context.Background(), sess, libStore, hasher, prober, source, dest, DefaultMatchConfig())
	if err != nil {
		t.Fatalf("ScanImportAdult: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 proposal, got %+v", got)
	}
	p := got[0]
	if p.Status != proposals.Pending {
		t.Fatalf("expected Pending alternate, got %+v", p)
	}
	if !strings.HasPrefix(p.Reason, "alternate:") {
		t.Fatalf("reason = %q", p.Reason)
	}
	if p.GiveBackBox != "stashdb" || p.GiveBackSceneID != "box-scene-1" {
		t.Fatalf("identity = %q/%q", p.GiveBackBox, p.GiveBackSceneID)
	}
}

func TestScanImportAdult_IncludesSchemaNamedDumpFile(t *testing.T) {
	source := t.TempDir()
	dest := t.TempDir()
	named := writeSceneFile(t, source, "Studio - Title (2021-01-01) [phash-existing].mp4")

	hasher := &fakeHasher{hashes: map[string]string{named: "existing"}}
	prober := &fakeProber{durations: map[string]float64{named: 1800}}
	stashdb := newFakeAdultBox(t, map[string]fakeAdultScene{
		"existing": {id: "box-scene-1", title: "Cascade Scene"},
	}, nil, nil)
	sess := adultTestSession(t, &countingAI{}, map[string]*stashbox.Client{"stashdb": stashdb})

	got, err := ScanImportAdult(context.Background(), sess, newTestLibraryStore(t), hasher, prober, source, dest, DefaultMatchConfig())
	if err != nil {
		t.Fatalf("ScanImportAdult: %v", err)
	}
	if len(got) != 1 || got[0].SourcePath != named {
		t.Fatalf("expected schema-named dump file proposed, got %+v", got)
	}
}

func TestScanImportAdult_SkipsAlreadyTrackedPath(t *testing.T) {
	source := t.TempDir()
	dest := t.TempDir()
	scenePath := writeSceneFile(t, source, "raw-scene.mp4")

	sess := adultTestSession(t, &countingAI{}, map[string]*stashbox.Client{})
	libStore := newTestLibraryStore(t)
	if _, err := libStore.UpsertScene(context.Background(), library.Scene{
		Box: library.LocalSceneBox, SceneID: library.LocalSceneID("x"),
		Title: "Tracked", FilePath: scenePath, RootFolderPath: dest,
	}); err != nil {
		t.Fatal(err)
	}

	got, err := ScanImportAdult(context.Background(), sess, libStore, &fakeHasher{}, &fakeProber{}, source, dest, DefaultMatchConfig())
	if err != nil {
		t.Fatalf("ScanImportAdult: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected skip of tracked path, got %+v", got)
	}
}

func TestScanImportAdult_RequiresIdentifyConfigured(t *testing.T) {
	sess := &mode.Session{Mode: mode.Adult}
	if _, err := ScanImportAdult(context.Background(), sess, newTestLibraryStore(t), &fakeHasher{}, &fakeProber{}, t.TempDir(), t.TempDir(), DefaultMatchConfig()); err == nil {
		t.Fatal("expected an error when identification isn't configured")
	}
}

func TestImportDestPath_Adult(t *testing.T) {
	p := proposals.Proposal{
		Mode: mode.Adult, Status: proposals.Pending, Title: "Scene",
		Studio: "Studio", Date: "2021-01-01", PHash: "abc",
		GiveBackBox: library.LocalSceneBox, GiveBackSceneID: library.LocalSceneID("abc"),
		RootFolderPath: "/adult", SourcePath: "/dump/raw.mp4",
	}
	got := ImportDestPath(p, naming.Jellyfin)
	want := filepath.Join("/adult", naming.AdultFileName("Studio", "Scene", "2021-01-01", "abc", ".mp4"))
	if got != want {
		t.Fatalf("ImportDestPath = %q, want %q", got, want)
	}
}

func TestScanImportMovies_SkipsAlreadyTrackedPath(t *testing.T) {
	source := t.TempDir()
	dest := t.TempDir()
	seedMovieRelease(t, source, "A.Beautiful.Mind.2001.1080p.BluRay.x264-GROUP")
	srcFile := filepath.Join(source, "A.Beautiful.Mind.2001.1080p.BluRay.x264-GROUP", "movie.mkv")

	sess := &mode.Session{Mode: mode.Movies, TMDB: fakeTMDBSearch(t, map[string]string{
		"A Beautiful Mind 2001": `{"results":[{"id":453,"title":"A Beautiful Mind","release_date":"2001-12-21"}]}`,
	})}
	libStore := newTestLibraryStore(t)
	if _, err := libStore.Upsert(context.Background(), library.Item{
		Mode: mode.Movies, TMDBID: 453, Title: "A Beautiful Mind", FilePath: srcFile, RootFolderPath: dest,
	}); err != nil {
		t.Fatal(err)
	}

	got, err := ScanImportMovies(context.Background(), sess, libStore, source, dest, DefaultMatchConfig(), nil)
	if err != nil {
		t.Fatalf("ScanImportMovies: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected skip of tracked path, got %+v", got)
	}
}
