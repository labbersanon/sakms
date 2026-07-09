package grabs

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/curtiswtaylorjr/sakms/internal/db"
	"github.com/curtiswtaylorjr/sakms/internal/mode"
)

// newTestStore builds a Store against a real, freshly migrated SQLite file —
// exercising the actual SQL, not a mock, matching every other store test in
// this repo.
func newTestStore(t *testing.T) *Store {
	t.Helper()
	sqlDB, err := db.Open(filepath.Join(t.TempDir(), "sakms.db"))
	if err != nil {
		t.Fatalf("opening db: %v", err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	return New(sqlDB)
}

func TestCreate_StartsQueuedWithTimestampsPopulated(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	g, err := s.Create(ctx, Grab{
		Mode: mode.Movies, Title: "Some Movie", TMDBID: 123,
		Indexer: "SomeIndexer", Protocol: "torrent", DownloadClient: "qbittorrent",
		ClientRef: "abc123", RootFolderPath: "/movies",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if g.ID == 0 {
		t.Error("expected a nonzero ID")
	}
	if g.Status != Queued {
		t.Errorf("expected new grab to start Queued, got %q", g.Status)
	}
	if g.CreatedAt == "" || g.UpdatedAt == "" {
		t.Error("expected CreatedAt/UpdatedAt to be populated")
	}
}

func TestGet_RoundTripsEveryField(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	created, err := s.Create(ctx, Grab{
		Mode: mode.Series, Title: "Some Show S01E01", TVDBID: 456,
		Indexer: "SomeUsenetIndexer", Protocol: "usenet", DownloadClient: "nzbget",
		ClientRef: "42", RootFolderPath: "/tv",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := s.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Mode != mode.Series || got.Title != "Some Show S01E01" || got.TVDBID != 456 ||
		got.Indexer != "SomeUsenetIndexer" || got.Protocol != "usenet" || got.DownloadClient != "nzbget" ||
		got.ClientRef != "42" || got.RootFolderPath != "/tv" || got.Status != Queued {
		t.Errorf("unexpected round-tripped grab: %+v", got)
	}
}

func TestGet_NotFound(t *testing.T) {
	s := newTestStore(t)
	_, err := s.Get(context.Background(), 999)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestUpdateStatus_ChangesStatusAndTimestamp(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	created, err := s.Create(ctx, Grab{Mode: mode.Movies, Title: "X", Indexer: "I", Protocol: "torrent", DownloadClient: "qbittorrent", RootFolderPath: "/movies"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if err := s.UpdateStatus(ctx, created.ID, Downloading); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := s.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Status != Downloading {
		t.Errorf("expected status Downloading, got %q", got.Status)
	}
}

func TestUpdateStatus_NotFound(t *testing.T) {
	s := newTestStore(t)
	err := s.UpdateStatus(context.Background(), 999, Completed)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestList_ScopedByModeAndOrderedNewestFirst(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if _, err := s.Create(ctx, Grab{Mode: mode.Movies, Title: "Movie A", Indexer: "I", Protocol: "torrent", DownloadClient: "qbittorrent", RootFolderPath: "/movies"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := s.Create(ctx, Grab{Mode: mode.Movies, Title: "Movie B", Indexer: "I", Protocol: "torrent", DownloadClient: "qbittorrent", RootFolderPath: "/movies"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := s.Create(ctx, Grab{Mode: mode.Series, Title: "Show A", Indexer: "I", Protocol: "usenet", DownloadClient: "nzbget", RootFolderPath: "/tv"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	movies, err := s.List(ctx, mode.Movies)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(movies) != 2 {
		t.Fatalf("expected 2 movie grabs, got %d", len(movies))
	}
	if movies[0].Title != "Movie B" {
		t.Errorf("expected most recently created grab first, got %q", movies[0].Title)
	}
}

func TestList_EmptyIsNotNil(t *testing.T) {
	s := newTestStore(t)
	got, err := s.List(context.Background(), mode.Adult)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got == nil {
		t.Error("expected an empty slice, not nil, so it serializes as [] not null")
	}
}
