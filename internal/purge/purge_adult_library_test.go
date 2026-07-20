package purge

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/proposals"
)

func TestScanLibraryAdult_ProposesOnlyScenesMatchingAllowlist(t *testing.T) {
	libStore := newTestLibraryStore(t)
	ctx := context.Background()

	vanilla, err := libStore.UpsertScene(ctx, library.Scene{Box: "stashdb", SceneID: "s-1", Title: "Vanilla Scene", RootFolderPath: "/media/Adult"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := libStore.AddSceneTag(ctx, vanilla.ID, "romance"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	flagged, err := libStore.UpsertScene(ctx, library.Scene{Box: "stashdb", SceneID: "s-2", Title: "Flagged Scene", RootFolderPath: "/media/Adult"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := libStore.AddSceneTag(ctx, flagged.ID, "BDSM"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := libStore.AddSceneTag(ctx, flagged.ID, "unrelated"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := ScanLibraryAdult(ctx, libStore, []string{"BDSM"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected exactly 1 matched proposal, got %d: %+v", len(got), got)
	}
	p := got[0]
	if p.TrackedID != int(flagged.ID) || p.Title != "Flagged Scene" || p.Mode != mode.Adult || p.Status != proposals.Pending {
		t.Errorf("unexpected proposal: %+v", p)
	}
	if p.Reason == "" {
		t.Error("expected a populated reason naming the matched tag")
	}
}

func TestScanLibraryAdult_EmptyAllowlistMatchesNothing(t *testing.T) {
	libStore := newTestLibraryStore(t)
	ctx := context.Background()
	scene, err := libStore.UpsertScene(ctx, library.Scene{Box: "stashdb", SceneID: "s-1", Title: "X", RootFolderPath: "/media/Adult"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := libStore.AddSceneTag(ctx, scene.ID, "BDSM"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := ScanLibraryAdult(ctx, libStore, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected no proposals against an empty allowlist, got %+v", got)
	}
}

func TestApplyLibraryAdult_DeletesFileAndScene(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "scene.mkv")
	if err := os.WriteFile(filePath, []byte("data"), 0o644); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	libStore := newTestLibraryStore(t)
	ctx := context.Background()
	scene, err := libStore.UpsertScene(ctx, library.Scene{Box: "stashdb", SceneID: "s-1", Title: "Flagged Scene", FilePath: filePath, RootFolderPath: dir})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// SourcePath is load-bearing here: ApplyLibraryAdult removes the file it
	// names (there's no GetScene-by-id to re-derive the path), so the
	// hand-built proposal must carry it exactly as ScanLibraryAdult would.
	changes, err := ApplyLibraryAdult(ctx, libStore, proposals.Proposal{
		ID: 1, Status: proposals.Pending, Title: "Flagged Scene",
		SourcePath: filePath, TrackedID: int(scene.ID),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, err := os.Stat(filePath); !os.IsNotExist(err) {
		t.Errorf("expected the file to be deleted, stat returned: %v", err)
	}
	if _, err := libStore.GetScene(ctx, "stashdb", "s-1"); err != library.ErrNotFound {
		t.Errorf("expected the scene row to be deleted, got err=%v", err)
	}
	if len(changes) != 1 || changes[0].Path != filePath || changes[0].Kind != mode.Deleted {
		t.Errorf("expected exactly one Deleted PathChange for %q, got %+v", filePath, changes)
	}
}

func TestApplyLibraryAdult_RejectsNonPendingProposal(t *testing.T) {
	libStore := newTestLibraryStore(t)
	for _, status := range []proposals.Status{proposals.Applied, proposals.Dismissed, proposals.Unmatched} {
		_, err := ApplyLibraryAdult(context.Background(), libStore, proposals.Proposal{Status: status, TrackedID: 5})
		if err == nil {
			t.Errorf("expected ApplyLibraryAdult to refuse a %q proposal", status)
		}
	}
}

func TestApplyLibraryAdult_RejectsMissingTrackedID(t *testing.T) {
	libStore := newTestLibraryStore(t)
	_, err := ApplyLibraryAdult(context.Background(), libStore, proposals.Proposal{Status: proposals.Pending, TrackedID: 0})
	if err == nil {
		t.Fatal("expected ApplyLibraryAdult to refuse a proposal with no scene id")
	}
}
