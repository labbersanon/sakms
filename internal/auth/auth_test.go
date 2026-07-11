package auth

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/curtiswtaylorjr/sakms/internal/db"
	"github.com/curtiswtaylorjr/sakms/internal/settings"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	sqlDB, err := db.Open(filepath.Join(t.TempDir(), "sakms.db"))
	if err != nil {
		t.Fatalf("opening db: %v", err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	return newStoreFromDB(t, sqlDB)
}

// newStoreFromDB builds a Store around an already-open sqlDB, with a
// deterministic test encryptor (testEncryptor, session_test.go — the same
// all-zero-key secrets.Store used everywhere else in this package's tests,
// so a secret encrypted via testEncryptor(t) in one test and stored via
// SetOIDCConfig decrypts correctly through this Store's own internal enc
// field) and a plain default HTTP client. Shared by newTestStore here and
// apikey_test.go's newTestStoreWithDB.
func newStoreFromDB(t *testing.T, sqlDB *sql.DB) *Store {
	t.Helper()
	return New(settings.New(sqlDB), testEncryptor(t), http.DefaultClient)
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

// --- auth_mode: effective-mode getter + migration-safe Configured ---

func TestAuthMode_UnsetDefaultsPassword(t *testing.T) {
	s := newTestStore(t)
	mode, err := s.AuthMode(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mode != ModePassword {
		t.Errorf("expected AuthMode to default to %q on a fresh store, got %q", ModePassword, mode)
	}
}

func TestAuthMode_ReturnsWhateverWasSet(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.SetAuthMode(ctx, ModeNone); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	mode, err := s.AuthMode(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mode != ModeNone {
		t.Errorf("expected AuthMode to return %q, got %q", ModeNone, mode)
	}
}

func TestConfigured_TrueAfterModeSetOnly(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.SetAuthMode(ctx, ModeNone); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	configured, err := s.Configured(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !configured {
		t.Error("expected Configured to be true once auth_mode is set, even with no auth_username")
	}
}

// TestConfigured_ExistingUsernameOnlyInstall_StillTrue is the instance-
// takeover regression test the plan (§0.1) specifically calls out: every
// pre-existing install has auth_username set but no auth_mode row (that
// setting didn't exist before this slice). If Configured were redefined as
// "auth_mode is set" ALONE (instead of the OR with auth_username), this
// install would report Configured=false — re-showing "Create your login"
// and, worse, letting the setup handler's 409 guard stop firing so an
// unauthenticated visitor could re-POST /api/auth/setup and overwrite the
// owner's credentials. This test simulates that install shape directly
// (SetCredentials only, auth_mode never written) and asserts Configured
// still reports true, and AuthMode still reports "password".
func TestConfigured_ExistingUsernameOnlyInstall_StillTrue(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.SetCredentials(ctx, "wade", "the-real-password"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Deliberately never call SetAuthMode — this is the exact shape of an
	// install that existed before auth_mode was introduced.

	configured, err := s.Configured(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !configured {
		t.Fatal("instance-takeover regression: an existing username-only install must still report Configured=true")
	}

	mode, err := s.AuthMode(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mode != ModePassword {
		t.Errorf("expected an existing username-only install to default to %q, got %q", ModePassword, mode)
	}
}

func TestPasswordConfigured_FalseBeforeSetCredentials(t *testing.T) {
	s := newTestStore(t)
	ok, err := s.PasswordConfigured(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Error("expected PasswordConfigured to be false on a fresh store")
	}
}

func TestPasswordConfigured_TrueAfterSetCredentials(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.SetCredentials(ctx, "wade", "the-real-password"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	ok, err := s.PasswordConfigured(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Error("expected PasswordConfigured to be true after SetCredentials")
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
