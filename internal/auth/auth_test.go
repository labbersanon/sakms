package auth

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/curtiswtaylorjr/sak/internal/db"
	"github.com/curtiswtaylorjr/sak/internal/settings"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	sqlDB, err := db.Open(filepath.Join(t.TempDir(), "sak.db"))
	if err != nil {
		t.Fatalf("opening db: %v", err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	return New(settings.New(sqlDB))
}

func TestConfigured_FalseBeforeAnyCredentialsSet(t *testing.T) {
	s := newTestStore(t)
	configured, err := s.Configured(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if configured {
		t.Error("expected Configured to be false on a fresh store")
	}
}

func TestSetCredentials_ThenVerifySucceeds(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.SetCredentials(ctx, "wade", "correct-horse-battery-staple"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	configured, err := s.Configured(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !configured {
		t.Error("expected Configured to be true after SetCredentials")
	}

	ok, err := s.Verify(ctx, "wade", "correct-horse-battery-staple")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Error("expected Verify to succeed with the correct username/password")
	}
}

func TestVerify_WrongPasswordFails(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.SetCredentials(ctx, "wade", "the-real-password"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	ok, err := s.Verify(ctx, "wade", "not-the-password")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Error("expected Verify to fail with the wrong password")
	}
}

func TestVerify_WrongUsernameFails(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.SetCredentials(ctx, "wade", "the-real-password"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	ok, err := s.Verify(ctx, "someone-else", "the-real-password")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Error("expected Verify to fail with the wrong username")
	}
}

func TestVerify_NotConfiguredYet(t *testing.T) {
	s := newTestStore(t)
	_, err := s.Verify(context.Background(), "wade", "anything")
	if !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("expected ErrNotConfigured, got %v", err)
	}
}

func TestSetCredentials_RejectsBlankUsernameOrPassword(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.SetCredentials(ctx, "", "password"); err == nil {
		t.Error("expected an error for a blank username")
	}
	if err := s.SetCredentials(ctx, "wade", ""); err == nil {
		t.Error("expected an error for a blank password")
	}
}

func TestSetCredentials_CanReplaceExistingLogin(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.SetCredentials(ctx, "wade", "first-password"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := s.SetCredentials(ctx, "wade", "second-password"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	ok, err := s.Verify(ctx, "wade", "second-password")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Error("expected the replaced password to verify")
	}
	ok, err = s.Verify(ctx, "wade", "first-password")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Error("expected the old password to no longer verify")
	}
}
