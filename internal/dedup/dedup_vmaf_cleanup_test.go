package dedup

import (
	"context"
	"os"
	"testing"

	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/proposals"
)

// The three tests below are Stage 1's AC9 coverage: a losing candidate's
// cached vmaf_scores row must be gone once Apply deletes that candidate's file,
// once per Dedup mode (the 3 logical prune hook points — Movies'
// removeLibraryCandidate, Series' inline delete loop, Adult's
// removeSceneCandidate). The assertion routes through the raw GetVMAFScore
// (which does not stat the file), so it fails if the row survives — it can't
// pass vacuously just because the deleted file now stats as missing.

func TestApplyLibrary_PrunesVMAFScore_OnDeletedLoser_Movies(t *testing.T) {
	dir := t.TempDir()
	loserPath := writeVideoFile(t, dir, "loser.mkv", 10)
	const winnerPath = "/winner.mkv"

	libStore := newTestLibraryStore(t)
	ctx := context.Background()
	tracked, err := libStore.Upsert(ctx, library.Item{
		Mode: mode.Movies, TMDBID: 1, Title: "X", FilePath: winnerPath, RootFolderPath: dir,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Seed a cached VMAF pair naming the loser as the candidate.
	if err := libStore.UpsertVMAFScore(ctx, library.VMAFScore{
		CandidatePath: loserPath, CandidateFileSize: 10, CandidateFileMTime: "t",
		ReferencePath: winnerPath, Score: 95, ComputedAt: "c",
	}); err != nil {
		t.Fatalf("seed vmaf score: %v", err)
	}

	p := proposals.Proposal{
		ID: 1, Status: proposals.Pending, Title: "X", TMDBID: 1,
		Candidates: []proposals.Candidate{
			{Label: "winner", Path: winnerPath, TrackedID: int(tracked.ID), Winner: true},
			{Label: "loser", Path: loserPath},
		},
	}
	if _, _, err := ApplyLibrary(ctx, libStore, p, nil, nil, false); err != nil {
		t.Fatalf("apply: %v", err)
	}

	if _, err := os.Stat(loserPath); !os.IsNotExist(err) {
		t.Fatal("expected the losing file to be deleted")
	}
	got, err := libStore.GetVMAFScore(ctx, loserPath, winnerPath)
	if err != nil {
		t.Fatalf("get vmaf score: %v", err)
	}
	if got.CandidatePath != "" {
		t.Errorf("expected the cached VMAF score pruned after the loser was deleted, got %+v", got)
	}
}

func TestApplyLibrarySeries_PrunesVMAFScore_OnDeletedLoser_Series(t *testing.T) {
	dir := t.TempDir()
	loserPath := writeVideoFile(t, dir, "loser.mkv", 10)
	const winnerPath = "/winner.mkv"

	libStore := newTestLibraryStore(t)
	ctx := context.Background()
	series, err := libStore.UpsertSeries(ctx, library.Series{TMDBID: 1, Title: "X", RootFolderPath: dir})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	tracked, err := libStore.UpsertEpisode(ctx, library.Episode{
		SeriesID: series.ID, SeasonNumber: 1, EpisodeNumber: 1, Title: "Pilot", FilePath: winnerPath,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := libStore.UpsertVMAFScore(ctx, library.VMAFScore{
		CandidatePath: loserPath, CandidateFileSize: 10, CandidateFileMTime: "t",
		ReferencePath: winnerPath, Score: 91, ComputedAt: "c",
	}); err != nil {
		t.Fatalf("seed vmaf score: %v", err)
	}

	p := proposals.Proposal{
		ID: 1, Status: proposals.Pending, Title: "X", TMDBID: 1, SeasonNumber: 1, EpisodeNumber: 1,
		Candidates: []proposals.Candidate{
			{Label: "winner", Path: winnerPath, TrackedID: int(tracked.ID), Winner: true},
			{Label: "loser", Path: loserPath},
		},
	}
	if _, _, err := ApplyLibrarySeries(ctx, libStore, p, nil, nil, false); err != nil {
		t.Fatalf("apply: %v", err)
	}

	if _, err := os.Stat(loserPath); !os.IsNotExist(err) {
		t.Fatal("expected the losing file to be deleted")
	}
	got, err := libStore.GetVMAFScore(ctx, loserPath, winnerPath)
	if err != nil {
		t.Fatalf("get vmaf score: %v", err)
	}
	if got.CandidatePath != "" {
		t.Errorf("expected the cached VMAF score pruned after the loser was deleted, got %+v", got)
	}
}

func TestApplyLibraryAdult_PrunesVMAFScore_OnDeletedLoser_Adult(t *testing.T) {
	dir := t.TempDir()
	loserPath := writeVideoFile(t, dir, "loser.mkv", 10)
	const winnerPath = "/winner.mkv"

	libStore := newTestLibraryStore(t)
	ctx := context.Background()
	tracked, err := libStore.UpsertScene(ctx, library.Scene{
		Box: "stashdb", SceneID: sceneUUIDA, Title: "X", FilePath: winnerPath, RootFolderPath: dir,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := libStore.UpsertVMAFScore(ctx, library.VMAFScore{
		CandidatePath: loserPath, CandidateFileSize: 10, CandidateFileMTime: "t",
		ReferencePath: winnerPath, Score: 88, ComputedAt: "c",
	}); err != nil {
		t.Fatalf("seed vmaf score: %v", err)
	}

	p := proposals.Proposal{
		ID: 1, Status: proposals.Pending, Title: "X",
		GiveBackBox: "stashdb", GiveBackSceneID: sceneUUIDA,
		Candidates: []proposals.Candidate{
			{Label: "winner", Path: winnerPath, TrackedID: int(tracked.ID), Winner: true},
			{Label: "loser", Path: loserPath},
		},
	}
	if _, _, err := ApplyLibraryAdult(ctx, libStore, p, nil, nil, false); err != nil {
		t.Fatalf("apply: %v", err)
	}

	if _, err := os.Stat(loserPath); !os.IsNotExist(err) {
		t.Fatal("expected the losing file to be deleted")
	}
	got, err := libStore.GetVMAFScore(ctx, loserPath, winnerPath)
	if err != nil {
		t.Fatalf("get vmaf score: %v", err)
	}
	if got.CandidatePath != "" {
		t.Errorf("expected the cached VMAF score pruned after the loser was deleted, got %+v", got)
	}
}
