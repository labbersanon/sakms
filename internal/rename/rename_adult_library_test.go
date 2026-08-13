package rename

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/labbersanon/sakms/internal/identify"
	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mediainfo"
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
	stashdb := newFakeAdultBox(t, map[string]struct{ id, title, image string }{
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

// TestScanLibraryAdult_AlreadyTrackedIsPendingAlternate is the B3 regression test.
// A newly-found file that identifies to an already-tracked scene must now surface as
// Pending with an "alternate:" reason (not Unmatched "leaving in place for manual review").
// The proposal still carries the file's own PHash so applyAdultAlternate can build
// both filenames correctly.
func TestScanLibraryAdult_AlreadyTrackedIsPendingAlternate(t *testing.T) {
	root := t.TempDir()
	scenePath := writeSceneFile(t, root, "new-copy.mp4")

	hasher := &fakeHasher{hashes: map[string]string{scenePath: "newhash"}}
	prober := &fakeProber{durations: map[string]float64{scenePath: 1800}}
	stashdb := newFakeAdultBox(t, map[string]struct{ id, title, image string }{
		"newhash": {id: "box-scene-1", title: "Cascade Scene"},
	}, nil, nil)
	sess := adultTestSession(t, &countingAI{}, map[string]*stashbox.Client{"stashdb": stashdb})
	libStore := newTestLibraryStore(t)

	// Already tracked at a DIFFERENT path (so ScanRootFolder still surfaces the
	// new copy — it's the identity, not the path, that makes it a duplicate).
	if _, err := libStore.UpsertScene(context.Background(), library.Scene{
		Box: "stashdb", SceneID: "box-scene-1", Title: "Cascade Scene",
		Studio: "Best Studio", Date: "2022-01-01",
		FilePath: "/elsewhere/original.mp4", RootFolderPath: "/elsewhere",
	}); err != nil {
		t.Fatalf("seeding tracked scene: %v", err)
	}

	got, err := ScanLibraryAdult(context.Background(), sess, libStore, hasher, prober, root)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 proposal, got %d: %+v", len(got), got)
	}
	p := got[0]
	if p.Status != proposals.Pending {
		t.Errorf("expected Pending (alternate), got %q", p.Status)
	}
	if !strings.HasPrefix(p.Reason, "alternate:") {
		t.Errorf("expected reason to start with 'alternate:', got %q", p.Reason)
	}
	// Title/Studio/Date come from the tracked row, not the identify result.
	if p.Title != "Cascade Scene" || p.Studio != "Best Studio" || p.Date != "2022-01-01" {
		t.Errorf("expected identity from the tracked row, got title=%q studio=%q date=%q", p.Title, p.Studio, p.Date)
	}
	// The proposal's own PHash must be the file's hash (for the alternate filename).
	if p.PHash != "newhash" {
		t.Errorf("expected PHash from the file's hash, got %q", p.PHash)
	}
	if p.GiveBackBox != "stashdb" || p.GiveBackSceneID != "box-scene-1" {
		t.Errorf("expected GiveBack from the tracked row, got box=%q scene=%q", p.GiveBackBox, p.GiveBackSceneID)
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
	stashdb := newFakeAdultBox(t, map[string]struct{ id, title, image string }{
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

	sceneID, _, changes, err := ApplyLibraryAdult(context.Background(), sess, libStore, p, "high", nil)
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

	// Capture-at-write: Size reuses the same stat PHashFileSize already needed
	// (zero extra I/O) but is a separate column — PHashFileSize is a
	// cache-validation key deliberately allowed to go stale, Size is not.
	if want := int64(len("fake video data")); scene.Size != want {
		t.Errorf("expected Size %d (the moved file's real bytes), got %d", want, scene.Size)
	}
	// Adult records the tier in force at APPLY time, not grab time — the grab
	// path writes no scene row at all (see ApplyLibraryAdult's doc comment).
	if scene.QualityTier != "high" {
		t.Errorf("expected QualityTier %q, got %q", "high", scene.QualityTier)
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
		if _, _, _, err := ApplyLibraryAdult(context.Background(), sess, libStore, proposals.Proposal{Status: status}, "", nil); err == nil {
			t.Errorf("expected ApplyLibraryAdult to refuse a %q proposal", status)
		}
	}
}

func TestApplyLibraryAdult_RefusesProposalWithoutSceneIdentifier(t *testing.T) {
	sess := adultTestSession(t, &countingAI{}, map[string]*stashbox.Client{})
	p := proposals.Proposal{ID: 1, Status: proposals.Pending, Title: "No Identity", SourcePath: "/tmp/x.mp4", RootFolderPath: "/tmp"}
	if _, _, _, err := ApplyLibraryAdult(context.Background(), sess, newTestLibraryStore(t), p, "", nil); err == nil {
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

	_, submitted, _, err := ApplyLibraryAdult(context.Background(), sess, newTestLibraryStore(t), p, "", nil)
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
	stashdb := newFakeAdultBox(t, map[string]struct{ id, title, image string }{
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

	sceneID, _, _, err := ApplyLibraryAdult(context.Background(), sess, libStore, got[0], "", nil)
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

// TestApplyLibraryAdult_AlternatePromoteAndLose tests the C6 fold gate:
// a second file for an already-tracked scene goes through applyAdultAlternate.
// Promote branch: orphan has higher quality → it becomes the new primary.
// Lose branch: orphan has lower quality → it becomes the alternate.
func TestApplyLibraryAdult_AlternatePromoteAndLose(t *testing.T) {
	t.Run("promote: better orphan becomes primary", func(t *testing.T) {
		root := t.TempDir()
		// Primary at 1080p is already tracked.
		primaryFile := writeSceneFile(t, root, "Best Studio - Best Scene (2022-05-05) [phash-oldhash].mkv")
		libStore := newTestLibraryStore(t)
		existing, err := libStore.UpsertScene(context.Background(), library.Scene{
			Box: "stashdb", SceneID: "scene-promote-1", Title: "Best Scene",
			Studio: "Best Studio", Date: "2022-05-05",
			FilePath: primaryFile, RootFolderPath: root,
			PHash: "oldhash", QualityTier: "medium",
		})
		if err != nil {
			t.Fatalf("seeding: %v", err)
		}

		// Orphan at 4K — should win.
		orphan := writeSceneFile(t, root, "raw-4k.mkv")
		orphanProber := mapProber{
			primaryFile: {Height: 1080, CodecName: "h264", BitRate: 5_000_000},
			orphan:      {Height: 2160, CodecName: "hevc", BitRate: 40_000_000},
		}
		_ = existing

		sess := adultTestSession(t, &countingAI{}, map[string]*stashbox.Client{})
		p := proposals.Proposal{
			ID: 1, Status: proposals.Pending,
			Title: "Best Scene", Studio: "Best Studio", Date: "2022-05-05",
			GiveBackBox: "stashdb", GiveBackSceneID: "scene-promote-1",
			PHash: "newhash", DurationSeconds: 1800,
			SourcePath: orphan, RootFolderPath: root,
		}
		sceneID, _, changes, err := ApplyLibraryAdult(context.Background(), sess, libStore, p, "medium", orphanProber)
		if err != nil {
			t.Fatalf("ApplyLibraryAdult: %v", err)
		}
		if sceneID == 0 {
			t.Fatal("expected a nonzero scene id")
		}

		// The orphan should now be the primary (AdultFileName with newhash).
		newPrimary := filepath.Join(root, "Best Studio - Best Scene (2022-05-05) [phash-newhash].mkv")
		if _, statErr := os.Stat(newPrimary); statErr != nil {
			t.Errorf("expected the orphan to be the new primary at %q: %v", newPrimary, statErr)
		}
		// The old primary should now have an alternate name with quality tokens.
		found := false
		for _, ch := range changes {
			if ch.Kind == mode.Created && strings.Contains(filepath.Base(ch.Path), "alternate") {
				found = true
				break
			}
			if ch.Kind == mode.Created && strings.Contains(filepath.Base(ch.Path), "1080p") {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected the demoted primary to have a quality-token alternate name, changes: %+v", changes)
		}

		// Two file rows must exist.
		files, err := libStore.ListSceneFiles(context.Background(), sceneID)
		if err != nil {
			t.Fatalf("ListSceneFiles: %v", err)
		}
		if len(files) != 2 {
			t.Errorf("expected 2 file rows, got %d: %+v", len(files), files)
		}
		primaryCount := 0
		for _, f := range files {
			if f.IsPrimary {
				primaryCount++
			}
		}
		if primaryCount != 1 {
			t.Errorf("expected exactly 1 primary file row, got %d", primaryCount)
		}
	})

	t.Run("lose: lower-quality orphan becomes alternate", func(t *testing.T) {
		root := t.TempDir()
		// Primary at 4K is already tracked.
		primaryFile := writeSceneFile(t, root, "Best Studio - Best Scene (2022-05-05) [phash-oldhash].mkv")
		libStore := newTestLibraryStore(t)
		_, err := libStore.UpsertScene(context.Background(), library.Scene{
			Box: "stashdb", SceneID: "scene-lose-1", Title: "Best Scene",
			Studio: "Best Studio", Date: "2022-05-05",
			FilePath: primaryFile, RootFolderPath: root,
			PHash: "oldhash", QualityTier: "high",
		})
		if err != nil {
			t.Fatalf("seeding: %v", err)
		}

		// Orphan at 1080p — loses to the 4K primary.
		orphan := writeSceneFile(t, root, "raw-1080p.mkv")
		orphanProber := mapProber{
			primaryFile: {Height: 2160, CodecName: "hevc", BitRate: 40_000_000},
			orphan:      {Height: 1080, CodecName: "h264", BitRate: 8_000_000},
		}

		sess := adultTestSession(t, &countingAI{}, map[string]*stashbox.Client{})
		p := proposals.Proposal{
			ID: 2, Status: proposals.Pending,
			Title: "Best Scene", Studio: "Best Studio", Date: "2022-05-05",
			GiveBackBox: "stashdb", GiveBackSceneID: "scene-lose-1",
			PHash: "lowhash", DurationSeconds: 900,
			SourcePath: orphan, RootFolderPath: root,
		}
		sceneID, _, _, err := ApplyLibraryAdult(context.Background(), sess, libStore, p, "high", orphanProber)
		if err != nil {
			t.Fatalf("ApplyLibraryAdult: %v", err)
		}

		// Primary file must not have moved.
		if _, statErr := os.Stat(primaryFile); statErr != nil {
			t.Errorf("expected the primary file to remain at %q: %v", primaryFile, statErr)
		}

		// Two file rows: one primary, one non-primary.
		files, err := libStore.ListSceneFiles(context.Background(), sceneID)
		if err != nil {
			t.Fatalf("ListSceneFiles: %v", err)
		}
		if len(files) != 2 {
			t.Fatalf("expected 2 file rows, got %d: %+v", len(files), files)
		}
		primaryRows := 0
		for _, f := range files {
			if f.IsPrimary {
				primaryRows++
				if f.FilePath != primaryFile {
					t.Errorf("expected primary row to point at original file %q, got %q", primaryFile, f.FilePath)
				}
			}
		}
		if primaryRows != 1 {
			t.Errorf("expected exactly 1 primary row, got %d", primaryRows)
		}
	})
}

// TestClassifyAdultMatch_WebIdentifiedKeepsTitle is the B4 regression test:
// a web-identified-only result (no scene ID) must return the result's Title,
// and the reason must contain "web-identified" for tmdb_session.go's substring check.
func TestClassifyAdultMatch_WebIdentifiedKeepsTitle(t *testing.T) {
	res := &identify.MatchResult{
		Source: "web_search", Box: "", SceneID: "",
		Title: "Web Only Scene", Studio: "Hot Studio", Type: "scene",
	}
	status, reason, title, foreignID, _ := classifyAdultMatch(res, nil)

	if status != proposals.Unmatched {
		t.Errorf("expected Unmatched, got %q", status)
	}
	if title != "Web Only Scene" {
		t.Errorf("expected title %q, got %q", "Web Only Scene", title)
	}
	if foreignID != "" {
		t.Errorf("expected empty foreignID, got %q", foreignID)
	}
	if !strings.Contains(reason, "web-identified") {
		t.Errorf("expected reason to contain 'web-identified' (for tmdb_session.go:106), got %q", reason)
	}
}

// TestBuildAdultLibraryProposal_PhashMatchesReviewedLocalBecomesPendingAlternate
// proves web-identified-only identify still routes into the alternate fold when
// the file's phash is already tracked as a Review local identity.
func TestBuildAdultLibraryProposal_PhashMatchesReviewedLocalBecomesPendingAlternate(t *testing.T) {
	root := t.TempDir()
	subdir := filepath.Join(root, "scenes", "Wow Girls")
	if err := os.MkdirAll(subdir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	dupPath := writeSceneFile(t, subdir, "raw-dup.mp4")

	const phash = "reviewedLocalHash"
	libStore := newTestLibraryStore(t)
	primaryPath := writeSceneFile(t, root, "Wow Girls - Reviewed Title (2024) [phash-reviewedLocalHash].mp4")
	if _, err := libStore.UpsertScene(context.Background(), library.Scene{
		Box:            library.LocalSceneBox,
		SceneID:        library.LocalSceneID(phash),
		Title:          "Reviewed Title",
		Studio:         "Wow Girls",
		Date:           "2024",
		FilePath:       primaryPath,
		RootFolderPath: root,
		PHash:          phash,
	}); err != nil {
		t.Fatalf("seed local scene: %v", err)
	}

	entry := library.UnmappedEntry{Name: filepath.Base(dupPath), Path: dupPath}
	id := adultIdentification{
		hashed: true,
		phash:  phash,
		match: &identify.MatchResult{
			Source: "web_search", Title: "Web Only Scene", Studio: "Wow Girls", Type: "scene",
		},
	}
	p := buildAdultLibraryProposal(context.Background(), libStore, root, entry, dupPath, id)

	if p.Status != proposals.Pending {
		t.Errorf("expected Pending alternate, got %q (%s)", p.Status, p.Reason)
	}
	if !strings.Contains(p.Reason, "already in library") {
		t.Errorf("expected alternate reason, got %q", p.Reason)
	}
	if p.GiveBackBox != library.LocalSceneBox {
		t.Errorf("expected local box, got %q", p.GiveBackBox)
	}
}

// Prevent unused import errors if mediainfo is not used elsewhere in this file.
var _ *mediainfo.Probe
