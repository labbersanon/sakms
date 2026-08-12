package rename

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/labbersanon/sakms/internal/identify"
	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/proposals"
	"github.com/labbersanon/sakms/internal/stashbox"
	"github.com/labbersanon/sakms/internal/throttle"
)

// adultTestSessionWithBoxes builds a minimal mode.Session wired for fingerprint
// lookups against the given stash-box clients. Used by the Review and Upgrade
// tests that need LookupFingerprints to work (GiveBack must be set).
func adultTestSessionWithBoxes(boxes map[string]*stashbox.Client) *mode.Session {
	return &mode.Session{
		Mode: mode.Adult,
		Identify: &identify.Identifier{
			GiveBack: identify.NewGiveBack(boxes),
			Boxes:    identify.NewBoxSearcher(boxes, nil),
			Throttle: throttle.New(0),
		},
	}
}

// webIdentifiedProposal returns an Unmatched Rename Adult proposal with a
// web-identified title but no catalog scene id — the Review-eligible shape.
func webIdentifiedProposal(videoPath, root string) proposals.Proposal {
	return proposals.Proposal{
		ID:             1,
		Status:         proposals.Unmatched,
		Workflow:       proposals.Rename,
		Mode:           mode.Adult,
		SourceName:     filepath.Base(videoPath),
		SourcePath:     videoPath,
		RootFolderPath: root,
		Title:          "Web Identified Scene",
		Studio:         "Studio X",
		Date:           "2024-01-15",
	}
}

// TestBuildAdultReview_ProposesNameFromWebIdentifiedTitle checks that the
// preview uses the web-identified title/studio/date when the recheck finds
// nothing new.
func TestBuildAdultReview_ProposesNameFromWebIdentifiedTitle(t *testing.T) {
	root := t.TempDir()
	videoPath := writeSceneFile(t, root, "raw-download.mp4")

	hasher := &fakeHasher{hashes: map[string]string{videoPath: "abc123"}}
	prober := &fakeProber{}
	// No catalog box configured → recheck returns nothing.
	sess := &mode.Session{Mode: mode.Adult}

	p := webIdentifiedProposal(videoPath, root)
	p.PHash = "abc123"

	preview, err := BuildAdultReview(context.Background(), sess, nil, hasher, prober, p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if preview.PHash != "abc123" {
		t.Errorf("expected phash abc123, got %q", preview.PHash)
	}
	if !strings.Contains(preview.ProposedName, "Studio X") {
		t.Errorf("proposed name should contain studio name, got %q", preview.ProposedName)
	}
	if !strings.Contains(preview.ProposedName, "2024-01-15") {
		t.Errorf("proposed name should contain date, got %q", preview.ProposedName)
	}
	if preview.CatalogBox != "" || preview.CatalogSceneID != "" {
		t.Errorf("no catalog hit expected, got box=%q scene=%q", preview.CatalogBox, preview.CatalogSceneID)
	}
}

// TestBuildAdultReview_RecomputesPhashWhenAbsent verifies that BuildAdultReview
// hashes the file on demand when p.PHash is empty.
func TestBuildAdultReview_RecomputesPhashWhenAbsent(t *testing.T) {
	root := t.TempDir()
	videoPath := writeSceneFile(t, root, "raw.mp4")

	hasher := &fakeHasher{hashes: map[string]string{videoPath: "freshHash"}}
	prober := &fakeProber{}
	sess := &mode.Session{Mode: mode.Adult}

	p := webIdentifiedProposal(videoPath, root)
	// PHash intentionally empty.

	preview, err := BuildAdultReview(context.Background(), sess, nil, hasher, prober, p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if preview.PHash != "freshHash" {
		t.Errorf("expected on-demand hash to be used, got %q", preview.PHash)
	}
}

// TestBuildAdultReview_HasherFailureIsSoft verifies that a hash failure
// populates RecheckError but does NOT return an error — the local path must
// remain available (the popup shows the warning).
func TestBuildAdultReview_HasherFailureIsSoft(t *testing.T) {
	root := t.TempDir()
	videoPath := writeSceneFile(t, root, "raw.mp4")

	hasher := &fakeHasher{errs: map[string]bool{videoPath: true}}
	prober := &fakeProber{}
	sess := &mode.Session{Mode: mode.Adult}

	p := webIdentifiedProposal(videoPath, root)

	preview, err := BuildAdultReview(context.Background(), sess, nil, hasher, prober, p)
	if err != nil {
		t.Fatalf("hasher failure must NOT be returned as error, got %v", err)
	}
	if preview.RecheckError == "" {
		t.Error("expected RecheckError to be set on hasher failure")
	}
	if preview.PHash != "" {
		t.Errorf("expected empty PHash on hasher failure, got %q", preview.PHash)
	}
}

// TestBuildAdultReview_CatalogHitPopulatesCatalogFields verifies that when the
// fingerprint lookup returns a match, the Catalog* fields are populated and the
// proposed name uses catalog values.
func TestBuildAdultReview_CatalogHitPopulatesCatalogFields(t *testing.T) {
	root := t.TempDir()
	videoPath := writeSceneFile(t, root, "raw.mp4")

	const phash = "catalogHash"
	hasher := &fakeHasher{hashes: map[string]string{videoPath: phash}}
	prober := &fakeProber{}

	stashdb := newFakeAdultBox(t, map[string]struct{ id, title string }{
		phash: {id: "scene-999", title: "Catalog Scene Title"},
	}, nil, nil)
	sess := adultTestSessionWithBoxes(map[string]*stashbox.Client{"stashdb": stashdb})

	p := webIdentifiedProposal(videoPath, root)
	p.PHash = phash

	preview, err := BuildAdultReview(context.Background(), sess, nil, hasher, prober, p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if preview.CatalogBox != "stashdb" || preview.CatalogSceneID != "scene-999" {
		t.Errorf("expected catalog hit stashdb/scene-999, got box=%q scene=%q", preview.CatalogBox, preview.CatalogSceneID)
	}
	if preview.CatalogTitle != "Catalog Scene Title" {
		t.Errorf("expected catalog title, got %q", preview.CatalogTitle)
	}
	// Proposed name should use catalog values, not the web-identified ones.
	if !strings.Contains(preview.ProposedName, "Catalog Scene Title") {
		t.Errorf("proposed name should use catalog title when available, got %q", preview.ProposedName)
	}
}

// TestBuildAdultReview_EligibilityGuards checks that non-Review-eligible
// proposals are rejected.
func TestBuildAdultReview_EligibilityGuards(t *testing.T) {
	root := t.TempDir()
	videoPath := writeSceneFile(t, root, "raw.mp4")
	sess := &mode.Session{Mode: mode.Adult}

	cases := []struct {
		name string
		p    proposals.Proposal
	}{
		{name: "wrong workflow", p: proposals.Proposal{Workflow: proposals.Purge, Mode: mode.Adult, Status: proposals.Unmatched, SourcePath: videoPath, RootFolderPath: root}},
		{name: "wrong mode", p: proposals.Proposal{Workflow: proposals.Rename, Mode: mode.Movies, Status: proposals.Unmatched, SourcePath: videoPath, RootFolderPath: root}},
		{name: "not unmatched", p: proposals.Proposal{Workflow: proposals.Rename, Mode: mode.Adult, Status: proposals.Pending, SourcePath: videoPath, RootFolderPath: root}},
		{name: "has catalog scene id", p: proposals.Proposal{Workflow: proposals.Rename, Mode: mode.Adult, Status: proposals.Unmatched, GiveBackSceneID: "u1", SourcePath: videoPath, RootFolderPath: root}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := BuildAdultReview(context.Background(), sess, nil, &fakeHasher{}, &fakeProber{}, tc.p)
			if err == nil {
				t.Fatal("expected an error for ineligible proposal")
			}
		})
	}
}

// TestConfirmAdultReviewLocal_RenamesFileAndCreatesLocalRow verifies the
// happy path: file is renamed under RootFolderPath, a local library_scenes row
// is created with box="local" / scene_id="phash:<hash>", and the primary
// library_scene_files row exists.
func TestConfirmAdultReviewLocal_RenamesFileAndCreatesLocalRow(t *testing.T) {
	root := t.TempDir()
	videoPath := writeSceneFile(t, root, "raw-download.mp4")

	const phash = "deadbeef"
	hasher := &fakeHasher{hashes: map[string]string{videoPath: phash}}
	prober := &fakeProber{}
	libStore := newTestLibraryStore(t)

	p := webIdentifiedProposal(videoPath, root)
	p.PHash = phash

	sceneID, changes, err := ConfirmAdultReviewLocal(context.Background(), libStore, hasher, prober, p, "Web Identified Scene (Studio X) (2024-01-15) [phash-deadbeef].mp4", "1080p")
	if err != nil {
		t.Fatalf("ConfirmAdultReviewLocal failed: %v", err)
	}
	if sceneID == 0 {
		t.Error("expected a non-zero scene id")
	}
	if len(changes) == 0 {
		t.Error("expected at least one file move to be reported")
	}

	// DB row must exist with local identity.
	sc, err := libStore.GetScene(context.Background(), library.LocalSceneBox, library.LocalSceneID(phash))
	if err != nil {
		t.Fatalf("GetScene(local, phash:%s): %v", phash, err)
	}
	if sc.Box != library.LocalSceneBox {
		t.Errorf("expected box %q, got %q", library.LocalSceneBox, sc.Box)
	}
	if sc.SceneID != library.LocalSceneID(phash) {
		t.Errorf("expected scene_id %q, got %q", library.LocalSceneID(phash), sc.SceneID)
	}
	// Primary file row must exist.
	files, _ := libStore.ListSceneFiles(context.Background(), sc.ID)
	var hasPrimary bool
	for _, f := range files {
		if f.IsPrimary {
			hasPrimary = true
		}
	}
	if !hasPrimary {
		t.Error("expected a primary library_scene_files row after local confirm")
	}
}

// TestConfirmAdultReviewLocal_RejectsEmptyPhash verifies that a proposal with
// no computable phash is rejected — a local identity without a phash is invalid.
func TestConfirmAdultReviewLocal_RejectsEmptyPhash(t *testing.T) {
	root := t.TempDir()
	videoPath := writeSceneFile(t, root, "raw.mp4")

	// Hasher always fails.
	hasher := &fakeHasher{errs: map[string]bool{videoPath: true}}
	prober := &fakeProber{}
	libStore := newTestLibraryStore(t)

	p := webIdentifiedProposal(videoPath, root)
	// PHash empty and hasher will fail.

	_, _, err := ConfirmAdultReviewLocal(context.Background(), libStore, hasher, prober, p, "output.mp4", "")
	if err == nil {
		t.Fatal("expected an error when no phash can be computed")
	}
	if !strings.Contains(err.Error(), "no perceptual hash") {
		t.Errorf("unexpected error message: %v", err)
	}
}

// TestConfirmAdultReviewLocal_PathTraversalIsBlocked verifies that a malicious
// fileName containing "../../" lands under RootFolderPath, not outside it.
func TestConfirmAdultReviewLocal_PathTraversalIsBlocked(t *testing.T) {
	root := t.TempDir()
	videoPath := writeSceneFile(t, root, "raw.mp4")

	const phash = "traversalHash"
	hasher := &fakeHasher{hashes: map[string]string{videoPath: phash}}
	prober := &fakeProber{}
	libStore := newTestLibraryStore(t)

	p := webIdentifiedProposal(videoPath, root)
	p.PHash = phash

	sceneID, _, err := ConfirmAdultReviewLocal(context.Background(), libStore, hasher, prober, p, "../../escape.mp4", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	sc, gerr := libStore.GetScene(context.Background(), library.LocalSceneBox, library.LocalSceneID(phash))
	if gerr != nil {
		t.Fatalf("GetScene: %v", gerr)
	}
	if !strings.HasPrefix(sc.FilePath, root) {
		t.Errorf("file landed outside RootFolderPath: got %q, expected prefix %q", sc.FilePath, root)
	}
	_ = sceneID
}

// TestConfirmAdultReviewLocal_ExtensionChangedIsRejected verifies that changing
// the extension (e.g. .mkv → .mp4) is rejected — it does not transcode.
func TestConfirmAdultReviewLocal_ExtensionChangedIsRejected(t *testing.T) {
	root := t.TempDir()
	videoPath := writeSceneFile(t, root, "raw.mkv")

	const phash = "extHash"
	hasher := &fakeHasher{hashes: map[string]string{videoPath: phash}}
	prober := &fakeProber{}
	libStore := newTestLibraryStore(t)

	p := webIdentifiedProposal(videoPath, root)
	p.PHash = phash

	_, _, err := ConfirmAdultReviewLocal(context.Background(), libStore, hasher, prober, p, "output.mp4", "")
	if err == nil {
		t.Fatal("expected an error when the extension is changed")
	}
	if !strings.Contains(err.Error(), "extension mismatch") {
		t.Errorf("unexpected error: %v", err)
	}
}

// TestConfirmAdultReviewLocal_MissingExtensionFilledFromSource verifies that a
// fileName with no extension gets the source extension appended.
func TestConfirmAdultReviewLocal_MissingExtensionFilledFromSource(t *testing.T) {
	root := t.TempDir()
	videoPath := writeSceneFile(t, root, "raw.mp4")

	const phash = "extFillHash"
	hasher := &fakeHasher{hashes: map[string]string{videoPath: phash}}
	prober := &fakeProber{}
	libStore := newTestLibraryStore(t)

	p := webIdentifiedProposal(videoPath, root)
	p.PHash = phash

	_, _, err := ConfirmAdultReviewLocal(context.Background(), libStore, hasher, prober, p, "output", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	sc, _ := libStore.GetScene(context.Background(), library.LocalSceneBox, library.LocalSceneID(phash))
	if !strings.HasSuffix(sc.FilePath, ".mp4") {
		t.Errorf("expected .mp4 extension to be filled in from source, got %q", sc.FilePath)
	}
}

// TestConfirmAdultReviewLocal_DuplicatePhashRoutesToAlternate verifies the
// duplicate-local guard: a second live file with the same phash folds via
// applyAdultAlternate instead of UpsertScene ON CONFLICT re-pointing file_path.
func TestConfirmAdultReviewLocal_DuplicatePhashRoutesToAlternate(t *testing.T) {
	root := t.TempDir()
	primaryFile := writeSceneFile(t, root, "Studio X - Web Identified Scene (2024-01-15) [phash-dupHash].mp4")
	const phash = "dupHash"
	libStore := newTestLibraryStore(t)
	existing, err := libStore.UpsertScene(context.Background(), library.Scene{
		Box: library.LocalSceneBox, SceneID: library.LocalSceneID(phash),
		Title: "Web Identified Scene", Studio: "Studio X", Date: "2024-01-15",
		FilePath: primaryFile, RootFolderPath: root,
		PHash: phash, QualityTier: "high",
	})
	if err != nil {
		t.Fatalf("seeding local primary: %v", err)
	}

	orphan := writeSceneFile(t, root, "raw-dup.mp4")
	hasher := &fakeHasher{hashes: map[string]string{orphan: phash}}
	prober := mapProber{
		primaryFile: {Height: 2160, CodecName: "hevc", BitRate: 40_000_000},
		orphan:      {Height: 1080, CodecName: "h264", BitRate: 8_000_000},
	}

	p := webIdentifiedProposal(orphan, root)
	p.PHash = phash

	sceneID, changes, cerr := ConfirmAdultReviewLocal(
		context.Background(), libStore, hasher, prober, p, "ignored-by-alternate.mp4", "high")
	if cerr != nil {
		t.Fatalf("ConfirmAdultReviewLocal: %v", cerr)
	}
	if sceneID != existing.ID {
		t.Errorf("expected fold onto existing scene %d, got %d", existing.ID, sceneID)
	}
	if len(changes) == 0 {
		t.Error("expected at least one path change from the alternate fold")
	}

	sc, gerr := libStore.GetScene(context.Background(), library.LocalSceneBox, library.LocalSceneID(phash))
	if gerr != nil {
		t.Fatalf("GetScene: %v", gerr)
	}
	if sc.FilePath != primaryFile {
		t.Errorf("primary path changed to %q — lose branch must keep the live 4K primary at %q", sc.FilePath, primaryFile)
	}
	files, lerr := libStore.ListSceneFiles(context.Background(), sc.ID)
	if lerr != nil {
		t.Fatalf("ListSceneFiles: %v", lerr)
	}
	if len(files) != 2 {
		t.Fatalf("expected 2 library_scene_files rows after alternate fold, got %d: %+v", len(files), files)
	}
	// Orphan should still exist somewhere under root (alternate name).
	if _, statErr := os.Stat(orphan); !os.IsNotExist(statErr) {
		t.Errorf("orphan source path %q should have been moved; stat err=%v", orphan, statErr)
	}
}

// TestConfirmAdultReviewLocal_StalePrimaryAllowsNewLocal verifies fileExists on
// the duplicate gate: a missing primary path is not an occupied slot, so Confirm
// creates/reclaims the local identity instead of calling applyAdultAlternate.
func TestConfirmAdultReviewLocal_StalePrimaryAllowsNewLocal(t *testing.T) {
	root := t.TempDir()
	gonePrimary := filepath.Join(root, "gone-primary.mp4")
	const phash = "staleHash"
	libStore := newTestLibraryStore(t)
	_, err := libStore.UpsertScene(context.Background(), library.Scene{
		Box: library.LocalSceneBox, SceneID: library.LocalSceneID(phash),
		Title: "Gone", Studio: "Studio X", Date: "2024-01-15",
		FilePath: gonePrimary, RootFolderPath: root,
		PHash: phash, QualityTier: "high",
	})
	if err != nil {
		t.Fatalf("seeding stale local row: %v", err)
	}

	orphan := writeSceneFile(t, root, "raw-stale.mp4")
	hasher := &fakeHasher{hashes: map[string]string{orphan: phash}}
	p := webIdentifiedProposal(orphan, root)
	p.PHash = phash

	_, _, cerr := ConfirmAdultReviewLocal(
		context.Background(), libStore, hasher, &fakeProber{}, p,
		"Reclaimed (Studio X) (2024-01-15) [phash-staleHash].mp4", "1080p")
	if cerr != nil {
		t.Fatalf("ConfirmAdultReviewLocal: %v", cerr)
	}
	sc, gerr := libStore.GetScene(context.Background(), library.LocalSceneBox, library.LocalSceneID(phash))
	if gerr != nil {
		t.Fatalf("GetScene: %v", gerr)
	}
	if sc.FilePath == gonePrimary {
		t.Error("stale primary path was kept — Confirm should have reclaimed FilePath onto the live file")
	}
	if _, statErr := os.Stat(sc.FilePath); statErr != nil {
		t.Errorf("new primary %q missing: %v", sc.FilePath, statErr)
	}
}

// TestUpgradeLocalAdultScenes_UpgradesLocalSceneToCatalogIdentity verifies
// that a local scene whose phash resolves to a catalog scene is upgraded in
// place: same library_scenes.id, new (box, scene_id), file renamed.
func TestUpgradeLocalAdultScenes_UpgradesLocalSceneToCatalogIdentity(t *testing.T) {
	root := t.TempDir()
	videoPath := writeSceneFile(t, root, "Web Scene (Studio) (2024) [phash-localH].mp4")

	const phash = "localH"
	libStore := newTestLibraryStore(t)

	// Insert a local scene row.
	sc, err := libStore.UpsertScene(context.Background(), library.Scene{
		Box: library.LocalSceneBox, SceneID: library.LocalSceneID(phash),
		Title: "Local Name", Studio: "Local Studio", Date: "2024-01-01",
		FilePath: videoPath, RootFolderPath: root, PHash: phash,
	})
	if err != nil {
		t.Fatalf("inserting local scene: %v", err)
	}
	originalID := sc.ID

	// Wire a catalog box that returns a hit for this phash.
	stashdb := newFakeAdultBox(t, map[string]struct{ id, title string }{
		phash: {id: "catalog-001", title: "Catalog Title"},
	}, nil, nil)
	sess := adultTestSessionWithBoxes(map[string]*stashbox.Client{"stashdb": stashdb})

	changes, err := UpgradeLocalAdultScenes(context.Background(), sess, libStore, &fakeProber{})
	if err != nil {
		t.Fatalf("UpgradeLocalAdultScenes: %v", err)
	}
	if len(changes) == 0 {
		t.Error("expected at least one path change to be reported")
	}

	// Row id must be unchanged; box/scene_id must be updated.
	upgraded, getErr := libStore.GetScene(context.Background(), "stashdb", "catalog-001")
	if getErr != nil {
		t.Fatalf("GetScene after upgrade: %v", getErr)
	}
	if upgraded.ID != originalID {
		t.Errorf("library_scenes.id changed: was %d, now %d — tags and file rows would be orphaned", originalID, upgraded.ID)
	}
	if upgraded.Title != "Catalog Title" {
		t.Errorf("title not updated to catalog value: got %q", upgraded.Title)
	}

	// File must have been renamed to a catalog-based name (no longer the local path).
	if _, statErr := os.Stat(videoPath); statErr == nil {
		t.Error("original local file path still exists after upgrade — expected it to be renamed")
	}
}

// TestUpgradeLocalAdultScenes_SkipsWhenCatalogAlreadyTracked verifies that
// an upgrade whose target catalog identity is already tracked is skipped safely
// (not merged, not panicked).
func TestUpgradeLocalAdultScenes_SkipsWhenCatalogAlreadyTracked(t *testing.T) {
	root := t.TempDir()
	videoPath := writeSceneFile(t, root, "local-scene.mp4")

	const phash = "collide"
	libStore := newTestLibraryStore(t)

	// Local row.
	if _, err := libStore.UpsertScene(context.Background(), library.Scene{
		Box: library.LocalSceneBox, SceneID: library.LocalSceneID(phash),
		Title: "Local", FilePath: videoPath, RootFolderPath: root, PHash: phash,
	}); err != nil {
		t.Fatalf("inserting local scene: %v", err)
	}
	// Catalog row already exists.
	if _, err := libStore.UpsertScene(context.Background(), library.Scene{
		Box: "stashdb", SceneID: "catalog-001",
		Title: "Already Tracked", FilePath: videoPath + ".extra", RootFolderPath: root,
	}); err != nil {
		t.Fatalf("inserting catalog scene: %v", err)
	}

	stashdb := newFakeAdultBox(t, map[string]struct{ id, title string }{
		phash: {id: "catalog-001", title: "Already Tracked"},
	}, nil, nil)
	sess := adultTestSessionWithBoxes(map[string]*stashbox.Client{"stashdb": stashdb})

	changes, err := UpgradeLocalAdultScenes(context.Background(), sess, libStore, &fakeProber{})
	if err != nil {
		t.Fatalf("UpgradeLocalAdultScenes: %v", err)
	}
	if len(changes) != 0 {
		t.Errorf("expected no changes when catalog already tracked, got %d", len(changes))
	}
	// Local row must still exist.
	if _, getErr := libStore.GetScene(context.Background(), library.LocalSceneBox, library.LocalSceneID(phash)); getErr != nil {
		t.Errorf("local scene should still exist after skip: %v", getErr)
	}
}

// TestUpgradeLocalAdultScenes_NoLocalScenesIsNoop verifies that when there are
// no local scenes, the function is fast and makes zero fingerprint calls.
func TestUpgradeLocalAdultScenes_NoLocalScenesIsNoop(t *testing.T) {
	libStore := newTestLibraryStore(t)
	// Wire a nil identify so any attempt to call LookupFingerprints panics.
	sess := &mode.Session{Mode: mode.Adult, Identify: nil}

	changes, err := UpgradeLocalAdultScenes(context.Background(), sess, libStore, nil)
	if err != nil {
		t.Fatalf("UpgradeLocalAdultScenes on empty library: %v", err)
	}
	if len(changes) != 0 {
		t.Errorf("expected no changes on empty library, got %d", len(changes))
	}
}
