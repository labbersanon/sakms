package rename

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/proposals"
	"github.com/labbersanon/sakms/internal/stashbox"
)

// writeSceneFile drops a fake scene video file into dir and returns its path.
func writeSceneFile(t *testing.T, dir, name string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("fake video data"), 0o644); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
	return path
}

func TestScanLibraryAdult_ProducesPendingProposalForNewScene(t *testing.T) {
	root := t.TempDir()
	scenePath := writeSceneFile(t, root, "scene1.mp4")

	hasher := &fakeHasher{hashes: map[string]string{scenePath: "hash1"}}
	prober := &fakeProber{durations: map[string]float64{scenePath: 1800}}
	stashdb := newFakeAdultBox(t, map[string]struct{ id, title string }{
		"hash1": {id: "box-scene-1", title: "Cascade Scene"},
	}, nil, nil)
	sess := adultTestSession(t, &countingAI{}, map[string]*stashbox.Client{"stashdb": stashdb})
	libStore := newTestLibraryStore(t)

	got, err := ScanLibraryAdult(context.Background(), sess, libStore, hasher, prober, root)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 proposal, got %d: %+v", len(got), got)
	}
	p := got[0]
	if p.Status != proposals.Pending || p.Title != "Cascade Scene" || p.ForeignID != "box-scene-1" {
		t.Fatalf("expected a fingerprint-cascade hit, got %+v", p)
	}
	if p.GiveBackBox != "stashdb" || p.GiveBackSceneID != "box-scene-1" {
		t.Errorf("expected give-back target captured from the cascade match, got box=%q scene=%q", p.GiveBackBox, p.GiveBackSceneID)
	}
	if p.PHash != "hash1" || p.DurationSeconds != 1800 {
		t.Errorf("expected local phash+prober duration, got phash=%q duration=%d", p.PHash, p.DurationSeconds)
	}
	if p.SourcePath != scenePath {
		t.Errorf("expected SourcePath to be the resolved video file %q, got %q", scenePath, p.SourcePath)
	}
}

func TestScanLibraryAdult_RequiresIdentifyConfigured(t *testing.T) {
	sess := &mode.Session{Mode: mode.Adult}
	if _, err := ScanLibraryAdult(context.Background(), sess, newTestLibraryStore(t), &fakeHasher{}, &fakeProber{}, t.TempDir()); err == nil {
		t.Fatal("expected an error when identification isn't configured")
	}
}

func TestScanLibraryAdult_RequiresRootFolderPath(t *testing.T) {
	sess := adultTestSession(t, &countingAI{}, map[string]*stashbox.Client{})
	if _, err := ScanLibraryAdult(context.Background(), sess, newTestLibraryStore(t), &fakeHasher{}, &fakeProber{}, ""); err == nil {
		t.Fatal("expected an error when no root folder path is configured")
	}
}

// TestScanLibraryAdult_SkipsAlreadyTrackedScene is the key improvement the
// library-owned (box, scene_id) key unlocks: a newly-found file that
// identifies to a scene already tracked in library_scenes is demoted to
// Unmatched (pre-Apply dedup) rather than being re-proposed — the Whisparr
// path couldn't do this.
func TestScanLibraryAdult_SkipsAlreadyTrackedScene(t *testing.T) {
	root := t.TempDir()
	scenePath := writeSceneFile(t, root, "new-copy.mp4")

	hasher := &fakeHasher{hashes: map[string]string{scenePath: "hash1"}}
	prober := &fakeProber{durations: map[string]float64{scenePath: 1800}}
	stashdb := newFakeAdultBox(t, map[string]struct{ id, title string }{
		"hash1": {id: "box-scene-1", title: "Cascade Scene"},
	}, nil, nil)
	sess := adultTestSession(t, &countingAI{}, map[string]*stashbox.Client{"stashdb": stashdb})
	libStore := newTestLibraryStore(t)

	// Already tracked at a DIFFERENT path (so ScanRootFolder still surfaces the
	// new copy — it's the identity, not the path, that makes it a duplicate).
	if _, err := libStore.UpsertScene(context.Background(), library.Scene{
		Box: "stashdb", SceneID: "box-scene-1", Title: "Cascade Scene",
		FilePath: "/elsewhere/original.mp4", RootFolderPath: "/elsewhere",
	}); err != nil {
		t.Fatalf("seeding tracked scene: %v", err)
	}

	got, err := ScanLibraryAdult(context.Background(), sess, libStore, hasher, prober, root)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || got[0].Status != proposals.Unmatched {
		t.Fatalf("expected the already-tracked scene to surface as unmatched rather than re-proposed, got %+v", got)
	}
	if got[0].ForeignID != "" {
		t.Errorf("expected no ForeignID on a dedup-skipped proposal, got %q", got[0].ForeignID)
	}
}

// TestScanLibraryAdult_SkipsAlreadyConformantName proves MatchesAdultSchema is
// wired in: a file already carrying the [phash-...] tag is never proposed,
// while a non-conformant sibling in the same root still is.
func TestScanLibraryAdult_SkipsAlreadyConformantName(t *testing.T) {
	root := t.TempDir()
	writeSceneFile(t, root, "Studio - Title (2021-01-01) [phash-existing].mp4")
	fresh := writeSceneFile(t, root, "raw-release.mp4")

	hasher := &fakeHasher{hashes: map[string]string{fresh: "hash1"}}
	prober := &fakeProber{durations: map[string]float64{fresh: 1800}}
	stashdb := newFakeAdultBox(t, map[string]struct{ id, title string }{
		"hash1": {id: "box-scene-1", title: "Cascade Scene"},
	}, nil, nil)
	sess := adultTestSession(t, &countingAI{}, map[string]*stashbox.Client{"stashdb": stashdb})

	got, err := ScanLibraryAdult(context.Background(), sess, newTestLibraryStore(t), hasher, prober, root)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || got[0].SourcePath != fresh {
		t.Fatalf("expected only the non-conformant file proposed, got %+v", got)
	}
}

func TestApplyLibraryAdult_RelocatesAndRecordsScene(t *testing.T) {
	root := t.TempDir()
	sourcePath := writeSceneFile(t, root, "raw-release.mp4")

	sess := adultTestSession(t, &countingAI{}, map[string]*stashbox.Client{})
	libStore := newTestLibraryStore(t)
	p := proposals.Proposal{
		ID: 1, Status: proposals.Pending, Title: "Scene Title", Studio: "Brazzers", Date: "2021-03-04",
		ForeignID: "box-scene-1", ItemType: "scene",
		GiveBackBox: "stashdb", GiveBackSceneID: "box-scene-1", PHash: "hash1", DurationSeconds: 1800,
		SourcePath: sourcePath, RootFolderPath: root,
	}

	sceneID, _, changes, err := ApplyLibraryAdult(context.Background(), sess, libStore, p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sceneID == 0 {
		t.Error("expected a nonzero scene id")
	}

	wantDest := filepath.Join(root, "Brazzers - Scene Title (2021-03-04) [phash-hash1].mp4")
	if _, err := os.Stat(sourcePath); !os.IsNotExist(err) {
		t.Errorf("expected the source file to be gone, stat returned: %v", err)
	}
	if data, err := os.ReadFile(wantDest); err != nil || string(data) != "fake video data" {
		t.Errorf("expected the file to have moved to %q intact, err=%v data=%q", wantDest, err, data)
	}

	scene, err := libStore.GetScene(context.Background(), "stashdb", "box-scene-1")
	if err != nil {
		t.Fatalf("expected the scene to be recorded, got: %v", err)
	}
	if scene.Title != "Scene Title" || scene.Studio != "Brazzers" || scene.Date != "2021-03-04" || scene.FilePath != wantDest || scene.PHash != "hash1" {
		t.Errorf("unexpected recorded scene: %+v", scene)
	}
	if scene.PHashFileSize == 0 || scene.PHashFileMTime == "" {
		t.Errorf("expected the phash file-identity key to be populated, got size=%d mtime=%q", scene.PHashFileSize, scene.PHashFileMTime)
	}

	want := []mode.PathChange{{Path: sourcePath, Kind: mode.Deleted}, {Path: wantDest, Kind: mode.Created}}
	if len(changes) != 2 || changes[0] != want[0] || changes[1] != want[1] {
		t.Errorf("expected changes %+v, got %+v", want, changes)
	}
}

func TestApplyLibraryAdult_RejectsNonPendingProposal(t *testing.T) {
	sess := adultTestSession(t, &countingAI{}, map[string]*stashbox.Client{})
	libStore := newTestLibraryStore(t)
	for _, status := range []proposals.Status{proposals.Applied, proposals.Dismissed, proposals.Unmatched} {
		if _, _, _, err := ApplyLibraryAdult(context.Background(), sess, libStore, proposals.Proposal{Status: status}); err == nil {
			t.Errorf("expected ApplyLibraryAdult to refuse a %q proposal", status)
		}
	}
}

func TestApplyLibraryAdult_RefusesProposalWithoutSceneIdentifier(t *testing.T) {
	sess := adultTestSession(t, &countingAI{}, map[string]*stashbox.Client{})
	p := proposals.Proposal{ID: 1, Status: proposals.Pending, Title: "No Identity", SourcePath: "/tmp/x.mp4", RootFolderPath: "/tmp"}
	if _, _, _, err := ApplyLibraryAdult(context.Background(), sess, newTestLibraryStore(t), p); err == nil {
		t.Fatal("expected ApplyLibraryAdult to refuse a proposal with no (box, scene_id) identity")
	}
}

// TestApplyLibraryAdult_FiresFingerprintGiveBack proves give-back still fires
// through the library-backed Apply (Stash-free), carrying the proposal's local
// phash + prober duration to the origin box.
func TestApplyLibraryAdult_FiresFingerprintGiveBack(t *testing.T) {
	root := t.TempDir()
	sourcePath := writeSceneFile(t, root, "raw-release.mp4")

	rec := &giveBackRecord{}
	stashdb := newFakeAdultBox(t, nil, rec, nil)
	sess := adultTestSession(t, &countingAI{}, map[string]*stashbox.Client{"stashdb": stashdb})
	p := proposals.Proposal{
		ID: 1, Status: proposals.Pending, Title: "Scene Title", Studio: "Brazzers", Date: "2021-03-04",
		GiveBackBox: "stashdb", GiveBackSceneID: "box-scene-1", PHash: "hash1", DurationSeconds: 1800,
		SourcePath: sourcePath, RootFolderPath: root,
	}

	_, submitted, _, err := ApplyLibraryAdult(context.Background(), sess, newTestLibraryStore(t), p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !submitted {
		t.Fatal("expected ApplyLibraryAdult to report fingerprint give-back as submitted")
	}
	if !rec.submitted || rec.hash != "hash1" || rec.duration != 1800 || rec.sceneID != "box-scene-1" {
		t.Errorf("expected give-back to carry the proposal's phash/duration/scene, got %+v", rec)
	}
}

// TestScanLibraryAdult_ThenApply_RoundTrip drives a scan-produced proposal
// through Apply and re-scans, proving the renamed+recorded scene is no longer
// re-proposed (both because it's now tracked and because its name matches
// MatchesAdultSchema).
func TestScanLibraryAdult_ThenApply_RoundTrip(t *testing.T) {
	root := t.TempDir()
	scenePath := writeSceneFile(t, root, "raw-release.mp4")

	hasher := &fakeHasher{hashes: map[string]string{scenePath: "hash1"}}
	prober := &fakeProber{durations: map[string]float64{scenePath: 1800}}
	stashdb := newFakeAdultBox(t, map[string]struct{ id, title string }{
		"hash1": {id: "box-scene-1", title: "Cascade Scene"},
	}, nil, nil)
	sess := adultTestSession(t, &countingAI{}, map[string]*stashbox.Client{"stashdb": stashdb})
	libStore := newTestLibraryStore(t)

	got, err := ScanLibraryAdult(context.Background(), sess, libStore, hasher, prober, root)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(got) != 1 || got[0].Status != proposals.Pending {
		t.Fatalf("expected one Pending proposal from the scan, got %+v", got)
	}

	sceneID, _, _, err := ApplyLibraryAdult(context.Background(), sess, libStore, got[0])
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if sceneID == 0 {
		t.Fatal("expected a nonzero scene id from apply")
	}
	wantDest := filepath.Join(root, "Cascade Scene [phash-hash1].mp4")
	if _, err := os.Stat(wantDest); err != nil {
		t.Fatalf("expected the renamed scene on disk at %q, got: %v", wantDest, err)
	}

	// Re-scan: the newly-renamed file hashes to the same thing, but is both
	// tracked and schema-conformant, so nothing new is proposed.
	hasher.hashes[wantDest] = "hash1"
	prober.durations[wantDest] = 1800
	again, err := ScanLibraryAdult(context.Background(), sess, libStore, hasher, prober, root)
	if err != nil {
		t.Fatalf("re-scan: %v", err)
	}
	if len(again) != 0 {
		t.Fatalf("expected the applied scene to not be re-proposed, got %+v", again)
	}
}
