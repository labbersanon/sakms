package dedup

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/labbersanon/sakms/internal/db"
	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mediainfo"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/proposals"
	"github.com/labbersanon/sakms/internal/searchterm"
	"github.com/labbersanon/sakms/internal/tmdb"
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

func fakeTMDBSearch(t *testing.T, results map[string]string) *tmdb.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		term := r.URL.Query().Get("query")
		body, ok := results[term]
		if !ok {
			t.Fatalf("unexpected search term %q", term)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return tmdb.New(tmdb.Config{BaseURL: srv.URL, APIKey: "test-key"}, srv.Client())
}

func TestScanLibrary_RequiresTMDBConfigured(t *testing.T) {
	sess := &mode.Session{Mode: mode.Movies}
	if _, err := ScanLibrary(context.Background(), sess, newTestLibraryStore(t), t.TempDir(), &fakeProber{}, &fakePHasher{}, 10); err == nil {
		t.Fatal("expected an error when TMDB isn't configured")
	}
}

func TestScanLibrary_RequiresRootFolderPath(t *testing.T) {
	sess := &mode.Session{Mode: mode.Movies, TMDB: fakeTMDBSearch(t, nil)}
	if _, err := ScanLibrary(context.Background(), sess, newTestLibraryStore(t), "", &fakeProber{}, &fakePHasher{}, 10); err == nil {
		t.Fatal("expected an error when no root folder path is configured")
	}
}

func TestScanLibrary_TrackedItemPlusOrphan_ProposesWithCorrectWinner(t *testing.T) {
	dir := t.TempDir()
	trackedDir := filepath.Join(dir, "Some Movie (2020)")
	orphanDir := filepath.Join(dir, "Some.Movie.2020.1080p.BluRay.x264-GROUP")
	trackedFile := writeVideoFile(t, trackedDir, "movie.mkv", 100)
	orphanFile := writeVideoFile(t, orphanDir, "movie.mkv", 100)

	libStore := newTestLibraryStore(t)
	tracked, err := libStore.Upsert(context.Background(), library.Item{
		Mode: mode.Movies, TMDBID: 42, Title: "Some Movie", FilePath: trackedFile, RootFolderPath: dir,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	sess := &mode.Session{Mode: mode.Movies, TMDB: fakeTMDBSearch(t, map[string]string{
		"Some Movie 2020": `{"results":[{"id":42,"title":"Some Movie"}]}`,
	})}
	prober := &fakeProber{byPath: map[string]*mediainfo.Probe{
		trackedFile: {CodecName: "h264", Width: 1280, Height: 720, BitRate: 3000},
		orphanFile:  {CodecName: "h265", Width: 1920, Height: 1080, BitRate: 8000},
	}}

	got, err := ScanLibrary(context.Background(), sess, libStore, dir, prober, matchingPHasher(trackedFile, orphanFile), 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected exactly 1 duplicate group, got %d: %+v", len(got), got)
	}
	p := got[0]
	if p.Status != proposals.Pending || p.TMDBID != 42 || len(p.Candidates) != 2 {
		t.Fatalf("unexpected proposal: %+v", p)
	}

	var winner, loser proposals.Candidate
	for _, c := range p.Candidates {
		if c.Winner {
			winner = c
		} else {
			loser = c
		}
	}
	if winner.Path != orphanFile {
		t.Errorf("expected the higher-resolution orphan to win, got winner=%+v", winner)
	}
	if loser.Path != trackedFile || loser.TrackedID != int(tracked.ID) {
		t.Errorf("expected the tracked file to be the loser, got %+v", loser)
	}
}

// TestScanLibrary_DiscoversDuplicateFileAlongsideAlreadyTrackedItem proves
// ScanRootFolder's recursion: once a movie folder has one already-tracked
// file inside it, the folder is no longer atomic — a duplicate file dropped
// in beside it (e.g. a second, higher-quality copy someone added later)
// surfaces individually and gets grouped as a duplicate, rather than being
// masked by the whole folder having previously been marked known.
func TestScanLibrary_DiscoversDuplicateFileAlongsideAlreadyTrackedItem(t *testing.T) {
	dir := t.TempDir()
	trackedDir := filepath.Join(dir, "Some Movie (2020)")
	trackedFile := writeVideoFile(t, trackedDir, "movie.mkv", 100)
	orphanFile := writeVideoFile(t, trackedDir, "Some.Movie.2020.REMUX.mkv", 100)

	libStore := newTestLibraryStore(t)
	tracked, err := libStore.Upsert(context.Background(), library.Item{
		Mode: mode.Movies, TMDBID: 42, Title: "Some Movie", FilePath: trackedFile, RootFolderPath: dir,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	term := searchterm.FromName("Some.Movie.2020.REMUX.mkv")
	sess := &mode.Session{Mode: mode.Movies, TMDB: fakeTMDBSearch(t, map[string]string{
		term: `{"results":[{"id":42,"title":"Some Movie"}]}`,
	})}
	prober := &fakeProber{byPath: map[string]*mediainfo.Probe{
		trackedFile: {CodecName: "h264", Width: 1280, Height: 720, BitRate: 3000},
		orphanFile:  {CodecName: "h265", Width: 1920, Height: 1080, BitRate: 8000},
	}}

	got, err := ScanLibrary(context.Background(), sess, libStore, dir, prober, matchingPHasher(trackedFile, orphanFile), 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || len(got[0].Candidates) != 2 {
		t.Fatalf("expected the sibling duplicate dropped alongside the tracked file to be discovered, got %+v", got)
	}
	var loser proposals.Candidate
	for _, c := range got[0].Candidates {
		if !c.Winner {
			loser = c
		}
	}
	if loser.Path != trackedFile || loser.TrackedID != int(tracked.ID) {
		t.Errorf("expected the tracked file to be the loser, got %+v", loser)
	}
}

func TestScanLibrary_SingleNewOrphanIsNotADuplicate(t *testing.T) {
	dir := t.TempDir()
	orphanDir := filepath.Join(dir, "New.Movie.2020")
	writeVideoFile(t, orphanDir, "movie.mkv", 100)

	sess := &mode.Session{Mode: mode.Movies, TMDB: fakeTMDBSearch(t, map[string]string{
		"New Movie 2020": `{"results":[{"id":99,"title":"New Movie"}]}`,
	})}

	got, err := ScanLibrary(context.Background(), sess, newTestLibraryStore(t), dir, &fakeProber{}, &fakePHasher{}, 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected no duplicate groups for a single new item, got %+v", got)
	}
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
	id, changes, err := ApplyLibrary(context.Background(), libStore, p, nil, nil, false)
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
	id, changes, err := ApplyLibrary(context.Background(), libStore, p, nil, nil, false)
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
	_, changes, err := ApplyLibrary(context.Background(), libStore, p, nil, nil, false)
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

	sqlDB, err := db.Open(filepath.Join(t.TempDir(), "sakms.db"))
	if err != nil {
		t.Fatalf("opening db: %v", err)
	}
	t.Cleanup(func() { sqlDB.Close() })
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
	_, changes, err := ApplyLibrary(context.Background(), libStore, p, nil, nil, false)
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
	id, changes, err := ApplyLibrary(context.Background(), libStore, p, nil, nil, true)
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
	if _, _, err := ApplyLibrary(context.Background(), libStore, p, nil, nil, false); err == nil {
		t.Fatal("expected ApplyLibrary to refuse an already-applied proposal")
	}
}

func TestApplyLibrary_RejectsFewerThanTwoCandidates(t *testing.T) {
	libStore := newTestLibraryStore(t)
	p := proposals.Proposal{Status: proposals.Pending, Candidates: []proposals.Candidate{{Path: "/a.mkv"}}}
	if _, _, err := ApplyLibrary(context.Background(), libStore, p, nil, nil, false); err == nil {
		t.Fatal("expected ApplyLibrary to refuse a proposal with fewer than 2 candidates")
	}
}
