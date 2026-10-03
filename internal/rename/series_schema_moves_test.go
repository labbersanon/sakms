package rename

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/naming"
	"github.com/labbersanon/sakms/internal/proposals"
)

func TestScanLibrarySeries_MovesTrackedFileIntoPresetLayout(t *testing.T) {
	root := t.TempDir()
	messy := filepath.Join(root, "Messy Show Folder", "cartoon.S1947E05.mkv")
	if err := os.MkdirAll(filepath.Dir(messy), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(messy, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	libStore := newTestLibraryStore(t)
	ctx := context.Background()
	series, err := libStore.UpsertSeries(ctx, library.Series{
		TMDBID: 42, Title: "Classic Shorts", Year: 1929, RootFolderPath: root,
	})
	if err != nil {
		t.Fatal(err)
	}
	ep, err := libStore.UpsertEpisode(ctx, library.Episode{
		SeriesID: series.ID, SeasonNumber: 1947, EpisodeNumber: 5, Title: "A Cartoon", FilePath: messy,
	})
	if err != nil {
		t.Fatal(err)
	}

	sess := &mode.Session{Mode: mode.Series, TMDB: fakeTMDBSeriesServer(t, map[string]string{}, nil)}
	got, err := ScanLibrarySeries(ctx, sess, libStore, root, naming.Jellyfin, DefaultMatchConfig(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var move *proposals.Proposal
	for i := range got {
		if got[i].SourcePath == messy && got[i].Status == proposals.Pending {
			move = &got[i]
			break
		}
	}
	if move == nil {
		t.Fatalf("expected a hierarchy move for the tracked file, got %+v", got)
	}
	if move.TrackedID != int(ep.ID) {
		t.Errorf("TrackedID = %d, want episode id %d so DeleteSource can refuse", move.TrackedID, ep.ID)
	}
	if move.SeasonNumber != 1947 || move.EpisodeNumber != 5 {
		t.Errorf("placement = S%dE%d, want S1947E05", move.SeasonNumber, move.EpisodeNumber)
	}
	wantDest := ImportDestPath(*move, naming.Jellyfin)
	want := filepath.Join(root, "Classic Shorts (1929) [tmdbid-42]", "Season 1947", "Classic Shorts S1947E05 A Cartoon.mkv")
	if wantDest != want {
		t.Errorf("dest = %q, want %q", wantDest, want)
	}
	if !naming.MatchesSeriesSchema(wantDest, naming.Jellyfin) {
		t.Errorf("dest %q must be schema-conformant so a later Scan does not loop", wantDest)
	}
}

func TestScanLibrarySeries_SkipsAlreadySchemaTrackedFile(t *testing.T) {
	root := t.TempDir()
	placed := filepath.Join(root, "Classic Shorts (1929) [tmdbid-42]", "Season 1947", "Classic Shorts S1947E05 A Cartoon.mkv")
	if err := os.MkdirAll(filepath.Dir(placed), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(placed, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	libStore := newTestLibraryStore(t)
	ctx := context.Background()
	series, err := libStore.UpsertSeries(ctx, library.Series{
		TMDBID: 42, Title: "Classic Shorts", Year: 1929, RootFolderPath: root,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := libStore.UpsertEpisode(ctx, library.Episode{
		SeriesID: series.ID, SeasonNumber: 1947, EpisodeNumber: 5, Title: "A Cartoon", FilePath: placed,
	}); err != nil {
		t.Fatal(err)
	}

	sess := &mode.Session{Mode: mode.Series, TMDB: fakeTMDBSeriesServer(t, map[string]string{}, nil)}
	got, err := ScanLibrarySeries(ctx, sess, libStore, root, naming.Jellyfin, DefaultMatchConfig(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range got {
		if p.SourcePath == placed {
			t.Fatalf("schema-conformant tracked file must not be re-proposed, got %+v", p)
		}
	}
}

func TestScanLibrarySeries_SkipsSyntheticTMDBTrackedMove(t *testing.T) {
	root := t.TempDir()
	messy := filepath.Join(root, "Dump", "short.mkv")
	if err := os.MkdirAll(filepath.Dir(messy), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(messy, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	libStore := newTestLibraryStore(t)
	ctx := context.Background()
	series, err := libStore.UpsertSeries(ctx, library.Series{
		TMDBID: -99, Title: "Anthology", Year: 1921, RootFolderPath: root,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := libStore.UpsertEpisode(ctx, library.Episode{
		SeriesID: series.ID, SeasonNumber: 3, EpisodeNumber: 1, FilePath: messy,
	}); err != nil {
		t.Fatal(err)
	}

	sess := &mode.Session{Mode: mode.Series, TMDB: fakeTMDBSeriesServer(t, map[string]string{}, nil)}
	got, err := ScanLibrarySeries(ctx, sess, libStore, root, naming.Jellyfin, DefaultMatchConfig(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range got {
		if p.SourcePath == messy && p.TrackedID != 0 {
			t.Fatalf("synthetic TMDB ids must not loop hierarchy moves, got %+v", p)
		}
	}
}

func TestScanLibrarySeries_SkipsTrackedMoveOutsideScanRoots(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "already-tracked.mkv")
	if err := os.WriteFile(outside, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	libStore := newTestLibraryStore(t)
	ctx := context.Background()
	series, err := libStore.UpsertSeries(ctx, library.Series{
		TMDBID: 42, Title: "Classic Shorts", Year: 1929, RootFolderPath: root,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := libStore.UpsertEpisode(ctx, library.Episode{
		SeriesID: series.ID, SeasonNumber: 1947, EpisodeNumber: 5, FilePath: outside,
	}); err != nil {
		t.Fatal(err)
	}

	sess := &mode.Session{Mode: mode.Series, TMDB: fakeTMDBSeriesServer(t, map[string]string{}, nil)}
	got, err := ScanLibrarySeries(ctx, sess, libStore, root, naming.Jellyfin, DefaultMatchConfig(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range got {
		if p.SourcePath == outside {
			t.Fatalf("files outside scan roots must not get hierarchy moves, got %+v", p)
		}
	}
}
