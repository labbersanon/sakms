package settings

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/labbersanon/sakms/internal/db"
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

func TestGet_NotFound(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.Get(context.Background(), "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestSetAndGet_RoundTrips(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.Set(ctx, "greeting", "hello"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got, err := s.Get(ctx, "greeting")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "hello" {
		t.Errorf("got %q, want %q", got, "hello")
	}
}

func TestSet_OverwritesExistingValue(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	s.Set(ctx, "k", "v1")
	if err := s.Set(ctx, "k", "v2"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got, _ := s.Get(ctx, "k")
	if got != "v2" {
		t.Errorf("got %q, want %q", got, "v2")
	}
}

func TestGetBool_DefaultWhenUnset(t *testing.T) {
	s := newTestStore(t)
	got, err := s.GetBool(context.Background(), "flag", true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got {
		t.Error("expected the default value (true) when unset")
	}
}

func TestSetBoolAndGetBool_RoundTrips(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.SetBool(ctx, "flag", true); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got, err := s.GetBool(ctx, "flag", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got {
		t.Error("expected true")
	}

	if err := s.SetBool(ctx, "flag", false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got, err = s.GetBool(ctx, "flag", true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got {
		t.Error("expected false")
	}
}
