package proposals

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/curtiswtaylorjr/tidyarr/internal/db"
	"github.com/curtiswtaylorjr/tidyarr/internal/mode"
)

// newTestStore builds a Store against a real, freshly migrated SQLite file —
// exercising the actual SQL, not a mock, matching every other store test in
// this repo.
func newTestStore(t *testing.T) *Store {
	t.Helper()
	sqlDB, err := db.Open(filepath.Join(t.TempDir(), "tidyarr.db"))
	if err != nil {
		t.Fatalf("opening db: %v", err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	return New(sqlDB)
}

func TestReplacePending_InsertsAndAssignsIDs(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	saved, err := s.ReplacePending(ctx, mode.Movies, Rename, []Proposal{
		{Status: Pending, SourceName: "Movie A", SourcePath: "/media/Movies/Movie A", RootFolderPath: "/media/Movies", Title: "Movie A", TMDBID: 1},
		{Status: Unmatched, SourceName: "gibberish", SourcePath: "/media/Movies/gibberish", RootFolderPath: "/media/Movies", Reason: "no match"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(saved) != 2 {
		t.Fatalf("expected 2 saved proposals, got %d", len(saved))
	}
	for _, p := range saved {
		if p.ID == 0 {
			t.Errorf("expected a real assigned ID, got 0: %+v", p)
		}
		if p.CreatedAt == "" {
			t.Errorf("expected CreatedAt to be populated: %+v", p)
		}
		if p.Mode != mode.Movies || p.Workflow != Rename {
			t.Errorf("expected mode/workflow to be stamped on the saved row: %+v", p)
		}
	}
}

func TestReplacePending_LeavesAppliedAndDismissedAlone(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	first, err := s.ReplacePending(ctx, mode.Movies, Rename, []Proposal{
		{Status: Pending, SourceName: "Movie A", SourcePath: "/a", RootFolderPath: "/media/Movies", Title: "Movie A"},
		{Status: Pending, SourceName: "Movie B", SourcePath: "/b", RootFolderPath: "/media/Movies", Title: "Movie B"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := s.MarkApplied(ctx, first[0].ID, 42); err != nil {
		t.Fatalf("marking applied: %v", err)
	}
	if err := s.Dismiss(ctx, first[1].ID); err != nil {
		t.Fatalf("dismissing: %v", err)
	}

	// A fresh Scan for the same mode/workflow must not touch the two rows
	// already resolved above.
	if _, err := s.ReplacePending(ctx, mode.Movies, Rename, []Proposal{
		{Status: Pending, SourceName: "Movie C", SourcePath: "/c", RootFolderPath: "/media/Movies", Title: "Movie C"},
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	all, err := s.List(ctx, mode.Movies, Rename)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("expected 3 rows (1 applied, 1 dismissed, 1 fresh pending), got %d: %+v", len(all), all)
	}

	applied, err := s.Get(ctx, first[0].ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if applied.Status != Applied || applied.TrackedID != 42 {
		t.Errorf("expected applied row to survive unchanged, got %+v", applied)
	}
	dismissed, err := s.Get(ctx, first[1].ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dismissed.Status != Dismissed {
		t.Errorf("expected dismissed row to survive unchanged, got %+v", dismissed)
	}
}

func TestReplacePending_ScopedByModeAndWorkflow(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if _, err := s.ReplacePending(ctx, mode.Movies, Rename, []Proposal{
		{Status: Pending, SourceName: "Movie A", SourcePath: "/a", RootFolderPath: "/media/Movies"},
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := s.ReplacePending(ctx, mode.Series, Rename, []Proposal{
		{Status: Pending, SourceName: "Show A", SourcePath: "/b", RootFolderPath: "/media/Series"},
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	movies, err := s.List(ctx, mode.Movies, Rename)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(movies) != 1 || movies[0].SourceName != "Movie A" {
		t.Fatalf("expected Movies queue to only contain its own proposal, got %+v", movies)
	}

	series, err := s.List(ctx, mode.Series, Rename)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(series) != 1 || series[0].SourceName != "Show A" {
		t.Fatalf("expected Series queue to only contain its own proposal, got %+v", series)
	}
}

func TestGet_NotFound(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.Get(context.Background(), 999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestMarkApplied_NotFound(t *testing.T) {
	s := newTestStore(t)
	if err := s.MarkApplied(context.Background(), 999, 1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestDismiss_NotFound(t *testing.T) {
	s := newTestStore(t)
	if err := s.Dismiss(context.Background(), 999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}
