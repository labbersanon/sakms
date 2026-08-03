package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labbersanon/sakms/internal/auth"
	"github.com/labbersanon/sakms/internal/sectionlock"
	"github.com/labbersanon/sakms/internal/settings"
)

// apikey_lockout_test.go — rotating the API key must not reset the section
// lock's brute-force counter.
//
// # Why this route is the second half of the brute-force bypass
//
// The lockout is keyed on the credential that authenticated, so a FRESH key is
// a FRESH bucket with a zeroed failure count. POST /api/apikey/regenerate is
// deliberately exempt from section gating (it is the break-glass recovery path
// — see docs/break-glass-recovery.md) and returns the plaintext key. So without
// the transfer, an attacker holding a valid key spends four guesses, rotates,
// and repeats forever, never reaching failureThreshold.
//
// Note the shape of the assertion below: it deliberately uses FOUR failures,
// one short of the threshold, because that is the count an attacker would
// actually rotate at. A test that failed five times first would trip the
// lockout before rotating and would pass even with no transfer at all.
//
// Note also WHERE the window opens. Lockout.Fail increments and only then sets
// the refusal window, and VerifyPin checks the window BEFORE it counts — so the
// threshold-reaching attempt still answers "wrong PIN", and the refusal is
// observable on the attempt after it. These tests therefore assert on the
// CORRECT PIN being refused, which is both the real observable and the property
// that actually matters.

// Four failures, then a rotation, then ONE more failure must trip the lockout —
// the counter has to survive the rotation.
func TestRegenerateCarriesTheLockoutToTheNewKey(t *testing.T) {
	ctx := context.Background()
	f := newAPIKeyLockoutFixture(t)

	// Four wrong PINs on the original key: one short of tripping.
	for i := 0; i < 4; i++ {
		if err := f.gate.VerifyPin(ctx, sectionlock.KeyIdentity(f.key), "000000"); err == nil {
			t.Fatalf("attempt %d: a wrong PIN was accepted", i+1)
		}
	}

	previous := f.key
	fresh := f.regenerate(t)
	if fresh == previous {
		t.Fatal("regenerate returned the same key")
	}

	// One more wrong PIN on the NEW key. If the count carried across the
	// rotation this is the 5th and it trips the window; if it did not, this is
	// the new bucket's 1st and nothing trips.
	_ = f.gate.VerifyPin(ctx, sectionlock.KeyIdentity(fresh), "000000")

	// THE ASSERTION: the CORRECT PIN must now be refused as a lockout.
	err := f.gate.VerifyPin(ctx, sectionlock.KeyIdentity(fresh), apiKeyLockoutPin)
	var lockedOut *sectionlock.LockedOutError
	if !errors.As(err, &lockedOut) {
		t.Fatalf("after 4 failures, a key rotation and a 5th failure, the CORRECT PIN "+
			"returned %v instead of a lockout — rotating the API key handed the caller a "+
			"fresh allowance, so the brute-force counter can be reset indefinitely at a "+
			"cost of one request per 4 guesses", err)
	}
}

// The old key's bucket must not survive the rotation: Transfer MOVES.
func TestRegenerateDoesNotLeaveTheOldBucketBehind(t *testing.T) {
	ctx := context.Background()
	f := newAPIKeyLockoutFixture(t)

	for i := 0; i < 4; i++ {
		_ = f.gate.VerifyPin(ctx, sectionlock.KeyIdentity(f.key), "000000")
	}
	old := f.key
	f.regenerate(t)

	// The dead key's bucket is gone, so it starts from zero. One failure against
	// it is the 1st, not the 5th, so no window opens and the correct PIN still
	// works. Had Transfer COPIED, that failure would be the 5th and the correct
	// PIN would be refused.
	if err := f.gate.VerifyPin(ctx, sectionlock.KeyIdentity(old), "000000"); err == nil {
		t.Fatal("a wrong PIN was accepted")
	}
	var lockedOut *sectionlock.LockedOutError
	if err := f.gate.VerifyPin(ctx, sectionlock.KeyIdentity(old), apiKeyLockoutPin); errors.As(err, &lockedOut) {
		t.Fatal("the retired key's bucket still carries the pre-rotation count — Transfer " +
			"copied instead of moving, so one rotation yields two independently-counting " +
			"buckets rather than one that moved")
	}
}

// Break-glass: an operator recovering with a freshly-minted key has no failure
// history, so the rotation must not encumber them with a bucket they never
// earned. docs/break-glass-recovery.md depends on this route staying usable.
func TestRegenerateWithNoFailureHistoryIsUnencumbered(t *testing.T) {
	ctx := context.Background()
	f := newAPIKeyLockoutFixture(t)

	fresh := f.regenerate(t)

	// The CORRECT PIN must work immediately on the new key.
	if err := f.gate.VerifyPin(ctx, sectionlock.KeyIdentity(fresh), apiKeyLockoutPin); err != nil {
		t.Fatalf("the correct PIN was refused on a freshly-minted key with no failure "+
			"history (%v) — break-glass recovery is broken", err)
	}
}

const apiKeyLockoutPin = "246813"

type apiKeyLockoutFixture struct {
	mux  http.Handler
	gate *sectionlock.Gate
	key  string
}

func newAPIKeyLockoutFixture(t *testing.T) *apiKeyLockoutFixture {
	t.Helper()
	ctx := context.Background()

	authStore, secretStore, sqlDB := testAuthStoreWithDB(t)
	if err := authStore.SetAuthMode(ctx, auth.ModePassword); err != nil {
		t.Fatalf("SetAuthMode: %v", err)
	}
	key, err := authStore.EnsureAPIKey(ctx)
	if err != nil {
		t.Fatalf("EnsureAPIKey: %v", err)
	}

	lockStore := sectionlock.NewStore(settings.New(sqlDB))
	if err := lockStore.SetPin(ctx, apiKeyLockoutPin); err != nil {
		t.Fatalf("SetPin: %v", err)
	}
	if err := lockStore.SetSections(ctx, []string{sectionlock.SectionAdultContent}); err != nil {
		t.Fatalf("SetSections: %v", err)
	}
	gate := sectionlock.NewGate(lockStore, secretStore)

	// Wrapped in the SAME middleware production uses, so the handler reads its
	// identity from the context the middleware populates — the whole mechanism
	// under test.
	mux := auth.Middleware(secretStore, authStore, NewAPIKeyMux(authStore, gate),
		auth.WithSectionGate(gate))
	return &apiKeyLockoutFixture{mux: mux, gate: gate, key: key}
}

// regenerate drives the real route with the CURRENT key and returns the new one.
func (f *apiKeyLockoutFixture) regenerate(t *testing.T) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/apikey/regenerate", nil)
	req.Header.Set("X-Api-Key", f.key)
	rec := httptest.NewRecorder()
	f.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("regenerate = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	var body apikeyRegenerateResponse
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decoding regenerate response: %v", err)
	}
	if body.APIKey == "" {
		t.Fatal("regenerate returned an empty key")
	}
	f.key = body.APIKey
	return body.APIKey
}
