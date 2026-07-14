package trakt

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/curtiswtaylorjr/sakms/internal/db"
	"github.com/curtiswtaylorjr/sakms/internal/secrets"
)

// newTestStore builds a Store against a real, freshly migrated SQLite file
// and a real secrets.Store — same convention as connections_test.go:
// exercise actual encryption and actual SQL, not mocks.
func newTestStore(t *testing.T) *Store {
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

	return NewStore(sqlDB, secretStore)
}

func TestGet_NotConfigured(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	_, err := s.Get(ctx)
	if !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("expected ErrNotConfigured, got %v", err)
	}

	configured, err := s.Configured(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if configured {
		t.Error("expected Configured() to be false before any credentials are saved")
	}
}

func TestSaveCredentials_RoundTripsDecrypted(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	secret := "shh-its-a-secret"
	if err := s.SaveCredentials(ctx, "client-abc", &secret); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	conn, err := s.Get(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if conn.ClientID != "client-abc" || conn.ClientSecret != secret {
		t.Errorf("unexpected connection: %+v", conn)
	}
	if conn.Tokens.Linked() {
		t.Error("expected no tokens linked yet")
	}

	configured, err := s.Configured(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !configured {
		t.Error("expected Configured() to be true after saving credentials")
	}
}

func TestSaveCredentials_NilPreservesExistingSecret(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	secret := "original-secret"
	if err := s.SaveCredentials(ctx, "client-abc", &secret); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Simulate re-saving after editing only client_id in Settings: the form
	// never receives the real secret back, so it must send nil, not "".
	if err := s.SaveCredentials(ctx, "client-def", nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	conn, err := s.Get(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if conn.ClientID != "client-def" {
		t.Errorf("expected client_id to update to client-def, got %q", conn.ClientID)
	}
	if conn.ClientSecret != "original-secret" {
		t.Errorf("expected original secret preserved, got %q", conn.ClientSecret)
	}
}

func TestSaveCredentials_EmptyStringClearsSecret(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	secret := "original-secret"
	if err := s.SaveCredentials(ctx, "client-abc", &secret); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	cleared := ""
	if err := s.SaveCredentials(ctx, "client-abc", &cleared); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	conn, err := s.Get(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if conn.ClientSecret != "" {
		t.Errorf("expected secret cleared, got %q", conn.ClientSecret)
	}
}

func TestSaveTokens_RoundTripsAndReportsLinked(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	secret := "secret"
	if err := s.SaveCredentials(ctx, "client-abc", &secret); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expiresAt := time.Now().Add(90 * 24 * time.Hour).Truncate(time.Second).UTC()
	if err := s.SaveTokens(ctx, "access-tok", "refresh-tok", expiresAt); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	conn, err := s.Get(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if conn.AccessToken != "access-tok" || conn.RefreshToken != "refresh-tok" {
		t.Errorf("unexpected tokens: %+v", conn.Tokens)
	}
	if !conn.ExpiresAt.Equal(expiresAt) {
		t.Errorf("expected ExpiresAt %v, got %v", expiresAt, conn.ExpiresAt)
	}
	if !conn.Tokens.Linked() {
		t.Error("expected Linked() true after SaveTokens")
	}
	// Credentials must survive a token save untouched.
	if conn.ClientID != "client-abc" || conn.ClientSecret != "secret" {
		t.Errorf("expected credentials preserved, got %+v", conn.Credentials)
	}
}

func TestSaveTokens_WithoutCredentialsReturnsErrNotConfigured(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	err := s.SaveTokens(ctx, "access-tok", "refresh-tok", time.Now())
	if !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("expected ErrNotConfigured, got %v", err)
	}
}

func TestClearTokens_LeavesCredentialsIntact(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	secret := "secret"
	if err := s.SaveCredentials(ctx, "client-abc", &secret); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := s.SaveTokens(ctx, "access-tok", "refresh-tok", time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if err := s.ClearTokens(ctx); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	conn, err := s.Get(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if conn.Tokens.Linked() {
		t.Error("expected tokens cleared")
	}
	if conn.ClientID != "client-abc" || conn.ClientSecret != "secret" {
		t.Errorf("expected credentials preserved, got %+v", conn.Credentials)
	}
}

func TestDelete_RemovesEverything(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	secret := "secret"
	if err := s.SaveCredentials(ctx, "client-abc", &secret); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if err := s.Delete(ctx); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	_, err := s.Get(ctx)
	if !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("expected ErrNotConfigured after delete, got %v", err)
	}

	// Deleting again (nothing configured) is not an error.
	if err := s.Delete(ctx); err != nil {
		t.Errorf("expected deleting an already-empty connection to be a no-op, got %v", err)
	}
}
