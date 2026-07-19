package rssfeeds

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/curtiswtaylorjr/sakms/internal/db"
)

// newTestStore builds a Store against a real, freshly migrated SQLite file —
// exercising the actual schema/migration, not mocks, the same way every
// other store in this repo is tested (see discoversliders_test.go).
func newTestStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()

	sqlDB, err := db.Open(filepath.Join(dir, "sakms.db"))
	if err != nil {
		t.Fatalf("opening db: %v", err)
	}
	t.Cleanup(func() { sqlDB.Close() })

	return New(sqlDB)
}

func TestCreateAndList_RoundTripsFeed(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	f, err := s.Create(ctx, "NZBGeek Saved Search", "https://nzbgeek.info/rss?t=1", TargetMovie, Usenet, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if f.ID == 0 {
		t.Fatal("expected a non-zero id")
	}
	if f.SortOrder != 0 {
		t.Errorf("expected first feed to get sort_order 0, got %d", f.SortOrder)
	}

	list, err := s.List(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(list) != 1 || list[0].Title != "NZBGeek Saved Search" || list[0].FeedURL != "https://nzbgeek.info/rss?t=1" {
		t.Errorf("unexpected list: %+v", list)
	}
}

func TestCreate_AppendsSortOrder(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	first, err := s.Create(ctx, "First", "https://example.com/rss1", TargetMovie, Usenet, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	second, err := s.Create(ctx, "Second", "https://example.com/rss2", TargetTV, Torrent, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if first.SortOrder != 0 || second.SortOrder != 1 {
		t.Errorf("expected sort_order 0 then 1, got %d then %d", first.SortOrder, second.SortOrder)
	}
}

func TestCreate_EmptyList_ReturnsEmptySliceNotNil(t *testing.T) {
	s := newTestStore(t)
	list, err := s.List(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if list == nil {
		t.Fatal("expected an empty slice, got nil")
	}
	if len(list) != 0 {
		t.Errorf("expected no feeds, got %+v", list)
	}
}

func TestCreate_RejectsBlankTitle(t *testing.T) {
	s := newTestStore(t)
	_, err := s.Create(context.Background(), "", "https://example.com/rss", TargetMovie, Usenet, true)
	if !errors.Is(err, ErrTitleRequired) {
		t.Fatalf("expected ErrTitleRequired, got %v", err)
	}
}

func TestCreate_RejectsBlankFeedURL(t *testing.T) {
	s := newTestStore(t)
	_, err := s.Create(context.Background(), "Title", "", TargetMovie, Usenet, true)
	if !errors.Is(err, ErrFeedURLRequired) {
		t.Fatalf("expected ErrFeedURLRequired, got %v", err)
	}
}

func TestCreate_RejectsUnknownTarget(t *testing.T) {
	s := newTestStore(t)
	_, err := s.Create(context.Background(), "Bogus", "https://example.com/rss", Target("bogus"), Usenet, true)
	if !errors.Is(err, ErrInvalidTarget) {
		t.Fatalf("expected ErrInvalidTarget, got %v", err)
	}
}

func TestCreate_RejectsUnknownProtocol(t *testing.T) {
	s := newTestStore(t)
	_, err := s.Create(context.Background(), "Bogus", "https://example.com/rss", TargetMovie, Protocol("bogus"), true)
	if !errors.Is(err, ErrInvalidProtocol) {
		t.Fatalf("expected ErrInvalidProtocol, got %v", err)
	}
}

func TestUpdate_OverwritesFieldsAndPreservesSortOrder(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	a, err := s.Create(ctx, "First", "https://example.com/rss1", TargetMovie, Usenet, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := s.Create(ctx, "Second", "https://example.com/rss2", TargetTV, Torrent, true); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	updated, err := s.Update(ctx, a.ID, "First Renamed", "https://example.com/rss1-new", TargetAdult, Torrent, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if updated.Title != "First Renamed" || updated.FeedURL != "https://example.com/rss1-new" || updated.Target != TargetAdult || updated.Protocol != Torrent || updated.Enabled {
		t.Errorf("unexpected updated feed: %+v", updated)
	}
	if updated.SortOrder != a.SortOrder {
		t.Errorf("expected Update to leave sort_order at %d, got %d", a.SortOrder, updated.SortOrder)
	}
}

func TestUpdate_NotFound(t *testing.T) {
	s := newTestStore(t)
	_, err := s.Update(context.Background(), 999, "X", "https://example.com/rss", TargetMovie, Usenet, true)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestUpdate_ValidatesLikeCreate(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	a, err := s.Create(ctx, "First", "https://example.com/rss1", TargetMovie, Usenet, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := s.Update(ctx, a.ID, "First", "", TargetMovie, Usenet, true); !errors.Is(err, ErrFeedURLRequired) {
		t.Fatalf("expected ErrFeedURLRequired, got %v", err)
	}
}

func TestDelete_RemovesFeed(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	a, err := s.Create(ctx, "First", "https://example.com/rss1", TargetMovie, Usenet, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := s.Delete(ctx, a.ID); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	list, err := s.List(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("expected no feeds after delete, got %+v", list)
	}
}

func TestDelete_NonExistentIDReturnsErrNotFound(t *testing.T) {
	s := newTestStore(t)
	if err := s.Delete(context.Background(), 999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound deleting a feed that never existed, got %v", err)
	}
}

func TestReorder_AppliesNewOrder(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	a, _ := s.Create(ctx, "A", "https://example.com/a", TargetMovie, Usenet, true)
	b, _ := s.Create(ctx, "B", "https://example.com/b", TargetTV, Usenet, true)
	c, _ := s.Create(ctx, "C", "https://example.com/c", TargetAdult, Usenet, true)

	if err := s.Reorder(ctx, []int{c.ID, a.ID, b.ID}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	list, err := s.List(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(list) != 3 || list[0].ID != c.ID || list[1].ID != a.ID || list[2].ID != b.ID {
		t.Errorf("unexpected order after reorder: %+v", list)
	}
	if list[0].SortOrder != 0 || list[1].SortOrder != 1 || list[2].SortOrder != 2 {
		t.Errorf("unexpected sort_order values after reorder: %+v", list)
	}
}

func TestReorder_RejectsPartialIDList(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	a, _ := s.Create(ctx, "A", "https://example.com/a", TargetMovie, Usenet, true)
	_, _ = s.Create(ctx, "B", "https://example.com/b", TargetTV, Usenet, true)

	if err := s.Reorder(ctx, []int{a.ID}); !errors.Is(err, ErrReorderMismatch) {
		t.Fatalf("expected ErrReorderMismatch, got %v", err)
	}
}

func TestReorder_RejectsUnknownID(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	a, _ := s.Create(ctx, "A", "https://example.com/a", TargetMovie, Usenet, true)

	if err := s.Reorder(ctx, []int{a.ID, 999}); !errors.Is(err, ErrReorderMismatch) {
		t.Fatalf("expected ErrReorderMismatch, got %v", err)
	}
}

func TestReorder_RejectsDuplicateID(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	a, _ := s.Create(ctx, "A", "https://example.com/a", TargetMovie, Usenet, true)
	b, _ := s.Create(ctx, "B", "https://example.com/b", TargetTV, Usenet, true)

	if err := s.Reorder(ctx, []int{a.ID, a.ID}); !errors.Is(err, ErrReorderMismatch) {
		t.Fatalf("expected ErrReorderMismatch for duplicate id, got %v", err)
	}
	_ = b
}
