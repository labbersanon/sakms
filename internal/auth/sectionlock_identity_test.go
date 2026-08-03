package auth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labbersanon/sakms/internal/secrets"
	"github.com/labbersanon/sakms/internal/sectionlock"
)

// sectionlock_identity_test.go — the brute-force lockout must key on the
// credential that ACTUALLY authenticated, never on a raw request value.
//
// # The bug this file exists to prevent from returning
//
// sectionlock.Identity used to fingerprint the sakms_session cookie straight
// off the request. Middleware checks X-Api-Key FIRST and skips the cookie check
// entirely when the key passes — so a caller holding a valid API key could
// attach a fresh random sakms_session to every PIN guess, land in a brand-new
// bucket every time, and never accumulate five failures anywhere. That defeated
// the entire brute-force control: a 6-digit PIN went from lockout-bounded
// (decades) to bcrypt-bounded (about an hour).
//
// MUTATION-CHECKED 2026-08-03: sectionlock.Identity was temporarily reverted to
// the cookie-first derivation and this file was re-run. Observed failure:
//
//	--- FAIL: TestLockoutSurvivesRotatingSessionCookie
//	    after 5 wrong PINs (each with a fresh random session cookie) the
//	    CORRECT PIN was accepted with status 200
//
// So this test genuinely discriminates; it does not merely pass alongside the
// fix. TestLockoutTripsOnAStableCredential passes under BOTH implementations by
// design — it is the control that proves the failure above is caused by the
// rotation and not by the PIN simply being wrong.

// An attacker with a valid API key who cycles a fresh random session cookie on
// every PIN guess must STILL be locked out after failureThreshold failures.
//
// The lockout is asserted BEHAVIOURALLY, with no test-only accessor: after the
// wrong guesses, the CORRECT PIN is presented on the same credential. A working
// lockout refuses it (403) because VerifyPin checks the refusal window before
// it checks the PIN; a bypassed lockout lets it straight through (200).
func TestLockoutSurvivesRotatingSessionCookie(t *testing.T) {
	enc := testEncryptor(t)
	handler := lockoutTestHandler(t, enc)

	guess := func(pin string) int {
		req := httptest.NewRequest(http.MethodGet, "/api/modes/adult/discover", nil)
		req.RemoteAddr = "10.1.10.55:4444"
		req.Header.Set("X-Api-Key", testAPIKey)
		req.Header.Set(sectionlock.HeaderName, pin)
		// The bypass: a brand-new, never-validated cookie value every time.
		req.AddCookie(&http.Cookie{Name: CookieName, Value: randomCookieValue(t)})
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec.Code
	}

	for i := 0; i < failureThresholdForTest; i++ {
		if got := guess("000000"); got != http.StatusForbidden {
			t.Fatalf("wrong PIN attempt %d: status = %d, want %d", i+1, got, http.StatusForbidden)
		}
	}

	if got := guess(lockoutTestPin); got != http.StatusForbidden {
		t.Fatalf("after %d wrong PINs (each with a fresh random session cookie) the CORRECT "+
			"PIN was accepted with status %d — the lockout never accumulated, because the "+
			"counter is keyed on attacker-controlled cookie noise instead of the API key "+
			"that actually authenticated", failureThresholdForTest, got)
	}
}

// The control for the test above: a caller who does NOT rotate anything is
// locked out the same way, so the assertion above is about the rotation and not
// about the PIN simply being wrong.
func TestLockoutTripsOnAStableCredential(t *testing.T) {
	enc := testEncryptor(t)
	handler := lockoutTestHandler(t, enc)

	guess := func(pin string) int {
		req := httptest.NewRequest(http.MethodGet, "/api/modes/adult/discover", nil)
		req.RemoteAddr = "10.1.10.55:4444"
		req.Header.Set("X-Api-Key", testAPIKey)
		req.Header.Set(sectionlock.HeaderName, pin)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec.Code
	}

	// The correct PIN works before any failures.
	if got := guess(lockoutTestPin); got != http.StatusOK {
		t.Fatalf("the correct PIN was refused before any failures: status = %d", got)
	}
	for i := 0; i < failureThresholdForTest; i++ {
		guess("000000")
	}
	if got := guess(lockoutTestPin); got != http.StatusForbidden {
		t.Fatalf("the correct PIN was accepted during the refusal window: status = %d", got)
	}
}

// The bucket is derived from the API key when the key is what authenticated —
// NOT from the cookie riding along on the same request.
func TestMiddlewareKeysLockoutOnTheAuthenticatingCredential(t *testing.T) {
	enc := testEncryptor(t)

	seen := make(chan string, 1)
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, _ := sectionlock.IdentityFrom(r.Context())
		seen <- id
		w.WriteHeader(http.StatusOK)
	})

	authStore := newTestStore(t)
	if err := authStore.SetAuthMode(context.Background(), ModePassword); err != nil {
		t.Fatalf("SetAuthMode: %v", err)
	}
	authStore.UseEnvAPIKey(testAPIKey)
	handler := Middleware(enc, authStore, inner)

	t.Run("api key authenticates", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/tracked", nil)
		req.Header.Set("X-Api-Key", testAPIKey)
		req.AddCookie(&http.Cookie{Name: CookieName, Value: "attacker-chosen-garbage"})
		handler.ServeHTTP(httptest.NewRecorder(), req)

		got := <-seen
		if want := sectionlock.KeyIdentity(testAPIKey); got != want {
			t.Fatalf("identity = %q, want %q — the bucket must come from the API key that "+
				"authenticated, not the unvalidated cookie on the same request", got, want)
		}
	})

	t.Run("session cookie authenticates", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/tracked", nil)
		token, err := IssueToken(enc)
		if err != nil {
			t.Fatalf("IssueToken: %v", err)
		}
		req.AddCookie(&http.Cookie{Name: CookieName, Value: token})
		handler.ServeHTTP(httptest.NewRecorder(), req)

		got := <-seen
		if want := sectionlock.SessionIdentity(token); got != want {
			t.Fatalf("identity = %q, want %q", got, want)
		}
	})
}

// failureThresholdForTest mirrors sectionlock's unexported failureThreshold.
// Duplicated rather than exported: the constant is part of that package's
// internal policy, and this test only needs it to assert the lockout trips
// PROMPTLY rather than to pin the exact value.
const failureThresholdForTest = 5

// lockoutTestPin is the PIN the lockout handler is configured with.
const lockoutTestPin = "135790"

func lockoutTestHandler(t *testing.T, enc *secrets.Store) http.Handler {
	t.Helper()
	ctx := context.Background()

	authStore := newTestStore(t)
	if err := authStore.SetAuthMode(ctx, ModePassword); err != nil {
		t.Fatalf("SetAuthMode: %v", err)
	}
	authStore.UseEnvAPIKey(testAPIKey)

	lockStore := sectionlock.NewStore(newMemorySettings())
	if err := lockStore.SetPin(ctx, lockoutTestPin); err != nil {
		t.Fatalf("SetPin: %v", err)
	}
	if err := lockStore.SetSections(ctx, []string{sectionlock.SectionAdultContent}); err != nil {
		t.Fatalf("SetSections: %v", err)
	}

	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	return Middleware(enc, authStore, inner, WithSectionGate(sectionlock.NewGate(lockStore, enc)))
}

func randomCookieValue(t *testing.T) string {
	t.Helper()
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return hex.EncodeToString(buf)
}
