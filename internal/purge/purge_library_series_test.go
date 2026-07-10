package purge

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/curtiswtaylorjr/sakms/internal/library"
	"github.com/curtiswtaylorjr/sakms/internal/mode"
	"github.com/curtiswtaylorjr/sakms/internal/proposals"
)

func TestScanLibrarySeries_ProposesOnlySeriesMatchingAllowlist(t *testing.T) {
	libStore := newTestLibraryStore(t)
	ctx := context.Background()

	vanilla, err := libStore.UpsertSeries(ctx, library.Series{TMDBID: 1, Title: "Vanilla Show", RootFolderPath: "/media/TV"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := libStore.AddSeriesTag(ctx, vanilla.ID, "family-friendly"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	flagged, err := libStore.UpsertSeries(ctx, library.Series{TMDBID: 2, Title: "Flagged Show", RootFolderPath: "/media/TV"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := libStore.AddSeriesTag(ctx, flagged.ID, "BDSM"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := libStore.AddSeriesTag(ctx, flagged.ID, "unrelated"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := ScanLibrarySeries(ctx, libStore, []string{"BDSM"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected exactly 1 matched proposal, got %d: %+v", len(got), got)
	}
	p := got[0]
	if p.TrackedID != int(flagged.ID) || p.Title != "Flagged Show" || p.Status != proposals.Pending {
		t.Errorf("unexpected proposal: %+v", p)
	}
	if p.Reason == "" {
		t.Error("expected a populated reason naming the matched tag")
	}
}

func TestScanLibrarySeries_EmptyAllowlistMatchesNothing(t *testing.T) {
	libStore := newTestLibraryStore(t)
	ctx := context.Background()
	series, err := libStore.UpsertSeries(ctx, library.Series{TMDBID: 1, Title: "X", RootFolderPath: "/media/TV"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := libStore.AddSeriesTag(ctx, series.ID, "BDSM"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := ScanLibrarySeries(ctx, libStore, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected no proposals against an empty allowlist, got %+v", got)
	}
}

func TestApplyLibrarySeries_DeletesAllEpisodeFilesAndSeries(t *testing.T) {
	dir := t.TempDir()
	ep1Path := filepath.Join(dir, "s01e01.mkv")
	ep2Path := filepath.Join(dir, "s01e02.mkv")
	if err := os.WriteFile(ep1Path, []byte("data"), 0o644); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := os.WriteFile(ep2Path, []byte("data"), 0o644); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	libStore := newTestLibraryStore(t)
	ctx := context.Background()
	series, err := libStore.UpsertSeries(ctx, library.Series{TMDBID: 1, Title: "Flagged Show", RootFolderPath: dir})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := libStore.UpsertEpisode(ctx, library.Episode{SeriesID: series.ID, SeasonNumber: 1, EpisodeNumber: 1, FilePath: ep1Path}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := libStore.UpsertEpisode(ctx, library.Episode{SeriesID: series.ID, SeasonNumber: 1, EpisodeNumber: 2, FilePath: ep2Path}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// A known-missing episode (no file) shouldn't cause any error on delete.
	if _, err := libStore.UpsertEpisode(ctx, library.Episode{SeriesID: series.ID, SeasonNumber: 1, EpisodeNumber: 3}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	changes, err := ApplyLibrarySeries(ctx, libStore, proposals.Proposal{
		ID: 1, Status: proposals.Pending, Title: "Flagged Show", TrackedID: int(series.ID),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, err := os.Stat(ep1Path); !os.IsNotExist(err) {
		t.Errorf("expected episode 1 file to be deleted, stat returned: %v", err)
	}
	if _, err := os.Stat(ep2Path); !os.IsNotExist(err) {
		t.Errorf("expected episode 2 file to be deleted, stat returned: %v", err)
	}
	if _, err := libStore.GetSeriesByTMDBID(ctx, 1); err != library.ErrNotFound {
		t.Errorf("expected the series to be deleted, got err=%v", err)
	}
	if eps, err := libStore.ListEpisodes(ctx, series.ID); err != nil || len(eps) != 0 {
		t.Errorf("expected episode rows to be deleted, got %v (err=%v)", eps, err)
	}

	// Edge #2 (player-rescan-notify plan): a series purge with N episode
	// files removed reports N Deleted PathChanges in one batch — not just
	// the first, and never one for the known-missing (no file) episode 3.
	wantPaths := map[string]bool{ep1Path: true, ep2Path: true}
	if len(changes) != 2 {
		t.Fatalf("expected exactly 2 Deleted PathChanges (one per episode file), got %+v", changes)
	}
	for _, c := range changes {
		if c.Kind != mode.Deleted {
			t.Errorf("expected Kind Deleted, got %+v", c)
		}
		if !wantPaths[c.Path] {
			t.Errorf("unexpected PathChange path %q, want one of %v", c.Path, wantPaths)
		}
		delete(wantPaths, c.Path)
	}
	if len(wantPaths) != 0 {
		t.Errorf("missing PathChange(s) for: %v", wantPaths)
	}
}

func TestApplyLibrarySeries_RejectsNonPendingProposal(t *testing.T) {
	libStore := newTestLibraryStore(t)
	for _, status := range []proposals.Status{proposals.Applied, proposals.Dismissed, proposals.Unmatched} {
		_, err := ApplyLibrarySeries(context.Background(), libStore, proposals.Proposal{Status: status, TrackedID: 5})
		if err == nil {
			t.Errorf("expected ApplyLibrarySeries to refuse a %q proposal", status)
		}
	}
}

func TestApplyLibrarySeries_RejectsMissingTrackedID(t *testing.T) {
	libStore := newTestLibraryStore(t)
	_, err := ApplyLibrarySeries(context.Background(), libStore, proposals.Proposal{Status: proposals.Pending, TrackedID: 0})
	if err == nil {
		t.Fatal("expected ApplyLibrarySeries to refuse a proposal with no series id")
	}
}
