package dedup

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/labbersanon/sakms/internal/dbtest"
	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/library/librarytest"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/proposals"
)

func newTestLibraryStore(t *testing.T) *library.Store {
	t.Helper()
	return librarytest.New(t)
}

func TestApplyLibrary_KeepsWinnerByDefault_DeletesOrphanLoser(t *testing.T) {
	dir := t.TempDir()
	loserPath := writeVideoFile(t, dir, "loser.mkv", 10)

	libStore := newTestLibraryStore(t)
	tracked, err := libStore.Upsert(context.Background(), library.Item{
		Mode: mode.Movies, TMDBID: 1, Title: "X", FilePath: "/winner.mkv", RootFolderPath: dir,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	p := proposals.Proposal{
		ID: 1, Status: proposals.Pending, Title: "X", TMDBID: 1,
		Candidates: []proposals.Candidate{
			{Label: "winner", Path: "/winner.mkv", TrackedID: int(tracked.ID), Winner: true},
			{Label: "loser", Path: loserPath},
		},
	}
	id, changes, err := ApplyLibrary(context.Background(), libStore, p, nil, nil, false, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != tracked.ID {
		t.Errorf("expected the already-tracked winner's id (%d), got %d", tracked.ID, id)
	}
	if _, err := os.Stat(loserPath); !os.IsNotExist(err) {
		t.Error("expected the losing orphan file to be deleted")
	}
	// The winner didn't move, so only the untracked loser's exact path
	// (c.Path, since it was never tracked) shows up in changes.
	if len(changes) != 1 || changes[0].Path != loserPath || changes[0].Kind != mode.Deleted {
		t.Errorf("expected exactly one Deleted PathChange for %q, got %+v", loserPath, changes)
	}
}

// Untracked orphans whose video was already moved/deleted (e.g. by Rename)
// must not fail Dedup Apply — same IsNotExist tolerance as tracked losers.
func TestApplyLibrary_UntrackedLoserAlreadyGone(t *testing.T) {
	dir := t.TempDir()
	missingLoser := filepath.Join(dir, "already-gone.mkv")

	libStore := newTestLibraryStore(t)
	tracked, err := libStore.Upsert(context.Background(), library.Item{
		Mode: mode.Movies, TMDBID: 1, Title: "X", FilePath: "/winner.mkv", RootFolderPath: dir,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	p := proposals.Proposal{
		ID: 1, Status: proposals.Pending, Title: "X", TMDBID: 1,
		Candidates: []proposals.Candidate{
			{Label: "winner", Path: "/winner.mkv", TrackedID: int(tracked.ID), Winner: true},
			{Label: "loser", Path: missingLoser},
		},
	}
	id, changes, err := ApplyLibrary(context.Background(), libStore, p, nil, nil, false, "")
	if err != nil {
		t.Fatalf("expected already-gone untracked loser to succeed, got: %v", err)
	}
	if id != tracked.ID {
		t.Errorf("expected winner id %d, got %d", tracked.ID, id)
	}
	if len(changes) != 1 || changes[0].Path != missingLoser || changes[0].Kind != mode.Deleted {
		t.Errorf("expected Deleted PathChange for missing path, got %+v", changes)
	}
}

func TestApplyLibrary_WinnerIsOrphan_DeletesTrackedLoserAndRegistersWinner(t *testing.T) {
	dir := t.TempDir()
	trackedFile := writeVideoFile(t, dir, "tracked.mkv", 10)
	winnerPath := writeVideoFile(t, dir, "winner.mkv", 10)

	libStore := newTestLibraryStore(t)
	tracked, err := libStore.Upsert(context.Background(), library.Item{
		Mode: mode.Movies, TMDBID: 42, Title: "Some Movie", FilePath: trackedFile, RootFolderPath: dir,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	p := proposals.Proposal{
		ID: 1, Status: proposals.Pending, Title: "Some Movie", TMDBID: 42,
		RootFolderPath: dir,
		Candidates: []proposals.Candidate{
			{Label: "tracked", Path: trackedFile, TrackedID: int(tracked.ID)},
			{Label: "winner", Path: winnerPath, Winner: true},
		},
	}
	id, changes, err := ApplyLibrary(context.Background(), libStore, p, nil, nil, false, "lossless")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id == 0 {
		t.Error("expected a nonzero library item id for the newly registered winner")
	}
	if _, err := os.Stat(trackedFile); !os.IsNotExist(err) {
		t.Error("expected the losing tracked file to be deleted")
	}
	if _, err := libStore.Get(context.Background(), tracked.ID); err != library.ErrNotFound {
		t.Errorf("expected the losing tracked library item to be deleted, got err=%v", err)
	}

	item, err := libStore.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if item.FilePath != winnerPath || item.TMDBID != 42 {
		t.Errorf("unexpected registered item: %+v", item)
	}

	// Capture-at-write on the Dedup path: a Dedup Apply swaps the tracked file
	// for a different one, so the row must record the WINNER's size (reusing
	// the fileIdentity stat, zero extra I/O) — carrying the loser's stale size
	// forward would be exactly the UI-vs-filesystem drift this project's
	// Mission declares the bar against.
	if info, err := os.Stat(winnerPath); err != nil {
		t.Fatalf("unexpected error: %v", err)
	} else if item.Size != info.Size() {
		t.Errorf("expected Size %d (the winner's real bytes), got %d", info.Size(), item.Size)
	}
	if item.QualityTier != "lossless" {
		t.Errorf("expected QualityTier %q, got %q", "lossless", item.QualityTier)
	}

	// Row 7 (player-rescan-notify plan): the removed loser's EXACT tracked
	// path (item.FilePath, resolved via removeLibraryCandidate — not c.Path)
	// is what's reported. Here they're the same value, but the assertion is
	// against trackedFile (the library item's own FilePath) specifically to
	// prove the tracked lookup path, not the proposal's own candidate path.
	// The winner never moved, so it never appears in changes.
	if len(changes) != 1 || changes[0].Path != trackedFile || changes[0].Kind != mode.Deleted {
		t.Errorf("expected exactly one Deleted PathChange for %q, got %+v", trackedFile, changes)
	}
}

// TestApplyLibrary_TrackedLoserChangeUsesLibraryItemPathNotCandidatePath
// proves the exact-path discipline row 7 (player-rescan-notify plan)
// requires: for a tracked loser, the Deleted PathChange must come from
// removeLibraryCandidate's libStore.Get lookup (item.FilePath) — the
// source of truth — not the proposal's own (possibly stale) c.Path.
func TestApplyLibrary_TrackedLoserChangeUsesLibraryItemPathNotCandidatePath(t *testing.T) {
	dir := t.TempDir()
	actualFile := writeVideoFile(t, dir, "actual.mkv", 10)
	winnerPath := writeVideoFile(t, dir, "winner.mkv", 10)

	libStore := newTestLibraryStore(t)
	tracked, err := libStore.Upsert(context.Background(), library.Item{
		Mode: mode.Movies, TMDBID: 1, Title: "X", FilePath: actualFile, RootFolderPath: dir,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	p := proposals.Proposal{
		ID: 1, Status: proposals.Pending, Title: "X", TMDBID: 1, RootFolderPath: dir,
		Candidates: []proposals.Candidate{
			// A deliberately stale candidate path, distinct from what's
			// actually recorded in libStore for this tracked item.
			{Label: "tracked", Path: filepath.Join(dir, "stale-scan-time-path.mkv"), TrackedID: int(tracked.ID)},
			{Label: "winner", Path: winnerPath, Winner: true},
		},
	}
	_, changes, err := ApplyLibrary(context.Background(), libStore, p, nil, nil, false, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(changes) != 1 || changes[0].Path != actualFile || changes[0].Kind != mode.Deleted {
		t.Errorf("expected the Deleted PathChange to be the library item's actual FilePath %q, got %+v", actualFile, changes)
	}
}

// TestApplyLibrary_TrackedLoserDBDeleteFails_StillReportsPhysicalDeletion
// proves the fix for the Phase-4 code-review finding: if os.Remove succeeds
// but the subsequent libStore.Delete (DB row removal) fails, the physically
// committed file deletion must still surface in changes — mirroring
// purge.ApplyLibrary's sibling behavior and the "capture at the point the
// os-level mutation lands" rule (Critic fix #3) used throughout this
// feature. Without the fix, removeLibraryCandidate discarded removedPath on
// this error path, silently leaving a phantom entry in any notified player.
func TestApplyLibrary_TrackedLoserDBDeleteFails_StillReportsPhysicalDeletion(t *testing.T) {
	dir := t.TempDir()
	trackedFile := writeVideoFile(t, dir, "tracked.mkv", 10)
	winnerPath := writeVideoFile(t, dir, "winner.mkv", 10)

	sqlDB := dbtest.New(t)
	libStore := library.New(sqlDB)

	tracked, err := libStore.Upsert(context.Background(), library.Item{
		Mode: mode.Movies, TMDBID: 42, Title: "Some Movie", FilePath: trackedFile, RootFolderPath: dir,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Force libStore.Delete to fail on its first statement (DELETE FROM
	// library_tags) while leaving Get/Upsert (which don't touch this table)
	// working normally — simulates a DB failure strictly after os.Remove has
	// already committed, without needing a mock Store.
	if _, err := sqlDB.Exec(`DROP TABLE library_tags`); err != nil {
		t.Fatalf("dropping library_tags to simulate a DB failure: %v", err)
	}

	p := proposals.Proposal{
		ID: 1, Status: proposals.Pending, Title: "Some Movie", TMDBID: 42,
		RootFolderPath: dir,
		Candidates: []proposals.Candidate{
			{Label: "tracked", Path: trackedFile, TrackedID: int(tracked.ID)},
			{Label: "winner", Path: winnerPath, Winner: true},
		},
	}
	_, changes, err := ApplyLibrary(context.Background(), libStore, p, nil, nil, false, "")
	if err == nil {
		t.Fatal("expected an error from the forced libStore.Delete failure")
	}
	if _, statErr := os.Stat(trackedFile); !os.IsNotExist(statErr) {
		t.Error("expected the loser file to have been physically removed despite the DB error")
	}
	if len(changes) != 1 || changes[0].Path != trackedFile || changes[0].Kind != mode.Deleted {
		t.Errorf("expected the committed physical deletion to still be reported as a Deleted PathChange for %q, got %+v", trackedFile, changes)
	}
}

func TestApplyLibrary_KeepAll_NoMutation(t *testing.T) {
	libStore := newTestLibraryStore(t)
	tracked, err := libStore.Upsert(context.Background(), library.Item{
		Mode: mode.Movies, TMDBID: 1, Title: "X", FilePath: "/a.mkv", RootFolderPath: "/x",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	p := proposals.Proposal{
		ID: 1, Status: proposals.Pending,
		Candidates: []proposals.Candidate{
			{Label: "a", Path: "/a.mkv", TrackedID: int(tracked.ID)},
			{Label: "b", Path: "/b.mkv"},
		},
	}
	id, changes, err := ApplyLibrary(context.Background(), libStore, p, nil, nil, true, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != tracked.ID {
		t.Errorf("expected keepAll to still report the existing tracked id, got %d", id)
	}
	if _, err := libStore.Get(context.Background(), tracked.ID); err != nil {
		t.Errorf("expected keepAll to leave the library item untouched, got err=%v", err)
	}
	// Edge #3 (player-rescan-notify plan): keepAll removes nothing, so it
	// must report zero PathChanges.
	if len(changes) != 0 {
		t.Errorf("expected keepAll to report zero PathChanges, got %+v", changes)
	}
}

func TestApplyLibrary_RejectsNonPendingProposal(t *testing.T) {
	libStore := newTestLibraryStore(t)
	p := proposals.Proposal{
		Status:     proposals.Applied,
		Candidates: []proposals.Candidate{{Path: "/a.mkv"}, {Path: "/b.mkv"}},
	}
	if _, _, err := ApplyLibrary(context.Background(), libStore, p, nil, nil, false, ""); err == nil {
		t.Fatal("expected ApplyLibrary to refuse an already-applied proposal")
	}
}

func TestApplyLibrary_RejectsFewerThanTwoCandidates(t *testing.T) {
	libStore := newTestLibraryStore(t)
	p := proposals.Proposal{Status: proposals.Pending, Candidates: []proposals.Candidate{{Path: "/a.mkv"}}}
	if _, _, err := ApplyLibrary(context.Background(), libStore, p, nil, nil, false, ""); err == nil {
		t.Fatal("expected ApplyLibrary to refuse a proposal with fewer than 2 candidates")
	}
}

func TestApplyLibrary_ExtraLoserDeletesOnlyTheExtra(t *testing.T) {
	dir := t.TempDir()
	primaryFile := writeVideoFile(t, dir, "primary.mkv", 10)
	extraFile := writeVideoFile(t, dir, "extra.mkv", 10)

	libStore := newTestLibraryStore(t)
	ctx := context.Background()
	tracked, err := libStore.Upsert(ctx, library.Item{
		Mode: mode.Movies, TMDBID: 89, Title: "Last Crusade", FilePath: primaryFile, RootFolderPath: dir,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := libStore.UpsertFile(ctx, library.ItemFile{
		ItemID: tracked.ID, FilePath: extraFile, IsPrimary: false,
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	p := proposals.Proposal{
		ID: 1, Status: proposals.Pending, Title: "Last Crusade", TMDBID: 89,
		Candidates: []proposals.Candidate{
			{Label: "primary", Path: primaryFile, TrackedID: int(tracked.ID), Winner: true},
			{Label: "extra", Path: extraFile, TrackedID: int(tracked.ID)},
		},
	}
	id, changes, err := ApplyLibrary(ctx, libStore, p, nil, nil, false, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != tracked.ID {
		t.Errorf("expected the title to stay tracked as %d, got %d", tracked.ID, id)
	}
	if _, err := os.Stat(primaryFile); err != nil {
		t.Errorf("expected the primary to remain, stat: %v", err)
	}
	if _, err := os.Stat(extraFile); !os.IsNotExist(err) {
		t.Error("expected the extra file to be deleted")
	}
	if _, err := libStore.Get(ctx, tracked.ID); err != nil {
		t.Errorf("expected the library item to remain, got %v", err)
	}
	files, err := libStore.ListFiles(ctx, tracked.ID)
	if err != nil {
		t.Fatalf("listing files: %v", err)
	}
	if len(files) != 1 || files[0].FilePath != primaryFile {
		t.Errorf("expected only the primary file row, got %+v", files)
	}
	if len(changes) != 1 || changes[0].Path != extraFile {
		t.Errorf("expected one Deleted PathChange for the extra, got %+v", changes)
	}
}
