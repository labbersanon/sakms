package excludes_test

import (
	"context"
	"testing"

	"github.com/labbersanon/sakms/internal/dbtest"
	"github.com/labbersanon/sakms/internal/excludes"
)

func newStore(t *testing.T) *excludes.Store {
	t.Helper()
	sqlDB := dbtest.New(t)
	return excludes.New(sqlDB)
}

func TestAddPersistsAndKeys(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	if err := s.Add(ctx, "movies", 42, "Some Movie"); err != nil {
		t.Fatalf("add movie: %v", err)
	}
	if err := s.Add(ctx, "adult", 0, "Some Scene"); err != nil {
		t.Fatalf("add scene: %v", err)
	}

	keys, err := s.Keys(ctx)
	if err != nil {
		t.Fatalf("keys: %v", err)
	}
	if !keys[excludes.Key("movies", 42, "Some Movie")] {
		t.Errorf("movie key missing: %+v", keys)
	}
	if !keys[excludes.Key("adult", 0, "Some Scene")] {
		t.Errorf("scene key missing: %+v", keys)
	}
	// Movie keyed by id ignores title; scene keyed by lowercased title.
	if excludes.Key("movies", 42, "Whatever") != excludes.Key("movies", 42, "Some Movie") {
		t.Errorf("tmdb-keyed exclusions must ignore title")
	}
	if excludes.Key("adult", 0, "Some Scene") != excludes.Key("adult", 0, "  some scene  ") {
		t.Errorf("title key must be trimmed+lowercased")
	}
}

func TestAddIdempotent(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if err := s.Add(ctx, "movies", 1, "Dup"); err != nil {
			t.Fatalf("add %d: %v", i, err)
		}
	}
	keys, err := s.Keys(ctx)
	if err != nil {
		t.Fatalf("keys: %v", err)
	}
	if len(keys) != 1 {
		t.Errorf("re-excluding the same title must be idempotent, got %d rows", len(keys))
	}
}

func TestAddInvalid(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	cases := []struct {
		mode   string
		tmdbID int
		title  string
	}{
		{"", 5, "x"},      // no mode
		{"movies", 0, ""}, // no id, no title
		{"adult", 0, "   "},
	}
	for _, c := range cases {
		if err := s.Add(ctx, c.mode, c.tmdbID, c.title); err != excludes.ErrInvalid {
			t.Errorf("Add(%q,%d,%q) = %v, want ErrInvalid", c.mode, c.tmdbID, c.title, err)
		}
	}
	keys, _ := s.Keys(ctx)
	if len(keys) != 0 {
		t.Errorf("no invalid exclusion should persist, got %d", len(keys))
	}
}
