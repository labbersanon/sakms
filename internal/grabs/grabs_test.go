package grabs

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/labbersanon/sakms/internal/db"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/secrets"
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
	secretStore, err := secrets.New(make([]byte, 32))
	if err != nil {
		t.Fatalf("building secret store: %v", err)
	}
	return New(sqlDB, secretStore)
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
		SeasonNumber: 1, EpisodeNumber: 1, SeasonSpecified: true,
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
		got.SeasonNumber != 1 || got.EpisodeNumber != 1 || !got.SeasonSpecified ||
		got.Indexer != "SomeUsenetIndexer" || got.Protocol != "usenet" || got.DownloadClient != "nzbget" ||
		got.ClientRef != "42" || got.RootFolderPath != "/tv" || got.Status != Queued {
		t.Errorf("unexpected round-tripped grab: %+v", got)
	}
}

func TestCreate_SeasonSpecifiedDefaultsFalse(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	created, err := s.Create(ctx, Grab{
		Mode: mode.Movies, Title: "Some Movie", TMDBID: 123,
		Indexer: "SomeIndexer", Protocol: "torrent", DownloadClient: "qbittorrent",
		RootFolderPath: "/movies",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if created.SeasonSpecified {
		t.Error("expected SeasonSpecified to default false when not set")
	}
}

// mustCreateWithGID creates a grab and stamps its download GID — the shape the
// grab handlers produce (Create, then SetDownloadGID once the download client
// assigns the GID).
func mustCreateWithGID(t *testing.T, s *Store, m mode.Mode, title, gid string) Grab {
	t.Helper()
	ctx := context.Background()
	g, err := s.Create(ctx, Grab{Mode: m, Title: title, Indexer: "I", Protocol: "torrent", DownloadClient: "anacrolix", RootFolderPath: "/x"})
	if err != nil {
		t.Fatalf("creating grab %q: %v", title, err)
	}
	if err := s.SetDownloadGID(ctx, g.ID, gid); err != nil {
		t.Fatalf("setting gid for %q: %v", title, err)
	}
	g.DownloadGID = gid
	return g
}

// TestActiveByDownloadGID_FindsInFlightScopedByModeAndGID proves the guard
// lookup is keyed on (mode, download_gid): it finds the in-flight grab for a
// GID, does NOT conflate two distinct GIDs (so genuinely distinct downloads
// never block each other), and is scoped per-mode.
func TestActiveByDownloadGID_FindsInFlightScopedByModeAndGID(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	a := mustCreateWithGID(t, s, mode.Movies, "Movie A", "gid-a")
	b := mustCreateWithGID(t, s, mode.Movies, "Movie B", "gid-b")
	c := mustCreateWithGID(t, s, mode.Series, "Show C", "gid-a") // same GID, different mode

	got, err := s.ActiveByDownloadGID(ctx, mode.Movies, "gid-a")
	if err != nil || got.ID != a.ID {
		t.Errorf("expected grab %d for (movies, gid-a), got %+v err=%v", a.ID, got, err)
	}
	got, err = s.ActiveByDownloadGID(ctx, mode.Movies, "gid-b")
	if err != nil || got.ID != b.ID {
		t.Errorf("a distinct GID must not be conflated: expected grab %d for (movies, gid-b), got %+v err=%v", b.ID, got, err)
	}
	got, err = s.ActiveByDownloadGID(ctx, mode.Series, "gid-a")
	if err != nil || got.ID != c.ID {
		t.Errorf("expected mode scoping: grab %d for (series, gid-a), got %+v err=%v", c.ID, got, err)
	}
	if _, err := s.ActiveByDownloadGID(ctx, mode.Movies, "gid-none"); !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound for an unknown GID, got %v", err)
	}
}

// TestActiveByDownloadGID_IgnoresTerminalStates proves a grab that reached a
// terminal state (imported or failed) no longer counts as active — a fresh
// re-grab of that same release is legitimate and must not be blocked — while a
// still-in-flight grab (including completed-but-not-yet-imported) does count.
func TestActiveByDownloadGID_IgnoresTerminalStates(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	for _, st := range []Status{Imported, Failed} {
		g := mustCreateWithGID(t, s, mode.Movies, "Movie "+string(st), "gid-"+string(st))
		if err := s.UpdateStatus(ctx, g.ID, st); err != nil {
			t.Fatalf("updating status to %s: %v", st, err)
		}
		if _, err := s.ActiveByDownloadGID(ctx, mode.Movies, "gid-"+string(st)); !errors.Is(err, ErrNotFound) {
			t.Errorf("a %s grab must not count as active (a fresh re-grab is legitimate), got %v", st, err)
		}
	}

	g := mustCreateWithGID(t, s, mode.Movies, "Movie completed", "gid-completed")
	if err := s.UpdateStatus(ctx, g.ID, Completed); err != nil {
		t.Fatalf("updating status to completed: %v", err)
	}
	got, err := s.ActiveByDownloadGID(ctx, mode.Movies, "gid-completed")
	if err != nil || got.ID != g.ID {
		t.Errorf("a completed (not-yet-imported) grab must still count as active, got %+v err=%v", got, err)
	}
}

// TestActiveByDownloadGID_EmptyGIDNotFound proves an empty GID is never a dedup
// key (many grabs legitimately have download_gid='').
func TestActiveByDownloadGID_EmptyGIDNotFound(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.ActiveByDownloadGID(context.Background(), mode.Movies, ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound for an empty GID, got %v", err)
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

func TestFlag_RoundTripsAndDefaultsUnflagged(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	created, err := s.Create(ctx, Grab{
		Mode: mode.Movies, Title: "Some Movie", TMDBID: 123,
		Indexer: "I", Protocol: "torrent", DownloadClient: "qbittorrent", RootFolderPath: "/movies",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// A brand-new grab is unflagged by default.
	if created.FlaggedForReview || created.FlagReason != "" {
		t.Fatalf("new grab should be unflagged, got flagged=%v reason=%q", created.FlaggedForReview, created.FlagReason)
	}

	const reason = "imported file runs 4 min but TMDB lists 100 min — possible mislabel or wrong content"
	if err := s.Flag(ctx, created.ID, reason); err != nil {
		t.Fatalf("Flag: %v", err)
	}

	got, err := s.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !got.FlaggedForReview || got.FlagReason != reason {
		t.Fatalf("after Flag: flagged=%v reason=%q, want true/%q", got.FlaggedForReview, got.FlagReason, reason)
	}
	// The flag must NOT touch the lifecycle status — the import still succeeded.
	if got.Status != Queued {
		t.Fatalf("Flag changed status to %q; it must leave the lifecycle status alone", got.Status)
	}
}

func TestFlag_NotFound(t *testing.T) {
	s := newTestStore(t)
	if err := s.Flag(context.Background(), 999, "x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound flagging a missing grab, got %v", err)
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
