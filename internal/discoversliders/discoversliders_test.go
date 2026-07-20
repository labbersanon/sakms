package discoversliders

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/labbersanon/sakms/internal/db"
)

// newTestStore builds a Store against a real, freshly migrated SQLite file —
// exercising the actual schema/migration, not mocks, the same way every
// other store in this repo is tested (see connections_test.go).
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

func TestCreateAndList_RoundTripsSlider(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	sl, err := s.Create(ctx, "Action Movies", FilterGenre, "28", TargetMovie, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sl.ID == 0 {
		t.Fatal("expected a non-zero id")
	}
	if sl.SortOrder != 0 {
		t.Errorf("expected first slider to get sort_order 0, got %d", sl.SortOrder)
	}

	list, err := s.List(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(list) != 1 || list[0].Title != "Action Movies" || list[0].FilterValue != "28" {
		t.Errorf("unexpected list: %+v", list)
	}
}

func TestCreate_AppendsSortOrder(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	first, err := s.Create(ctx, "Trending", FilterTrending, "", TargetMixed, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	second, err := s.Create(ctx, "Netflix", FilterNetwork, "213", TargetTV, true)
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
		t.Errorf("expected no sliders, got %+v", list)
	}
}

func TestCreate_RejectsBlankTitle(t *testing.T) {
	s := newTestStore(t)
	_, err := s.Create(context.Background(), "", FilterTrending, "", TargetMixed, true)
	if !errors.Is(err, ErrTitleRequired) {
		t.Fatalf("expected ErrTitleRequired, got %v", err)
	}
}

func TestCreate_RejectsUnknownFilterType(t *testing.T) {
	s := newTestStore(t)
	_, err := s.Create(context.Background(), "Bogus", FilterType("bogus"), "1", TargetMovie, true)
	if !errors.Is(err, ErrInvalidFilterType) {
		t.Fatalf("expected ErrInvalidFilterType, got %v", err)
	}
}

func TestCreate_RejectsUnknownTarget(t *testing.T) {
	s := newTestStore(t)
	_, err := s.Create(context.Background(), "Bogus", FilterGenre, "28", Target("bogus"), true)
	if !errors.Is(err, ErrInvalidTarget) {
		t.Fatalf("expected ErrInvalidTarget, got %v", err)
	}
}

func TestCreate_RejectsMissingFilterValueForIDBasedFilters(t *testing.T) {
	s := newTestStore(t)
	for _, ft := range []FilterType{FilterGenre, FilterKeyword, FilterStudio, FilterNetwork} {
		_, err := s.Create(context.Background(), "Missing value", ft, "", TargetMovie, true)
		if !errors.Is(err, ErrFilterValueRequired) {
			t.Errorf("filter type %q: expected ErrFilterValueRequired, got %v", ft, err)
		}
	}
}

func TestCreate_RejectsFilterValueForFixedFeeds(t *testing.T) {
	s := newTestStore(t)
	for _, ft := range []FilterType{FilterUpcoming, FilterTrending, FilterPopular} {
		_, err := s.Create(context.Background(), "Unwanted value", ft, "123", TargetMovie, true)
		if !errors.Is(err, ErrFilterValueNotAllowed) {
			t.Errorf("filter type %q: expected ErrFilterValueNotAllowed, got %v", ft, err)
		}
	}
}

func TestUpdate_OverwritesFieldsAndPreservesSortOrder(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	a, err := s.Create(ctx, "First", FilterTrending, "", TargetMixed, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := s.Create(ctx, "Second", FilterPopular, "", TargetMixed, true); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	updated, err := s.Update(ctx, a.ID, "First Renamed", FilterGenre, "35", TargetTV, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if updated.Title != "First Renamed" || updated.FilterType != FilterGenre || updated.FilterValue != "35" || updated.Target != TargetTV || updated.Enabled {
		t.Errorf("unexpected updated slider: %+v", updated)
	}
	if updated.SortOrder != a.SortOrder {
		t.Errorf("expected Update to leave sort_order at %d, got %d", a.SortOrder, updated.SortOrder)
	}
}

func TestUpdate_NotFound(t *testing.T) {
	s := newTestStore(t)
	_, err := s.Update(context.Background(), 999, "X", FilterTrending, "", TargetMixed, true)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestUpdate_ValidatesLikeCreate(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	a, err := s.Create(ctx, "First", FilterTrending, "", TargetMixed, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := s.Update(ctx, a.ID, "First", FilterGenre, "", TargetMixed, true); !errors.Is(err, ErrFilterValueRequired) {
		t.Fatalf("expected ErrFilterValueRequired, got %v", err)
	}
}

func TestDelete_RemovesSlider(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	a, err := s.Create(ctx, "First", FilterTrending, "", TargetMixed, true)
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
		t.Errorf("expected no sliders after delete, got %+v", list)
	}
}

func TestDelete_NonExistentIDIsNotAnError(t *testing.T) {
	s := newTestStore(t)
	if err := s.Delete(context.Background(), 999); err != nil {
		t.Fatalf("unexpected error deleting a slider that never existed: %v", err)
	}
}

func TestReorder_AppliesNewOrder(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	a, _ := s.Create(ctx, "A", FilterTrending, "", TargetMixed, true)
	b, _ := s.Create(ctx, "B", FilterPopular, "", TargetMixed, true)
	c, _ := s.Create(ctx, "C", FilterUpcoming, "", TargetMixed, true)

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

	a, _ := s.Create(ctx, "A", FilterTrending, "", TargetMixed, true)
	_, _ = s.Create(ctx, "B", FilterPopular, "", TargetMixed, true)

	if err := s.Reorder(ctx, []int{a.ID}); !errors.Is(err, ErrReorderMismatch) {
		t.Fatalf("expected ErrReorderMismatch, got %v", err)
	}
}

func TestReorder_RejectsUnknownID(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	a, _ := s.Create(ctx, "A", FilterTrending, "", TargetMixed, true)

	if err := s.Reorder(ctx, []int{a.ID, 999}); !errors.Is(err, ErrReorderMismatch) {
		t.Fatalf("expected ErrReorderMismatch, got %v", err)
	}
}

func TestReorder_RejectsDuplicateID(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	a, _ := s.Create(ctx, "A", FilterTrending, "", TargetMixed, true)
	b, _ := s.Create(ctx, "B", FilterPopular, "", TargetMixed, true)

	if err := s.Reorder(ctx, []int{a.ID, a.ID}); !errors.Is(err, ErrReorderMismatch) {
		t.Fatalf("expected ErrReorderMismatch for duplicate id, got %v", err)
	}
	_ = b
}
