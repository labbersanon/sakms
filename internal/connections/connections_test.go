package connections

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/curtiswtaylorjr/sak/internal/db"
	"github.com/curtiswtaylorjr/sak/internal/secrets"
)

// newTestStore builds a Store against a real, freshly migrated SQLite file
// and a real secrets.Store — exercising the actual encryption + actual SQL,
// not mocks, the same way every other client in this repo is tested.
func newTestStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()

	sqlDB, err := db.Open(filepath.Join(dir, "sak.db"))
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

func TestUpsertAndGet_RoundTripsDecryptedKey(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if err := s.Upsert(ctx, "radarr", "http://192.168.1.12:7878", "my-secret-key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := s.Get(ctx, "radarr")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.URL != "http://192.168.1.12:7878" || got.APIKey != "my-secret-key" {
		t.Errorf("unexpected connection: %+v", got)
	}
}

func TestGet_NotConfigured(t *testing.T) {
	s := newTestStore(t)
	_, err := s.Get(context.Background(), "radarr")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestUpsert_ReplacesExistingConnection(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if err := s.Upsert(ctx, "radarr", "http://old:7878", "old-key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := s.Upsert(ctx, "radarr", "http://new:7878", "new-key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := s.Get(ctx, "radarr")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.URL != "http://new:7878" || got.APIKey != "new-key" {
		t.Errorf("expected the second Upsert to replace the first, got %+v", got)
	}
}

func TestUpsert_EmptyAPIKeyIsAllowed(t *testing.T) {
	// Ollama doesn't need a key by default — Upsert must not require one.
	s := newTestStore(t)
	ctx := context.Background()

	if err := s.Upsert(ctx, "ollama", "http://127.0.0.1:11434", ""); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got, err := s.Get(ctx, "ollama")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.APIKey != "" {
		t.Errorf("expected empty API key, got %q", got.APIKey)
	}
}

func TestList_RedactsKeysButShowsSuffixAndPresence(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if err := s.Upsert(ctx, "radarr", "http://radarr:7878", "abcd1234"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := s.Upsert(ctx, "ollama", "http://ollama:11434", ""); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	list, err := s.List(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("expected 2 connections, got %d: %+v", len(list), list)
	}

	byService := map[string]Summary{}
	for _, sum := range list {
		byService[sum.Service] = sum
		// Never leak the key itself into the redacted view.
		if sum.KeySuffix == "abcd1234" {
			t.Fatal("KeySuffix must not be the full key")
		}
	}

	radarr := byService["radarr"]
	if !radarr.HasAPIKey || radarr.KeySuffix != "1234" {
		t.Errorf("unexpected radarr summary: %+v", radarr)
	}
	ollama := byService["ollama"]
	if ollama.HasAPIKey || ollama.KeySuffix != "" {
		t.Errorf("expected ollama to report no key set, got %+v", ollama)
	}
}

func TestDelete_RemovesConnection(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if err := s.Upsert(ctx, "radarr", "http://radarr:7878", "key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := s.Delete(ctx, "radarr"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := s.Get(ctx, "radarr"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound after delete, got %v", err)
	}
}

func TestDelete_NonExistentServiceIsNotAnError(t *testing.T) {
	s := newTestStore(t)
	if err := s.Delete(context.Background(), "nonexistent"); err != nil {
		t.Fatalf("unexpected error deleting a service that was never configured: %v", err)
	}
}
