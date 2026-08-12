package library

import (
	"context"
	"errors"
	"testing"
)

// sceneFileRow is the subset of a library_scene_files row these tests assert on.
// Queried directly rather than through ListSceneFiles so a bug in the lister can
// never mask a bug in the writer under test.
type sceneFileRow struct {
	FilePath    string
	IsPrimary   bool
	QualityTier string
	Size        int64
	PHash       string
}

func sceneFileRows(t *testing.T, s *Store, sceneRowID int64) []sceneFileRow {
	t.Helper()
	rows, err := s.db.QueryContext(context.Background(), `
		SELECT file_path, is_primary, quality_tier, size, phash
		FROM library_scene_files
		WHERE scene_id = ?
		ORDER BY is_primary DESC, id ASC
	`, sceneRowID)
	if err != nil {
		t.Fatalf("querying scene files: %v", err)
	}
	defer rows.Close()
	out := []sceneFileRow{}
	for rows.Next() {
		var r sceneFileRow
		if err := rows.Scan(&r.FilePath, &r.IsPrimary, &r.QualityTier, &r.Size, &r.PHash); err != nil {
			t.Fatalf("scanning scene file: %v", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterating scene files: %v", err)
	}
	return out
}

// newFilelessScene mints a scene row with no file so a test exercising
// UpsertSceneFile directly starts from an empty library_scene_files rather than
// from the primary row UpsertScene's sync hook would have minted.
func newFilelessScene(t *testing.T, s *Store, box, sceneID, title string) Scene {
	t.Helper()
	sc, err := s.UpsertScene(context.Background(), Scene{
		Box: box, SceneID: sceneID, Title: title, RootFolderPath: "/adult",
	})
	if err != nil {
		t.Fatalf("creating fileless scene %q/%q: %v", box, sceneID, err)
	}
	return sc
}

// UpsertScene via A4 must create exactly one primary file row when FilePath is set.
func TestUpsertScene_SyncsPrimarySceneFileRow(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	sc, err := s.UpsertScene(ctx, Scene{
		Box: "stashdb", SceneID: "sync-uuid-1", Title: "Sync Scene", Studio: "Studio",
		FilePath:    "/adult/Studio - Sync Scene (2024-01-02) [phash-abc].mkv",
		RootFolderPath: "/adult",
		QualityTier: "high", Size: 4096, PHash: "abc",
	})
	if err != nil {
		t.Fatalf("upserting scene: %v", err)
	}

	files := sceneFileRows(t, s, sc.ID)
	if len(files) != 1 {
		t.Fatalf("expected exactly one file row after UpsertScene, got %d: %+v", len(files), files)
	}
	want := sceneFileRow{FilePath: sc.FilePath, IsPrimary: true, QualityTier: "high", Size: 4096, PHash: "abc"}
	if files[0] != want {
		t.Fatalf("primary row mismatch:\n got %+v\nwant %+v", files[0], want)
	}
}

// A fileless scene must never mint a file row — the sc.FilePath == "" guard is
// load-bearing, not defensive tidiness.
func TestUpsertScene_FilelessSceneMintsNoFileRow(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	sc, err := s.UpsertScene(ctx, Scene{
		Box: "stashdb", SceneID: "fileless-uuid-1", Title: "No File Yet", RootFolderPath: "/adult",
	})
	if err != nil {
		t.Fatalf("upserting fileless scene: %v", err)
	}
	if files := sceneFileRows(t, s, sc.ID); len(files) != 0 {
		t.Fatalf("expected no file rows for a fileless scene, got %+v", files)
	}
}

// A repeat UpsertScene for the same scene at an unchanged path must be idempotent —
// no duplicate rows, no flap.
func TestUpsertScene_RepeatSyncIsIdempotent(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	in := Scene{
		Box: "stashdb", SceneID: "idempotent-uuid-1", Title: "Idempotent",
		FilePath: "/adult/Idempotent.mkv", RootFolderPath: "/adult", QualityTier: "high", Size: 10,
	}
	if _, err := s.UpsertScene(ctx, in); err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	sc, err := s.UpsertScene(ctx, in)
	if err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	files := sceneFileRows(t, s, sc.ID)
	if len(files) != 1 || !files[0].IsPrimary {
		t.Fatalf("expected one primary row after a repeat sync, got %+v", files)
	}
}

// A path-change upsert must re-point the primary and demote the old row (accepted
// orphan gap — the demoted row survives, exactly as for Episodes).
func TestUpsertScene_PathChangeFlipsPrimaryAndKeepsOrphan(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	const loser = "/adult/loser.mkv"
	const winner = "/adult/winner.mkv"

	if _, err := s.UpsertScene(ctx, Scene{
		Box: "stashdb", SceneID: "flip-uuid-1", Title: "Flip",
		FilePath: loser, RootFolderPath: "/adult", QualityTier: "low", Size: 100,
	}); err != nil {
		t.Fatalf("upserting first copy: %v", err)
	}
	sc, err := s.UpsertScene(ctx, Scene{
		Box: "stashdb", SceneID: "flip-uuid-1", Title: "Flip",
		FilePath: winner, RootFolderPath: "/adult", QualityTier: "high", Size: 900,
	})
	if err != nil {
		t.Fatalf("upserting winner: %v", err)
	}

	files := sceneFileRows(t, s, sc.ID)
	if len(files) != 2 {
		t.Fatalf("expected the demoted row to survive alongside the new primary, got %d: %+v", len(files), files)
	}
	if !files[0].IsPrimary || files[0].FilePath != winner {
		t.Fatalf("expected %q to be primary, got %+v", winner, files[0])
	}
	if files[1].IsPrimary || files[1].FilePath != loser {
		t.Fatalf("expected %q to survive as a non-primary orphan, got %+v", loser, files[1])
	}
}

// The same file_path must upsert once then update in place (ON CONFLICT target is
// UNIQUE(file_path)). ID equality is the discriminating assertion.
func TestUpsertSceneFile_InsertThenUpdate(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	sc := newFilelessScene(t, s, "stashdb", "upsert-uuid-1", "File Scene")

	const path = "/adult/alt.mkv"
	first, err := s.UpsertSceneFile(ctx, SceneFile{
		SceneID: sc.ID, FilePath: path, QualityTier: "low", Size: 100, VideoCodec: "h264",
	})
	if err != nil {
		t.Fatalf("inserting: %v", err)
	}
	second, err := s.UpsertSceneFile(ctx, SceneFile{
		SceneID: sc.ID, FilePath: path, QualityTier: "high", Size: 900, VideoCodec: "hevc",
	})
	if err != nil {
		t.Fatalf("updating: %v", err)
	}
	if second.ID != first.ID {
		t.Fatalf("expected the same row updated in place, got id %d then %d", first.ID, second.ID)
	}
	files := sceneFileRows(t, s, sc.ID)
	if len(files) != 1 {
		t.Fatalf("expected exactly one row after re-upsert, got %d: %+v", len(files), files)
	}
	want := sceneFileRow{FilePath: path, IsPrimary: false, QualityTier: "high", Size: 900}
	if files[0] != want {
		t.Fatalf("row mismatch:\n got %+v\nwant %+v", files[0], want)
	}
}

// A second IsPrimary write for one scene must leave exactly one primary and not
// delete the incumbent — the partial unique index must not fire.
func TestUpsertSceneFile_PrimaryDemotesSibling(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	sc := newFilelessScene(t, s, "stashdb", "demote-uuid-1", "Demote Scene")

	const first = "/adult/first.mkv"
	const second = "/adult/second.mkv"
	if _, err := s.UpsertSceneFile(ctx, SceneFile{SceneID: sc.ID, FilePath: first, IsPrimary: true}); err != nil {
		t.Fatalf("inserting first primary: %v", err)
	}
	if _, err := s.UpsertSceneFile(ctx, SceneFile{SceneID: sc.ID, FilePath: second, IsPrimary: true}); err != nil {
		t.Fatalf("inserting second primary: %v", err)
	}

	files := sceneFileRows(t, s, sc.ID)
	if len(files) != 2 {
		t.Fatalf("expected the demoted sibling to survive, got %d rows: %+v", len(files), files)
	}
	primaries := 0
	for _, f := range files {
		if f.IsPrimary {
			primaries++
		}
	}
	if primaries != 1 {
		t.Fatalf("expected exactly one primary, got %d: %+v", primaries, files)
	}
	if !files[0].IsPrimary || files[0].FilePath != second {
		t.Fatalf("expected %q to be the surviving primary, got %+v", second, files[0])
	}
	if files[1].IsPrimary || files[1].FilePath != first {
		t.Fatalf("expected %q to survive demoted, got %+v", first, files[1])
	}
}

// Unlike Episode files (UNIQUE(episode_id, file_path)), Scene files have
// UNIQUE(file_path). Two different scenes cannot share a path — the second
// upsert re-points the row to the new scene via the ON CONFLICT clause.
func TestUpsertSceneFile_SamePathRePoinitsToNewScene(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	scA := newFilelessScene(t, s, "stashdb", "repoint-uuid-1", "Scene A")
	scB := newFilelessScene(t, s, "stashdb", "repoint-uuid-2", "Scene B")

	const sharedPath = "/adult/shared.mkv"
	rowA, err := s.UpsertSceneFile(ctx, SceneFile{SceneID: scA.ID, FilePath: sharedPath})
	if err != nil {
		t.Fatalf("inserting under scene A: %v", err)
	}
	rowB, err := s.UpsertSceneFile(ctx, SceneFile{SceneID: scB.ID, FilePath: sharedPath})
	if err != nil {
		t.Fatalf("re-pointing to scene B: %v", err)
	}
	// ON CONFLICT re-points: same row id, new scene_id
	if rowA.ID != rowB.ID {
		t.Fatalf("expected the same row id after re-point, got %d then %d", rowA.ID, rowB.ID)
	}
	if rowB.SceneID != scB.ID {
		t.Fatalf("expected SceneID to be re-pointed to %d, got %d", scB.ID, rowB.SceneID)
	}
}

// Ordering: primary first, then insertion order.
func TestListSceneFiles_PrimaryFirst(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	sc := newFilelessScene(t, s, "stashdb", "order-uuid-1", "Order Scene")

	const altOne = "/adult/alt-1.mkv"
	const altTwo = "/adult/alt-2.mkv"
	const primary = "/adult/primary.mkv"
	for _, f := range []SceneFile{
		{SceneID: sc.ID, FilePath: altOne},
		{SceneID: sc.ID, FilePath: primary, IsPrimary: true},
		{SceneID: sc.ID, FilePath: altTwo},
	} {
		if _, err := s.UpsertSceneFile(ctx, f); err != nil {
			t.Fatalf("seeding %q: %v", f.FilePath, err)
		}
	}

	files, err := s.ListSceneFiles(ctx, sc.ID)
	if err != nil {
		t.Fatalf("listing: %v", err)
	}
	got := []string{}
	for _, f := range files {
		got = append(got, f.FilePath)
	}
	want := []string{primary, altOne, altTwo}
	if len(got) != len(want) {
		t.Fatalf("expected %d rows, got %v", len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("expected order %v, got %v", want, got)
		}
	}
	if !files[0].IsPrimary {
		t.Fatalf("expected the first row to be the primary, got %+v", files[0])
	}
}

// AllScenePaths returns the denormalized library_scenes.file_path AND every
// library_scene_files.file_path, deduped. The alternate-only path is included
// and the scene's denormalized path appears exactly once.
func TestAllScenePaths_UnionAndDistinct(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	const primary = "/adult/Studio - Title (2024-01-02) [phash-abc].mkv"
	const altOnly = "/adult/Studio - Title (2024-01-02) [phash-abc] - 1080p h264 8 Mbps.mkv"

	sc, err := s.UpsertScene(ctx, Scene{
		Box: "stashdb", SceneID: "allpaths-uuid-1", Title: "All Paths",
		FilePath: primary, RootFolderPath: "/adult", QualityTier: "high",
	})
	if err != nil {
		t.Fatalf("seeding scene: %v", err)
	}
	// The denormalized library_scenes.file_path row is already in library_scene_files via
	// SyncPrimarySceneFile (called from UpsertScene). Now add an alternate.
	if _, err := s.UpsertSceneFile(ctx, SceneFile{SceneID: sc.ID, FilePath: altOnly}); err != nil {
		t.Fatalf("seeding alternate: %v", err)
	}

	paths, err := s.AllScenePaths(ctx)
	if err != nil {
		t.Fatalf("listing all paths: %v", err)
	}
	seen := map[string]int{}
	for _, p := range paths {
		seen[p]++
	}
	if len(paths) != 2 {
		t.Fatalf("expected exactly 2 distinct paths, got %d: %v", len(paths), paths)
	}
	if seen[primary] != 1 {
		t.Fatalf("expected the primary path exactly once, got %d: %v", seen[primary], paths)
	}
	if seen[altOnly] != 1 {
		t.Fatalf("expected the alternate-only path to be included, got %v", paths)
	}
}

// SyncPrimarySceneFile must be a no-op for ID == 0 or FilePath == "".
func TestSyncPrimarySceneFile_NoOpGuards(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if err := s.SyncPrimarySceneFile(ctx, Scene{ID: 0, FilePath: "/adult/x.mkv"}); err != nil {
		t.Fatalf("ID==0 must be a no-op, got: %v", err)
	}
	if err := s.SyncPrimarySceneFile(ctx, Scene{ID: 999, FilePath: ""}); err != nil {
		t.Fatalf("empty FilePath must be a no-op, got: %v", err)
	}
}

// DeleteScene must cascade the library_scene_files rows (proves the FK).
func TestDeleteScene_CascadesSceneFiles(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	sc, err := s.UpsertScene(ctx, Scene{
		Box: "stashdb", SceneID: "cascade-uuid-1", Title: "Cascade",
		FilePath: "/adult/cascade.mkv", RootFolderPath: "/adult",
	})
	if err != nil {
		t.Fatalf("upserting scene: %v", err)
	}
	// Verify the file row was created
	if files := sceneFileRows(t, s, sc.ID); len(files) == 0 {
		t.Fatal("precondition: expected at least one file row before delete")
	}

	if err := s.DeleteScene(ctx, sc.ID); err != nil {
		t.Fatalf("DeleteScene: %v", err)
	}
	if files := sceneFileRows(t, s, sc.ID); len(files) != 0 {
		t.Errorf("expected library_scene_files to cascade-delete with the scene, got %+v", files)
	}
}

// UpdateScenePrimaryPath rewrites path/tier/size AND the phash triple on
// library_scenes. The intact title/studio is the whole point — it guards against
// using UpsertScene here, which would blank them if handed a partial Scene.
// It deliberately does NOT touch library_scene_files — the caller owns that.
func TestUpdateScenePrimaryPath(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	const oldPath = "/adult/old.mkv"
	const newPath = "/adult/new.mkv"
	sc, err := s.UpsertScene(ctx, Scene{
		Box: "stashdb", SceneID: "primarypath-uuid-1", Title: "Primary Path", Studio: "Studio",
		Date: "2024-01-02", FilePath: oldPath, RootFolderPath: "/adult",
		QualityTier: "low", Size: 100, PHash: "oldhash",
	})
	if err != nil {
		t.Fatalf("seeding scene: %v", err)
	}

	if err := s.UpdateScenePrimaryPath(ctx, sc.ID, newPath, "high", 900, "newhash", 900, "2026-01-02T00:00:00Z"); err != nil {
		t.Fatalf("UpdateScenePrimaryPath: %v", err)
	}

	got, err := s.GetScene(ctx, "stashdb", "primarypath-uuid-1")
	if err != nil {
		t.Fatalf("re-reading scene: %v", err)
	}
	if got.FilePath != newPath || got.QualityTier != "high" || got.Size != 900 {
		t.Fatalf("expected path/tier/size updated, got %q/%q/%d", got.FilePath, got.QualityTier, got.Size)
	}
	if got.PHash != "newhash" || got.PHashFileSize != 900 || got.PHashFileMTime != "2026-01-02T00:00:00Z" {
		t.Fatalf("expected phash triple updated, got %q/%d/%q", got.PHash, got.PHashFileSize, got.PHashFileMTime)
	}
	if got.Title != "Primary Path" || got.Studio != "Studio" || got.Date != "2024-01-02" {
		t.Fatalf("expected title/studio/date left intact, got %q/%q/%q", got.Title, got.Studio, got.Date)
	}
}

// PrimarySceneFile returns the primary row or ErrNotFound when none exists.
func TestPrimarySceneFile_FoundAndNotFound(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	sc := newFilelessScene(t, s, "stashdb", "primary-uuid-1", "Primary Test")

	if _, err := s.PrimarySceneFile(ctx, sc.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound for a scene with no files, got %v", err)
	}

	if _, err := s.UpsertSceneFile(ctx, SceneFile{SceneID: sc.ID, FilePath: "/adult/primary.mkv", IsPrimary: true}); err != nil {
		t.Fatalf("seeding primary: %v", err)
	}
	pf, err := s.PrimarySceneFile(ctx, sc.ID)
	if err != nil {
		t.Fatalf("PrimarySceneFile: %v", err)
	}
	if !pf.IsPrimary || pf.FilePath != "/adult/primary.mkv" {
		t.Fatalf("expected the primary file, got %+v", pf)
	}
}

// TestUpgradeSceneIdentity_RewritesBoxAndSceneIDPreservingID verifies the core
// guarantee: the row's id (and therefore its tags and file rows) survives the
// box/scene_id rewrite.
func TestUpgradeSceneIdentity_RewritesBoxAndSceneIDPreservingID(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	sc, err := s.UpsertScene(ctx, Scene{
		Box: LocalSceneBox, SceneID: LocalSceneID("abc123"),
		Title: "Local Name", Studio: "Local Studio", Date: "2024-01-01",
		FilePath: "/adult/local.mp4", RootFolderPath: "/adult",
	})
	if err != nil {
		t.Fatalf("seeding local scene: %v", err)
	}
	originalID := sc.ID

	if err := s.UpgradeSceneIdentity(ctx, sc.ID, "stashdb", "catalog-uuid", "Catalog Title", "Catalog Studio", "2023-06-15"); err != nil {
		t.Fatalf("UpgradeSceneIdentity: %v", err)
	}

	// Must be findable by new identity.
	got, err := s.GetScene(ctx, "stashdb", "catalog-uuid")
	if err != nil {
		t.Fatalf("GetScene after upgrade: %v", err)
	}
	if got.ID != originalID {
		t.Errorf("id changed: was %d, now %d — tags and file rows would be orphaned", originalID, got.ID)
	}
	if got.Box != "stashdb" || got.SceneID != "catalog-uuid" {
		t.Errorf("box/scene_id not updated: got %q/%q", got.Box, got.SceneID)
	}
	if got.Title != "Catalog Title" || got.Studio != "Catalog Studio" || got.Date != "2023-06-15" {
		t.Errorf("title/studio/date not updated: got %q/%q/%q", got.Title, got.Studio, got.Date)
	}

	// Old local identity must no longer exist.
	if _, getErr := s.GetScene(ctx, LocalSceneBox, LocalSceneID("abc123")); getErr == nil {
		t.Error("old local identity should not exist after upgrade")
	}
}

// TestUpgradeSceneIdentity_NonexistentIDIsNoop verifies that upgrading a
// non-existent id is not an error — matching DeleteScene's convention.
func TestUpgradeSceneIdentity_NonexistentIDIsNoop(t *testing.T) {
	s := newTestStore(t)
	if err := s.UpgradeSceneIdentity(context.Background(), 99999, "box", "sid", "T", "S", "D"); err != nil {
		t.Errorf("expected no error for non-existent id, got %v", err)
	}
}
