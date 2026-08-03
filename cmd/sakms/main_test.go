package main

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/labbersanon/sakms/internal/connections"
	"github.com/labbersanon/sakms/internal/db"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/secrets"
	"github.com/labbersanon/sakms/internal/settings"
)

// newTestStores builds a connections.Store and settings.Store against a
// real, freshly migrated SQLite file — same convention as every other
// package's tests in this repo (see internal/connections/connections_test.go).
func newTestStores(t *testing.T) (*connections.Store, *settings.Store) {
	t.Helper()
	dir := t.TempDir()

	sqlDB, err := db.Open(filepath.Join(dir, "sakms.db"))
	if err != nil {
		t.Fatalf("opening db: %v", err)
	}
	t.Cleanup(func() { sqlDB.Close() })

	secretStore, err := secrets.New(make([]byte, 32))
	if err != nil {
		t.Fatalf("building secret store: %v", err)
	}

	return connections.New(sqlDB, secretStore), settings.New(sqlDB)
}

func TestSeedBundledOllamaDefaults_BlankInstall(t *testing.T) {
	connStore, settingsStore := newTestStores(t)
	ctx := context.Background()

	if err := seedBundledOllamaDefaults(ctx, connStore, settingsStore, "qwen2.5:1.5b"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	conn, err := connStore.Get(ctx, "ollama")
	if err != nil {
		t.Fatalf("expected ollama connection to be seeded, got error: %v", err)
	}
	if conn.URL != "http://localhost:11434" {
		t.Errorf("URL = %q, want http://localhost:11434", conn.URL)
	}

	model, err := settingsStore.Get(ctx, mode.AIModelKey)
	if err != nil {
		t.Fatalf("expected ai_model to be seeded, got error: %v", err)
	}
	if model != "qwen2.5:1.5b" {
		t.Errorf("ai_model = %q, want qwen2.5:1.5b", model)
	}
}

func TestSeedBundledOllamaDefaults_DoesNotOverwriteExistingConnection(t *testing.T) {
	connStore, settingsStore := newTestStores(t)
	ctx := context.Background()

	// Operator already pointed "ollama" at some other host — e.g. an
	// external instance they were already running before switching to the
	// bundled image.
	if err := connStore.Upsert(ctx, "ollama", "http://10.1.10.10:8586", ""); err != nil {
		t.Fatalf("seeding pre-existing connection: %v", err)
	}

	if err := seedBundledOllamaDefaults(ctx, connStore, settingsStore, "qwen2.5:1.5b"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	conn, err := connStore.Get(ctx, "ollama")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if conn.URL != "http://10.1.10.10:8586" {
		t.Errorf("URL = %q, want the pre-existing http://10.1.10.10:8586 to survive untouched", conn.URL)
	}
}

func TestSeedBundledOllamaDefaults_DoesNotOverwriteExistingModel(t *testing.T) {
	connStore, settingsStore := newTestStores(t)
	ctx := context.Background()

	// Operator already picked a different model (or a different provider
	// entirely — buildAIClient only reads ai_model for whichever provider
	// is configured, so this is safe to seed unconditionally).
	if err := settingsStore.Set(ctx, mode.AIModelKey, "gpt-4o-mini"); err != nil {
		t.Fatalf("seeding pre-existing model setting: %v", err)
	}

	if err := seedBundledOllamaDefaults(ctx, connStore, settingsStore, "qwen2.5:1.5b"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	model, err := settingsStore.Get(ctx, mode.AIModelKey)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if model != "gpt-4o-mini" {
		t.Errorf("ai_model = %q, want the pre-existing gpt-4o-mini to survive untouched", model)
	}
}

// SL-22's environment half. The disarm itself is asserted end-to-end in
// internal/api's sectionlock_test.go; what can only be asserted here is that
// nothing OTHER than "1" arms it.
//
// The =0 and =false rows are the point of this test: the codebase's one
// existing boolean-ish env var (internal/tmdb/cache.go) is a bare `!= ""`,
// and copying that convention here would silently disarm a security control
// for an operator who explicitly wrote "off".
func TestSectionLockDisabledByEnv(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  bool
	}{
		{"1", true},
		{"", false},
		{"0", false},
		{"false", false},
		{"true", false},
		{"yes", false},
	} {
		t.Run("value="+tc.value, func(t *testing.T) {
			t.Setenv("SAKMS_SECTION_LOCK_DISABLE", tc.value)
			if got := sectionLockDisabledByEnv(); got != tc.want {
				t.Fatalf("SAKMS_SECTION_LOCK_DISABLE=%q disarmed=%v, want %v", tc.value, got, tc.want)
			}
		})
	}
}
