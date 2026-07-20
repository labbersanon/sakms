package allowlist

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/labbersanon/sakms/internal/db"
	"github.com/labbersanon/sakms/internal/mode"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	sqlDB, err := db.Open(filepath.Join(t.TempDir(), "sakms.db"))
	if err != nil {
		t.Fatalf("opening db: %v", err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	return New(sqlDB)
}

func TestAddAndList(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	for _, tag := range []string{"BDSM", "Bondage", "Dungeon"} {
		if err := s.Add(ctx, mode.Movies, tag); err != nil {
			t.Fatalf("unexpected error adding %q: %v", tag, err)
		}
	}

	got, err := s.List(ctx, mode.Movies)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"BDSM", "Bondage", "Dungeon"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("got %v, want %v", got, want)
			break
		}
	}
}

func TestAdd_DuplicateCaseInsensitiveIsNotAnError(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if err := s.Add(ctx, mode.Movies, "BDSM"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := s.Add(ctx, mode.Movies, "bdsm"); err != nil {
		t.Fatalf("unexpected error re-adding a different-case duplicate: %v", err)
	}

	got, err := s.List(ctx, mode.Movies)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected the duplicate to collapse to one entry, got %v", got)
	}
}

func TestRemove_CaseInsensitive(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if err := s.Add(ctx, mode.Movies, "BDSM"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := s.Remove(ctx, mode.Movies, "bdsm"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := s.List(ctx, mode.Movies)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected empty allowlist after removal, got %v", got)
	}
}

func TestRemove_NotPresentIsNotAnError(t *testing.T) {
	s := newTestStore(t)
	if err := s.Remove(context.Background(), mode.Movies, "nonexistent"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestList_ScopedByMode(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if err := s.Add(ctx, mode.Movies, "BDSM"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := s.Add(ctx, mode.Series, "Kids"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	movies, err := s.List(ctx, mode.Movies)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(movies) != 1 || movies[0] != "BDSM" {
		t.Fatalf("expected Movies allowlist to only contain its own tag, got %v", movies)
	}
}
