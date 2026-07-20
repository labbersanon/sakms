package adultnewest

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/labbersanon/sakms/internal/db"
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

func TestMigration_SeedsFourDefaultRows(t *testing.T) {
	s := newTestStore(t)
	list, err := s.List(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(list) != 4 {
		t.Fatalf("expected 4 seeded default rows, got %d: %+v", len(list), list)
	}
	wantTypes := []RowType{RowMovie, RowScene, RowPerformer, RowStudio}
	for i, want := range wantTypes {
		if list[i].RowType != want {
			t.Errorf("row %d: expected type %q, got %q", i, want, list[i].RowType)
		}
		if list[i].GenreFilter != "" {
			t.Errorf("row %d: expected no genre filter on default row, got %q", i, list[i].GenreFilter)
		}
		if !list[i].Enabled {
			t.Errorf("row %d: expected default row to be enabled", i)
		}
	}
}

func TestCreateAndList_RoundTripsRow(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	r, err := s.Create(ctx, "Anal Scenes", RowScene, "Anal", true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.ID == 0 {
		t.Fatal("expected a non-zero id")
	}
	if r.SortOrder != 4 {
		t.Errorf("expected new row to append after the 4 seeded rows (sort_order 4), got %d", r.SortOrder)
	}

	list, err := s.List(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(list) != 5 || list[4].Title != "Anal Scenes" || list[4].GenreFilter != "Anal" {
		t.Errorf("unexpected list: %+v", list)
	}
}

func TestCreate_InvalidRowType(t *testing.T) {
	s := newTestStore(t)
	_, err := s.Create(context.Background(), "Bogus", RowType("bogus"), "", true)
	if !errors.Is(err, ErrInvalidRowType) {
		t.Fatalf("expected ErrInvalidRowType, got %v", err)
	}
}

func TestCreate_TitleRequired(t *testing.T) {
	s := newTestStore(t)
	_, err := s.Create(context.Background(), "", RowScene, "", true)
	if !errors.Is(err, ErrTitleRequired) {
		t.Fatalf("expected ErrTitleRequired, got %v", err)
	}
}

func TestCreate_EmptyGenreFilterIsAlwaysValid(t *testing.T) {
	s := newTestStore(t)
	// Every RowType, with and without a genre filter, must validate — unlike
	// discoversliders there is no required/forbidden pairing rule.
	for _, rt := range []RowType{RowMovie, RowScene, RowPerformer, RowStudio} {
		if _, err := s.Create(context.Background(), string(rt)+" no genre", rt, "", true); err != nil {
			t.Errorf("row type %q with no genre filter: unexpected error: %v", rt, err)
		}
		if _, err := s.Create(context.Background(), string(rt)+" with genre", rt, "Some Genre", true); err != nil {
			t.Errorf("row type %q with genre filter: unexpected error: %v", rt, err)
		}
	}
}

func TestUpdate_NotFound(t *testing.T) {
	s := newTestStore(t)
	_, err := s.Update(context.Background(), 9999, "Whatever", RowScene, "", true)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestUpdate_PreservesSortOrder(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	list, err := s.List(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	sceneRow := list[1] // "Newest Scenes", seeded at sort_order 1

	updated, err := s.Update(ctx, sceneRow.ID, "Renamed Scenes", RowScene, "Anal", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if updated.SortOrder != sceneRow.SortOrder {
		t.Errorf("expected sort_order unchanged by Update, was %d now %d", sceneRow.SortOrder, updated.SortOrder)
	}
	if updated.Title != "Renamed Scenes" || updated.GenreFilter != "Anal" || updated.Enabled {
		t.Errorf("unexpected updated row: %+v", updated)
	}
}

func TestDelete_NonexistentIDIsNotAnError(t *testing.T) {
	s := newTestStore(t)
	if err := s.Delete(context.Background(), 9999); err != nil {
		t.Fatalf("expected no error deleting a nonexistent id, got %v", err)
	}
}

func TestReorder_RoundTrips(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	list, err := s.List(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Reverse the 4 seeded rows.
	ids := []int{list[3].ID, list[2].ID, list[1].ID, list[0].ID}
	if err := s.Reorder(ctx, ids); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	reordered, err := s.List(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for i, id := range ids {
		if reordered[i].ID != id {
			t.Errorf("position %d: expected id %d, got %d", i, id, reordered[i].ID)
		}
		if reordered[i].SortOrder != i {
			t.Errorf("position %d: expected sort_order %d, got %d", i, i, reordered[i].SortOrder)
		}
	}
}

func TestReorder_MismatchedIDsRejected(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	list, err := s.List(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Missing one id.
	if err := s.Reorder(ctx, []int{list[0].ID, list[1].ID, list[2].ID}); !errors.Is(err, ErrReorderMismatch) {
		t.Errorf("expected ErrReorderMismatch for a short id list, got %v", err)
	}
	// Duplicate id.
	if err := s.Reorder(ctx, []int{list[0].ID, list[0].ID, list[1].ID, list[2].ID}); !errors.Is(err, ErrReorderMismatch) {
		t.Errorf("expected ErrReorderMismatch for a duplicate id, got %v", err)
	}
	// Unknown id.
	if err := s.Reorder(ctx, []int{9999, list[1].ID, list[2].ID, list[3].ID}); !errors.Is(err, ErrReorderMismatch) {
		t.Errorf("expected ErrReorderMismatch for an unknown id, got %v", err)
	}
}
