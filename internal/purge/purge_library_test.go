package purge

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/labbersanon/sakms/internal/db"
	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/proposals"
)

func newTestLibraryStore(t *testing.T) *library.Store {
	t.Helper()
	sqlDB, err := db.Open(filepath.Join(t.TempDir(), "sakms.db"))
	if err != nil {
		t.Fatalf("opening db: %v", err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	return library.New(sqlDB)
}

func TestScanLibrary_ProposesOnlyItemsMatchingAllowlist(t *testing.T) {
	libStore := newTestLibraryStore(t)
	ctx := context.Background()

	vanilla, err := libStore.Upsert(ctx, library.Item{Mode: mode.Movies, TMDBID: 1, Title: "Vanilla Movie", RootFolderPath: "/media/Movies"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := libStore.AddTag(ctx, vanilla.ID, "family-friendly"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	flagged, err := libStore.Upsert(ctx, library.Item{Mode: mode.Movies, TMDBID: 2, Title: "Flagged Movie", RootFolderPath: "/media/Movies"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := libStore.AddTag(ctx, flagged.ID, "BDSM"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := libStore.AddTag(ctx, flagged.ID, "unrelated"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := ScanLibrary(ctx, libStore, []string{"BDSM"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected exactly 1 matched proposal, got %d: %+v", len(got), got)
	}
	p := got[0]
	if p.TrackedID != int(flagged.ID) || p.Title != "Flagged Movie" || p.Status != proposals.Pending {
		t.Errorf("unexpected proposal: %+v", p)
	}
	if p.Reason == "" {
		t.Error("expected a populated reason naming the matched tag")
	}
}

func TestScanLibrary_EmptyAllowlistMatchesNothing(t *testing.T) {
	libStore := newTestLibraryStore(t)
	ctx := context.Background()
	item, err := libStore.Upsert(ctx, library.Item{Mode: mode.Movies, TMDBID: 1, Title: "X", RootFolderPath: "/media/Movies"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := libStore.AddTag(ctx, item.ID, "BDSM"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := ScanLibrary(ctx, libStore, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected no proposals against an empty allowlist, got %+v", got)
	}
}

func TestApplyLibrary_DeletesFileAndLibraryItem(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "movie.mkv")
	if err := os.WriteFile(filePath, []byte("data"), 0o644); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	libStore := newTestLibraryStore(t)
	ctx := context.Background()
	item, err := libStore.Upsert(ctx, library.Item{Mode: mode.Movies, TMDBID: 1, Title: "Flagged Movie", FilePath: filePath, RootFolderPath: dir})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	changes, err := ApplyLibrary(ctx, libStore, proposals.Proposal{
		ID: 1, Status: proposals.Pending, Title: "Flagged Movie", TrackedID: int(item.ID),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, err := os.Stat(filePath); !os.IsNotExist(err) {
		t.Errorf("expected the file to be deleted, stat returned: %v", err)
	}
	if _, err := libStore.Get(ctx, item.ID); err != library.ErrNotFound {
		t.Errorf("expected the library item to be deleted, got err=%v", err)
	}
	if len(changes) != 1 || changes[0].Path != filePath || changes[0].Kind != mode.Deleted {
		t.Errorf("expected exactly one Deleted PathChange for %q, got %+v", filePath, changes)
	}
}

func TestApplyLibrary_RejectsNonPendingProposal(t *testing.T) {
	libStore := newTestLibraryStore(t)
	for _, status := range []proposals.Status{proposals.Applied, proposals.Dismissed, proposals.Unmatched} {
		_, err := ApplyLibrary(context.Background(), libStore, proposals.Proposal{Status: status, TrackedID: 5})
		if err == nil {
			t.Errorf("expected ApplyLibrary to refuse a %q proposal", status)
		}
	}
}

func TestApplyLibrary_RejectsMissingTrackedID(t *testing.T) {
	libStore := newTestLibraryStore(t)
	_, err := ApplyLibrary(context.Background(), libStore, proposals.Proposal{Status: proposals.Pending, TrackedID: 0})
	if err == nil {
		t.Fatal("expected ApplyLibrary to refuse a proposal with no tracked id")
	}
}
