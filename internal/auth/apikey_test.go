package auth

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"github.com/curtiswtaylorjr/sakms/internal/db"
)

// newTestStoreWithDB is newTestStore (auth_test.go) plus the raw *sql.DB
// handle, needed by tests that force a real settings-store read error (e.g.
// DROP TABLE settings) rather than exercising the happy path.
func newTestStoreWithDB(t *testing.T) (*Store, *sql.DB) {
	t.Helper()
	sqlDB, err := db.Open(filepath.Join(t.TempDir(), "sakms.db"))
	if err != nil {
		t.Fatalf("opening db: %v", err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	return newStoreFromDB(t, sqlDB), sqlDB
}

func TestEnsureAPIKey_GeneratesWhenEmpty(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	raw, err := s.EnsureAPIKey(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if raw == "" {
		t.Fatal("expected a non-empty raw key on first generation")
	}

	status, err := s.APIKeyStatus(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !status.HasKey || status.Source != "settings" {
		t.Fatalf("expected hasKey=true source=settings after generation, got %+v", status)
	}
}

func TestEnsureAPIKey_ReusesExisting(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	first, err := s.EnsureAPIKey(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if first == "" {
		t.Fatal("expected a raw key on first call")
	}
	statusBefore, err := s.APIKeyStatus(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	second, err := s.EnsureAPIKey(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if second != "" {
		t.Fatalf("expected reuse (empty raw) on second call, got %q", second)
	}
	statusAfter, err := s.APIKeyStatus(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if statusBefore.KeySuffix != statusAfter.KeySuffix {
		t.Fatalf("expected the same stored key across calls, suffixes differ: %q vs %q", statusBefore.KeySuffix, statusAfter.KeySuffix)
	}

	// The originally generated key must still verify.
	ok, err := s.VerifyAPIKey(ctx, first)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Error("expected the reused (unchanged) key to still verify")
	}
}

func TestUseEnvAPIKey_VerifiesAndTakesPrecedence(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// A different key persisted in settings first.
	settingsRaw, err := s.EnsureAPIKey(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if settingsRaw == "" {
		t.Fatal("expected a generated settings key")
	}

	s.UseEnvAPIKey("env-supplied-key-value")

	ok, err := s.VerifyAPIKey(ctx, "env-supplied-key-value")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Error("expected the env key to verify while active")
	}

	ok, err = s.VerifyAPIKey(ctx, settingsRaw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Error("expected the persisted settings key to NOT verify while the env key is active (env precedence)")
	}

	status, err := s.APIKeyStatus(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status.Source != "env" {
		t.Fatalf("expected status source=env, got %q", status.Source)
	}
}

func TestVerifyAPIKey_MatchAndMismatch(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	raw, err := s.EnsureAPIKey(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	ok, err := s.VerifyAPIKey(ctx, raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Error("expected the correct key to verify")
	}

	ok, err = s.VerifyAPIKey(ctx, raw+"x")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Error("expected a mismatched key to fail verification")
	}
}

func TestVerifyAPIKey_EmptyAndWhitespaceRejected(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if _, err := s.EnsureAPIKey(ctx); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, presented := range []string{"", "   ", "\t\n"} {
		ok, err := s.VerifyAPIKey(ctx, presented)
		if err != nil {
			t.Fatalf("unexpected error for %q: %v", presented, err)
		}
		if ok {
			t.Errorf("expected empty/whitespace presented key %q to be rejected, not treated as a false-pass", presented)
		}
	}
}

func TestVerifyAPIKey_NoKeyConfigured_False(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	ok, err := s.VerifyAPIKey(ctx, "some-presented-key")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Error("expected no-key-configured to never verify a presented key")
	}
}

func TestVerifyAPIKey_StoreReadError(t *testing.T) {
	s, sqlDB := newTestStoreWithDB(t)
	ctx := context.Background()

	if _, err := sqlDB.Exec(`DROP TABLE settings`); err != nil {
		t.Fatalf("dropping settings table: %v", err)
	}

	ok, err := s.VerifyAPIKey(ctx, "any-nonempty-presented-key")
	if err == nil {
		t.Fatal("expected a real settings-store error to propagate")
	}
	if ok {
		t.Error("expected false on a store error, never a match")
	}
}

func TestRegenerate_InvalidatesPreviousKey(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	oldRaw, err := s.EnsureAPIKey(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	newRaw, newSuffix, err := s.Regenerate(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if newRaw == "" || newRaw == oldRaw {
		t.Fatalf("expected a fresh, different raw key, got %q (old was %q)", newRaw, oldRaw)
	}
	if newSuffix != suffix(newRaw) {
		t.Errorf("expected Regenerate's returned suffix to match the new raw key's own suffix, got %q for raw %q", newSuffix, newRaw)
	}

	ok, err := s.VerifyAPIKey(ctx, oldRaw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Error("expected the old key to no longer verify after Regenerate")
	}

	ok, err = s.VerifyAPIKey(ctx, newRaw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Error("expected the new key to verify after Regenerate")
	}
}

func TestEnvKeyActive(t *testing.T) {
	s := newTestStore(t)
	if s.EnvKeyActive() {
		t.Error("expected EnvKeyActive to be false before UseEnvAPIKey is called")
	}

	s.UseEnvAPIKey("env-supplied-key-value")
	if !s.EnvKeyActive() {
		t.Error("expected EnvKeyActive to be true after UseEnvAPIKey")
	}
}

func TestRegenerate_EnvManaged_Refused(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	s.UseEnvAPIKey("env-key-value")

	_, _, err := s.Regenerate(ctx)
	if !errors.Is(err, ErrEnvManaged) {
		t.Fatalf("expected ErrEnvManaged, got %v", err)
	}
}

func TestNewRandomKey_FormatAndEntropy(t *testing.T) {
	a, err := newRandomKey()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	b, err := newRandomKey()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if a == b {
		t.Fatal("expected two independently generated keys to differ")
	}
	if len(a) < 32 {
		t.Fatalf("expected a reasonably long base64url key, got length %d", len(a))
	}
	for _, c := range a {
		if c == '+' || c == '/' || c == '=' {
			t.Fatalf("expected RawURLEncoding (no +, /, or = padding), got %q in %q", c, a)
		}
	}
}

// TestConstantTimeCompareUsed is a behavioral check that VerifyAPIKey uses
// subtle.ConstantTimeCompare correctly on equal-length hashes rather than a
// short-circuiting == — both a correct and an incorrect key of matching
// length must still resolve correctly, which a naive byte-slice == would
// also get right, so this is paired with the source-level guarantee that
// VerifyAPIKey's implementation calls subtle.ConstantTimeCompare (see
// apikey.go) rather than any private code-path assertion here.
func TestConstantTimeCompareUsed(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	raw, err := s.EnsureAPIKey(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Construct a same-length, different-content presented key so a
	// naive == on the raw hash bytes and ConstantTimeCompare would agree
	// on the (false) result — this test exists to pin the behavior, not
	// to distinguish the two implementations at runtime.
	wrong := make([]byte, len(raw))
	copy(wrong, raw)
	if wrong[0] == 'a' {
		wrong[0] = 'b'
	} else {
		wrong[0] = 'a'
	}

	ok, err := s.VerifyAPIKey(ctx, string(wrong))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Error("expected a same-length mismatched key to fail")
	}

	ok, err = s.VerifyAPIKey(ctx, raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Error("expected the correct key to still verify")
	}
}
