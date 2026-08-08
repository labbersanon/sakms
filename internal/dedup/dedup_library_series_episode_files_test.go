package dedup

import (
	"context"
	"database/sql"
	"os"
	"testing"

	"github.com/labbersanon/sakms/internal/dbtest"
	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/proposals"
)

// TestApplyLibrarySeries_SyncsPrimaryEpisodeFileRow proves the claim
// ApplyLibrarySeries makes no call of its own to support: because
// library.Store.SyncPrimaryEpisodeFile is hooked INSIDE UpsertEpisode, this
// Apply path keeps library_episode_files' primary entry naming the surviving
// winner with no second call site here. Without the hook, the row would still
// flag the DELETED loser as primary — silently, no error, no log line.
//
// It also pins the accepted §11.11 gap: the deleted loser's row SURVIVES as a
// non-primary orphan. That is deliberate, not a bug to fix here.
//
// Satisfies plan §9.1's TestSeriesDedupApply_WithPrimaryFileSyncHook — all
// three of its assertions: (1) Dedup Apply still SUCCEEDS with the hook wired
// in (the actual D1 regression risk), (2) library_episodes.file_path names the
// surviving winner, unchanged from before the hook existed, and (3) exactly one
// primary library_episode_files row, naming the winner and not the deleted
// loser.
func TestApplyLibrarySeries_SyncsPrimaryEpisodeFileRow(t *testing.T) {
	dir := t.TempDir()
	trackedFile := writeVideoFile(t, dir, "tracked.mkv", 10)
	winnerPath := writeVideoFile(t, dir, "winner.mkv", 10)

	// Unlike newTestLibraryStore, this keeps the *sql.DB so the test can read
	// library_episode_files directly rather than through library.ListEpisodeFiles
	// — a bug in the lister must not be able to mask a bug in the Apply path.
	sqlDB := dbtest.New(t)
	libStore := library.New(sqlDB)
	ctx := context.Background()

	series, err := libStore.UpsertSeries(ctx, library.Series{TMDBID: 556, Title: "Show Name", RootFolderPath: dir})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	tracked, err := libStore.UpsertEpisode(ctx, library.Episode{
		SeriesID: series.ID, SeasonNumber: 1, EpisodeNumber: 1, Title: "Pilot", FilePath: trackedFile,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := primaryEpisodeFilePath(t, sqlDB, tracked.ID); got != trackedFile {
		t.Fatalf("precondition: expected the tracked file to start as primary, got %q", got)
	}

	p := proposals.Proposal{
		ID: 1, Status: proposals.Pending, Title: "Show Name", TMDBID: 556, SeasonNumber: 1, EpisodeNumber: 1,
		RootFolderPath: dir,
		Candidates: []proposals.Candidate{
			{Label: "tracked", Path: trackedFile, TrackedID: int(tracked.ID)},
			{Label: "winner", Path: winnerPath, Winner: true},
		},
	}
	if _, _, err := ApplyLibrarySeries(ctx, libStore, p, nil, nil, false, ""); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := os.Stat(trackedFile); !os.IsNotExist(err) {
		t.Fatal("precondition: expected the losing tracked file to be deleted")
	}

	// (2) The denormalized library_episodes.file_path names the winner. This is
	// the pre-hook behaviour the hook must not have disturbed, so it is asserted
	// separately from the library_episode_files row below rather than inferred
	// from it.
	survivor, err := libStore.GetEpisode(ctx, series.ID, 1, 1)
	if err != nil {
		t.Fatalf("re-reading the episode row: %v", err)
	}
	if survivor.FilePath != winnerPath {
		t.Errorf("expected library_episodes.file_path to name the winner %q, got %q", winnerPath, survivor.FilePath)
	}

	// (3) Exactly one primary row, and it is the winner. The count is asserted
	// explicitly because primaryEpisodeFilePath's QueryRow would happily return
	// the first of two.
	if n := countPrimaryEpisodeFileRows(t, sqlDB, tracked.ID); n != 1 {
		t.Errorf("expected exactly one primary episode file row, got %d", n)
	}
	if got := primaryEpisodeFilePath(t, sqlDB, tracked.ID); got != winnerPath {
		t.Errorf("expected the surviving winner %q flagged primary, got %q", winnerPath, got)
	}
	// The deleted loser's row is NOT removed — plan §11.11's flagged, accepted
	// orphan gap. Asserting its survival is what keeps a future session from
	// "fixing" it with deletion logic that was never in scope.
	if n := countEpisodeFileRows(t, sqlDB, tracked.ID); n != 2 {
		t.Errorf("expected the demoted loser row to survive alongside the new primary (2 rows), got %d", n)
	}
}

func primaryEpisodeFilePath(t *testing.T, sqlDB *sql.DB, episodeID int64) string {
	t.Helper()
	var path string
	err := sqlDB.QueryRowContext(context.Background(),
		`SELECT file_path FROM library_episode_files WHERE episode_id = ? AND is_primary = true`, episodeID).Scan(&path)
	if err != nil {
		t.Fatalf("reading primary episode file for episode %d: %v", episodeID, err)
	}
	return path
}

func countPrimaryEpisodeFileRows(t *testing.T, sqlDB *sql.DB, episodeID int64) int {
	t.Helper()
	var n int
	if err := sqlDB.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM library_episode_files WHERE episode_id = ? AND is_primary = true`, episodeID).Scan(&n); err != nil {
		t.Fatalf("counting primary episode files for episode %d: %v", episodeID, err)
	}
	return n
}

func countEpisodeFileRows(t *testing.T, sqlDB *sql.DB, episodeID int64) int {
	t.Helper()
	var n int
	if err := sqlDB.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM library_episode_files WHERE episode_id = ?`, episodeID).Scan(&n); err != nil {
		t.Fatalf("counting episode files for episode %d: %v", episodeID, err)
	}
	return n
}
