package dedup

import (
	"context"
	"os"
	"testing"

	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/proposals"
)

func TestApplyLibrarySeries_KeepsWinnerByDefault_DeletesOrphanLoser(t *testing.T) {
	dir := t.TempDir()
	loserPath := writeVideoFile(t, dir, "loser.mkv", 10)

	libStore := newTestLibraryStore(t)
	ctx := context.Background()
	series, err := libStore.UpsertSeries(ctx, library.Series{TMDBID: 1, Title: "X", RootFolderPath: dir})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	tracked, err := libStore.UpsertEpisode(ctx, library.Episode{
		SeriesID: series.ID, SeasonNumber: 1, EpisodeNumber: 1, Title: "Pilot", FilePath: "/winner.mkv",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	p := proposals.Proposal{
		ID: 1, Status: proposals.Pending, Title: "X", TMDBID: 1, SeasonNumber: 1, EpisodeNumber: 1,
		Candidates: []proposals.Candidate{
			{Label: "winner", Path: "/winner.mkv", TrackedID: int(tracked.ID), Winner: true},
			{Label: "loser", Path: loserPath},
		},
	}
	id, changes, err := ApplyLibrarySeries(ctx, libStore, p, nil, nil, false, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != tracked.ID {
		t.Errorf("expected the already-tracked winner's episode id (%d), got %d", tracked.ID, id)
	}
	if _, err := os.Stat(loserPath); !os.IsNotExist(err) {
		t.Error("expected the losing orphan file to be deleted")
	}
	ep, err := libStore.GetEpisode(ctx, series.ID, 1, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ep.FilePath != "/winner.mkv" || ep.Title != "Pilot" {
		t.Errorf("expected the episode row untouched, got %+v", ep)
	}
	// The winner didn't move, so only the loser's exact candidate path
	// shows up in changes.
	if len(changes) != 1 || changes[0].Path != loserPath || changes[0].Kind != mode.Deleted {
		t.Errorf("expected exactly one Deleted PathChange for %q, got %+v", loserPath, changes)
	}
}

func TestApplyLibrarySeries_WinnerIsOrphan_DeletesTrackedLoserFile_UpsertsSameEpisodeRow(t *testing.T) {
	dir := t.TempDir()
	trackedFile := writeVideoFile(t, dir, "tracked.mkv", 10)
	winnerPath := writeVideoFile(t, dir, "winner.mkv", 10)

	libStore := newTestLibraryStore(t)
	ctx := context.Background()
	series, err := libStore.UpsertSeries(ctx, library.Series{TMDBID: 555, Title: "Show Name", RootFolderPath: dir})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	tracked, err := libStore.UpsertEpisode(ctx, library.Episode{
		SeriesID: series.ID, SeasonNumber: 1, EpisodeNumber: 1, Title: "Pilot", AirDate: "2020-01-01", FilePath: trackedFile,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	p := proposals.Proposal{
		ID: 1, Status: proposals.Pending, Title: "Show Name", TMDBID: 555, SeasonNumber: 1, EpisodeNumber: 1,
		RootFolderPath: dir,
		Candidates: []proposals.Candidate{
			{Label: "tracked", Path: trackedFile, TrackedID: int(tracked.ID)},
			{Label: "winner", Path: winnerPath, Winner: true},
		},
	}
	id, changes, err := ApplyLibrarySeries(ctx, libStore, p, nil, nil, false, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id == 0 {
		t.Error("expected a nonzero episode id for the newly registered winner")
	}
	// Same episode row (same id), not a fresh one — the slot's content was
	// overwritten, nothing was ever deleted.
	if id != tracked.ID {
		t.Errorf("expected the same episode row id to be reused (%d), got %d", tracked.ID, id)
	}
	if _, err := os.Stat(trackedFile); !os.IsNotExist(err) {
		t.Error("expected the losing tracked file to be deleted")
	}

	ep, err := libStore.GetEpisode(ctx, series.ID, 1, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ep.FilePath != winnerPath {
		t.Errorf("expected the episode row's file path updated to the winner, got %+v", ep)
	}
	// Existing metadata (from a prior Sonarr import/Rename scan) is
	// preserved, not blanked, even though this Apply call only supplied a
	// file path.
	if ep.Title != "Pilot" || ep.AirDate != "2020-01-01" {
		t.Errorf("expected existing episode metadata preserved, got %+v", ep)
	}
	// Row 8 (player-rescan-notify plan): the removed loser's candidate path
	// (c.Path) is reported. The winner's slot was overwritten in place, not
	// moved, so it never appears in changes.
	if len(changes) != 1 || changes[0].Path != trackedFile || changes[0].Kind != mode.Deleted {
		t.Errorf("expected exactly one Deleted PathChange for %q, got %+v", trackedFile, changes)
	}
}

// TestApplyLibrarySeries_SharedFileLosesItsOwnKey_NotDeleted_SiblingIntact is
// the critical regression test for the logical-episode-splitting
// correctness fix: episode 1 and episode 2 of the same series share ONE
// physical file (a "S01E01-E02" split). Dedup's episode-1 dedup group finds
// a better standalone copy of episode 1 elsewhere, so the shared file LOSES
// its own key's comparison. Before the fix, ApplyLibrarySeries would
// unconditionally os.Remove the loser — deleting the shared file while
// episode 2's row still pointed at it, a live "no drift" mission violation
// (see CLAUDE.md's Mission section and dedup.go's ApplyLibrarySeries doc
// comment). This test proves: the shared file survives on disk, episode 2's
// row is completely untouched, and episode 1's row is still correctly
// updated to the winner.
func TestApplyLibrarySeries_SharedFileLosesItsOwnKey_NotDeleted_SiblingIntact(t *testing.T) {
	dir := t.TempDir()
	sharedFile := writeVideoFile(t, dir, "Show.S01E01-E02.mkv", 10)
	winnerPath := writeVideoFile(t, dir, "winner.mkv", 10)

	libStore := newTestLibraryStore(t)
	ctx := context.Background()
	series, err := libStore.UpsertSeries(ctx, library.Series{TMDBID: 555, Title: "Show Name", RootFolderPath: dir})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Episode 1 AND episode 2 both point at the exact same file — the
	// logical-episode-split scenario library.CountEpisodesByFilePath exists
	// to detect.
	ep1, err := libStore.UpsertEpisode(ctx, library.Episode{
		SeriesID: series.ID, SeasonNumber: 1, EpisodeNumber: 1, Title: "Part One", FilePath: sharedFile,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	ep2Before, err := libStore.UpsertEpisode(ctx, library.Episode{
		SeriesID: series.ID, SeasonNumber: 1, EpisodeNumber: 2, Title: "Part Two", AirDate: "2020-01-08", FilePath: sharedFile,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Dedup proposal for EPISODE 1's key only — the shared file is the
	// tracked/losing candidate, a standalone orphan copy is the winner.
	p := proposals.Proposal{
		ID: 1, Status: proposals.Pending, Title: "Show Name", TMDBID: 555, SeasonNumber: 1, EpisodeNumber: 1,
		RootFolderPath: dir,
		Candidates: []proposals.Candidate{
			{Label: "tracked", Path: sharedFile, TrackedID: int(ep1.ID)},
			{Label: "winner", Path: winnerPath, Winner: true},
		},
	}
	_, changes, err := ApplyLibrarySeries(ctx, libStore, p, nil, nil, false, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// The critical assertion: the shared file must survive on disk, even
	// though it lost episode 1's dedup comparison, because episode 2's row
	// still needs it.
	if _, err := os.Stat(sharedFile); err != nil {
		t.Fatalf("expected the shared file to SURVIVE (still referenced by episode 2), but stat failed: %v", err)
	}
	// No Deleted PathChange should be reported for a file that was never
	// actually deleted.
	for _, c := range changes {
		if c.Path == sharedFile && c.Kind == mode.Deleted {
			t.Errorf("expected no Deleted PathChange for the still-referenced shared file, got %+v", changes)
		}
	}

	// Episode 2's row must be completely untouched — same file path, same
	// metadata, same row id.
	ep2After, err := libStore.GetEpisode(ctx, series.ID, 1, 2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ep2After.ID != ep2Before.ID || ep2After.FilePath != sharedFile {
		t.Fatalf("expected episode 2's row to be completely untouched, got %+v (was %+v)", ep2After, ep2Before)
	}
	if ep2After.Title != "Part Two" || ep2After.AirDate != "2020-01-08" {
		t.Errorf("expected episode 2's metadata untouched, got %+v", ep2After)
	}

	// Episode 1's row is still correctly updated to the winner — the fix
	// doesn't mean episode 1's OWN dedup resolution stops working, only
	// that the shared file's physical deletion is what's guarded.
	ep1After, err := libStore.GetEpisode(ctx, series.ID, 1, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ep1After.FilePath != winnerPath {
		t.Errorf("expected episode 1's row updated to the winner path, got %+v", ep1After)
	}
}

// TestApplyLibrarySeries_SharedFileGuardIsPathBased_NotCandidateLabelBased
// closes a follow-up flagged during pre-merge review: the shared-file guard
// (CountEpisodesByFilePath) is a pure function of the candidate's PATH
// STRING, not of how that candidate was labeled/discovered. This proves it
// protects a shared file even when it arrives as a plain, non-"tracked"-
// labeled losing candidate (TrackedID=0) — not just the "tracked" shape the
// other regression test already covers. (Note: a shared file surfacing as
// an actual Dedup-scan-discovered ORPHAN is not reachable in practice —
// ScanLibrarySeries's `known` set masks every already-tracked FilePath from
// ever being reported as an unmapped/orphan entry in the first place,
// regardless of which key it would parse to — but ApplyLibrarySeries
// itself makes no such assumption about how its Candidates arrived, so this
// test exercises that generality directly.)
func TestApplyLibrarySeries_SharedFileGuardIsPathBased_NotCandidateLabelBased(t *testing.T) {
	dir := t.TempDir()
	sharedFile := writeVideoFile(t, dir, "Show.S01E01-E02.mkv", 10)
	winnerPath := writeVideoFile(t, dir, "winner.mkv", 10)

	libStore := newTestLibraryStore(t)
	ctx := context.Background()
	series, err := libStore.UpsertSeries(ctx, library.Series{TMDBID: 555, Title: "Show Name", RootFolderPath: dir})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := libStore.UpsertEpisode(ctx, library.Episode{
		SeriesID: series.ID, SeasonNumber: 1, EpisodeNumber: 1, FilePath: sharedFile,
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := libStore.UpsertEpisode(ctx, library.Episode{
		SeriesID: series.ID, SeasonNumber: 1, EpisodeNumber: 2, FilePath: sharedFile,
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// The shared path arrives as a PLAIN candidate (TrackedID=0, not labeled
	// "tracked") — a shape that wouldn't occur via today's real Scan, but
	// which ApplyLibrarySeries must still guard correctly, since its guard
	// keys purely on Path.
	p := proposals.Proposal{
		ID: 1, Status: proposals.Pending, Title: "Show Name", TMDBID: 555, SeasonNumber: 1, EpisodeNumber: 1,
		RootFolderPath: dir,
		Candidates: []proposals.Candidate{
			{Label: "some-orphan-name", Path: sharedFile},
			{Label: "winner", Path: winnerPath, Winner: true},
		},
	}
	if _, changes, err := ApplyLibrarySeries(ctx, libStore, p, nil, nil, false, ""); err != nil {
		t.Fatalf("unexpected error: %v", err)
	} else {
		for _, c := range changes {
			if c.Path == sharedFile && c.Kind == mode.Deleted {
				t.Errorf("expected no Deleted PathChange for the still-referenced shared file, got %+v", changes)
			}
		}
	}

	if _, err := os.Stat(sharedFile); err != nil {
		t.Fatalf("expected the shared file to survive regardless of candidate labeling, but stat failed: %v", err)
	}
	ep2, err := libStore.GetEpisode(ctx, series.ID, 1, 2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ep2.FilePath != sharedFile {
		t.Errorf("expected episode 2's row untouched, got %+v", ep2)
	}
}

func TestApplyLibrarySeries_KeepAll_NoMutation(t *testing.T) {
	libStore := newTestLibraryStore(t)
	ctx := context.Background()
	series, err := libStore.UpsertSeries(ctx, library.Series{TMDBID: 1, Title: "X", RootFolderPath: "/x"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	tracked, err := libStore.UpsertEpisode(ctx, library.Episode{
		SeriesID: series.ID, SeasonNumber: 1, EpisodeNumber: 1, FilePath: "/a.mkv",
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
	id, changes, err := ApplyLibrarySeries(ctx, libStore, p, nil, nil, true, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != tracked.ID {
		t.Errorf("expected keepAll to still report the existing tracked episode id, got %d", id)
	}
	if _, err := libStore.GetEpisode(ctx, series.ID, 1, 1); err != nil {
		t.Errorf("expected keepAll to leave the episode row untouched, got err=%v", err)
	}
	// Edge #3 (player-rescan-notify plan): keepAll removes nothing, so it
	// must report zero PathChanges.
	if len(changes) != 0 {
		t.Errorf("expected keepAll to report zero PathChanges, got %+v", changes)
	}
}

func TestApplyLibrarySeries_RejectsNonPendingProposal(t *testing.T) {
	libStore := newTestLibraryStore(t)
	p := proposals.Proposal{
		Status:     proposals.Applied,
		Candidates: []proposals.Candidate{{Path: "/a.mkv"}, {Path: "/b.mkv"}},
	}
	if _, _, err := ApplyLibrarySeries(context.Background(), libStore, p, nil, nil, false, ""); err == nil {
		t.Fatal("expected ApplyLibrarySeries to refuse an already-applied proposal")
	}
}

func TestApplyLibrarySeries_RejectsFewerThanTwoCandidates(t *testing.T) {
	libStore := newTestLibraryStore(t)
	p := proposals.Proposal{Status: proposals.Pending, Candidates: []proposals.Candidate{{Path: "/a.mkv"}}}
	if _, _, err := ApplyLibrarySeries(context.Background(), libStore, p, nil, nil, false, ""); err == nil {
		t.Fatal("expected ApplyLibrarySeries to refuse a proposal with fewer than 2 candidates")
	}
}
