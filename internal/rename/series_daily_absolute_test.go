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
	"github.com/labbersanon/sakms/internal/tmdb"
)

func TestScanLibrarySeries_DailyAirDatePinned(t *testing.T) {
	tmdb.ResetDefaultCache()
	t.Cleanup(tmdb.ResetDefaultCache)

	root := t.TempDir()
	showDir := filepath.Join(root, "The Daily Show")
	if err := os.MkdirAll(showDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(showDir, "The.Daily.Show.2024.03.15.720p.WEB.mkv"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	client := fakeTMDBEpisodeTitleServer(t, 2224, "The Daily Show", map[int][]tmdb.SeasonEpisode{
		29: {{EpisodeNumber: 47, Name: "Guest", AirDate: "2024-03-15"}},
	}, -1, nil)
	sess := &mode.Session{Mode: mode.Series, TMDB: client}
	libStore := newTestLibraryStore(t)
	ctx := context.Background()
	if _, err := libStore.UpsertSeries(ctx, library.Series{TMDBID: 2224, Title: "The Daily Show", Year: 1996, RootFolderPath: root}); err != nil {
		t.Fatal(err)
	}

	got, err := ScanLibrarySeries(ctx, sess, libStore, root, naming.Jellyfin, DefaultMatchConfig(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d: %+v", len(got), got)
	}
	p := got[0]
	if p.Status != proposals.Pending {
		t.Fatalf("status=%v reason=%q", p.Status, p.Reason)
	}
	if p.TMDBID != 2224 || p.SeasonNumber != 29 || p.EpisodeNumber != 47 {
		t.Fatalf("slot = tmdb=%d S%02dE%02d", p.TMDBID, p.SeasonNumber, p.EpisodeNumber)
	}
}

func TestScanLibrarySeries_AbsoluteAnimePinned(t *testing.T) {
	tmdb.ResetDefaultCache()
	t.Cleanup(tmdb.ResetDefaultCache)

	root := t.TempDir()
	showDir := filepath.Join(root, "One Piece")
	if err := os.MkdirAll(showDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(showDir, "One Piece - 3.mkv"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	client := fakeTMDBEpisodeTitleServer(t, 37854, "One Piece", map[int][]tmdb.SeasonEpisode{
		0: {{EpisodeNumber: 1, Name: "Special", AirDate: "1999-10-01"}},
		1: {
			{EpisodeNumber: 1, Name: "I'm Luffy", AirDate: "1999-10-20"},
			{EpisodeNumber: 2, Name: "Roronoa Zoro", AirDate: "1999-11-17"},
			{EpisodeNumber: 3, Name: "Morgan", AirDate: "1999-11-24"},
		},
	}, -1, nil)
	sess := &mode.Session{Mode: mode.Series, TMDB: client}
	libStore := newTestLibraryStore(t)
	ctx := context.Background()
	if _, err := libStore.UpsertSeries(ctx, library.Series{TMDBID: 37854, Title: "One Piece", Year: 1999, RootFolderPath: root}); err != nil {
		t.Fatal(err)
	}

	got, err := ScanLibrarySeries(ctx, sess, libStore, root, naming.Jellyfin, DefaultMatchConfig(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Status != proposals.Pending {
		t.Fatalf("got %+v", got)
	}
	p := got[0]
	if p.SeasonNumber != 1 || p.EpisodeNumber != 3 {
		t.Fatalf("absolute 3 should skip specials and land S01E03, got S%02dE%02d", p.SeasonNumber, p.EpisodeNumber)
	}
}

func TestScanLibrarySeries_DailyAmbiguousUnmatched(t *testing.T) {
	tmdb.ResetDefaultCache()
	t.Cleanup(tmdb.ResetDefaultCache)

	root := t.TempDir()
	showDir := filepath.Join(root, "The Daily Show")
	if err := os.MkdirAll(showDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(showDir, "The.Daily.Show.2024.03.15.mkv"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	client := fakeTMDBEpisodeTitleServer(t, 2224, "The Daily Show", map[int][]tmdb.SeasonEpisode{
		29: {
			{EpisodeNumber: 47, Name: "One", AirDate: "2024-03-15"},
			{EpisodeNumber: 48, Name: "Two", AirDate: "2024-03-15"},
		},
	}, -1, nil)
	sess := &mode.Session{Mode: mode.Series, TMDB: client}
	libStore := newTestLibraryStore(t)
	ctx := context.Background()
	if _, err := libStore.UpsertSeries(ctx, library.Series{TMDBID: 2224, Title: "The Daily Show", Year: 1996, RootFolderPath: root}); err != nil {
		t.Fatal(err)
	}

	got, err := ScanLibrarySeries(ctx, sess, libStore, root, naming.Jellyfin, DefaultMatchConfig(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Status != proposals.Unmatched {
		t.Fatalf("want unmatched, got %+v", got)
	}
}
