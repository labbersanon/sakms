package sectionlock

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// newTestLockout returns a Lockout with a controllable clock, plus a
// function to advance it. The backoff caps at 15 minutes, so a test that
// slept through the progression would take a quarter of an hour.
func newTestLockout() (*Lockout, func(time.Duration)) {
	current := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	l := NewLockout()
	l.now = func() time.Time { return current }
	return l, func(d time.Duration) { current = current.Add(d) }
}

// SL-3: five consecutive failures trip a 60s refusal.
func TestLockoutTripsAfterFiveFailures(t *testing.T) {
	l, _ := newTestLockout()
	const id = "session:abc"

	for i := 1; i < failureThreshold; i++ {
		l.Fail(id)
		if _, locked := l.Locked(id); locked {
			t.Fatalf("locked out after only %d failures; the threshold is %d", i, failureThreshold)
		}
	}
	l.Fail(id)
	remaining, locked := l.Locked(id)
	if !locked {
		t.Fatalf("not locked out after %d failures", failureThreshold)
	}
	if remaining != baseBackoff {
		t.Fatalf("first refusal window = %v, want %v", remaining, baseBackoff)
	}
}

// SL-3: the window doubles on each further failure, up to a 15-minute cap.
func TestLockoutBackoffDoubles(t *testing.T) {
	l, advance := newTestLockout()
	const id = "key:abc"

	for i := 0; i < failureThreshold; i++ {
		l.Fail(id)
	}

	want := []time.Duration{
		60 * time.Second,
		2 * time.Minute,
		4 * time.Minute,
		8 * time.Minute,
		15 * time.Minute, // 16m, capped
		15 * time.Minute, // stays capped
	}
	for i, expected := range want {
		remaining, locked := l.Locked(id)
		if !locked {
			t.Fatalf("step %d: expected to be locked out", i)
		}
		if remaining != expected {
			t.Fatalf("step %d: window = %v, want %v", i, remaining, expected)
		}
		// Wait the window out, then fail once more.
		advance(remaining)
		if _, stillLocked := l.Locked(id); stillLocked {
			t.Fatalf("step %d: still locked out after the window elapsed", i)
		}
		l.Fail(id)
	}
}

// SL-3: a correct PIN resets the counter — and the BACKOFF, not just the
// failure count. Otherwise a success followed by five fresh failures would
// resume at the previously doubled window, which is not what "reset on
// success" means.
func TestLockoutResetsOnSuccess(t *testing.T) {
	l, advance := newTestLockout()
	const id = "session:abc"

	// Trip it twice so the backoff has doubled to 2 minutes.
	for i := 0; i < failureThreshold+1; i++ {
		l.Fail(id)
	}
	if remaining, _ := l.Locked(id); remaining != 2*time.Minute {
		t.Fatalf("precondition failed: window = %v, want 2m", remaining)
	}

	advance(2 * time.Minute)
	l.Reset(id)

	if _, locked := l.Locked(id); locked {
		t.Fatal("still locked out after a successful verification")
	}
	// A fresh run of failures must start from scratch: four do nothing...
	for i := 1; i < failureThreshold; i++ {
		l.Fail(id)
		if _, locked := l.Locked(id); locked {
			t.Fatalf("locked out after %d failures following a reset; the count did not reset", i)
		}
	}
	// ...and the fifth trips the BASE window, not the doubled one.
	l.Fail(id)
	remaining, locked := l.Locked(id)
	if !locked {
		t.Fatal("not locked out after a fresh run of five failures")
	}
	if remaining != baseBackoff {
		t.Fatalf("window after reset = %v, want %v — the backoff did not reset", remaining, baseBackoff)
	}
}

// SL-3: the counter is per-identity, never global.
//
// A global counter would let the STATED THREAT ACTOR — a household member
// on a shared screen — self-DoS the operator indefinitely by spamming
// wrong PINs from their own browser.
func TestLockoutIsPerIdentity(t *testing.T) {
	l, _ := newTestLockout()
	for i := 0; i < failureThreshold*3; i++ {
		l.Fail("session:intruder")
	}
	if _, locked := l.Locked("session:intruder"); !locked {
		t.Fatal("the failing identity is not locked out")
	}
	if _, locked := l.Locked("session:operator"); locked {
		t.Fatal("one identity's failures locked out a different identity — the counter is global")
	}
}

// Identity reads the bucket auth.Middleware resolved from the VERIFIED
// credential, and falls back to the client IP when there is none. It must
// never derive a bucket from a raw request value.
//
// Claude 2026-08-03: rewritten. The previous version asserted the OLD
// behaviour — that a raw sakms_session cookie on the request produced a
// "session:" bucket, which was the vulnerability itself (an attacker holding a
// valid API key could vary that cookie freely and mint an unlimited supply of
// buckets). The credential-derived forms are now built by KeyIdentity /
// SessionIdentity at the point of verification and are asserted below.
func TestIdentity(t *testing.T) {
	// A raw cookie and a raw API key on the request, with NO identity in the
	// context: all three must collapse to the IP bucket. This is the assertion
	// that fails if the cookie-sniffing derivation is ever restored.
	withSession := httptest.NewRequest(http.MethodGet, "/api/modes/adult/discover", nil)
	withSession.AddCookie(&http.Cookie{Name: "sakms_session", Value: "ciphertext-a"})
	withSession.Header.Set("X-Api-Key", "key-a")
	withSession.RemoteAddr = "10.1.10.55:4444"

	bare := httptest.NewRequest(http.MethodGet, "/api/modes/adult/discover", nil)
	bare.RemoteAddr = "10.1.10.55:4444"

	if got := Identity(withSession); got != "ip:10.1.10.55" {
		t.Fatalf("Identity honoured an UNVALIDATED request credential: got %q, want ip:10.1.10.55", got)
	}
	if got := Identity(bare); got != "ip:10.1.10.55" {
		t.Fatalf("IP fallback = %q, want ip:10.1.10.55", got)
	}

	// A varying raw cookie must NOT vary the bucket — the whole bypass.
	other := httptest.NewRequest(http.MethodGet, "/", nil)
	other.RemoteAddr = "10.1.10.55:4444"
	other.AddCookie(&http.Cookie{Name: "sakms_session", Value: "ciphertext-b"})
	if Identity(other) != Identity(withSession) {
		t.Fatal("varying the raw session cookie varied the lockout bucket — the brute-force bypass is back")
	}

	// With an identity in the context, that is what is used.
	authed := httptest.NewRequest(http.MethodGet, "/", nil)
	authed.RemoteAddr = "10.1.10.55:4444"
	authed = authed.WithContext(WithIdentity(authed.Context(), KeyIdentity("key-a")))
	if got := Identity(authed); got != KeyIdentity("key-a") {
		t.Fatalf("Identity = %q, want the context identity %q", got, KeyIdentity("key-a"))
	}

	// A spoofed X-Forwarded-For must not change the identity.
	spoofed := httptest.NewRequest(http.MethodGet, "/", nil)
	spoofed.RemoteAddr = "10.1.10.55:4444"
	spoofed.Header.Set("X-Forwarded-For", "203.0.113.9")
	if Identity(spoofed) != "ip:10.1.10.55" {
		t.Fatal("X-Forwarded-For changed the lockout identity; it is caller-controlled and must be ignored")
	}
}

// The three bucket forms are distinct from each other and never embed the raw
// credential.
func TestIdentityFormsAreDistinctAndOpaque(t *testing.T) {
	key, session := KeyIdentity("key-a"), SessionIdentity("ciphertext-a")
	bare := httptest.NewRequest(http.MethodGet, "/", nil)
	bare.RemoteAddr = "10.1.10.55:4444"
	ip := IPIdentity(bare)

	if key == session || key == ip || session == ip {
		t.Fatalf("identities collided: session=%q key=%q ip=%q", session, key, ip)
	}
	// The same credential is stably one bucket; a different one is another.
	if KeyIdentity("key-a") != key || SessionIdentity("ciphertext-a") != session {
		t.Fatal("the same credential produced two different identities")
	}
	if KeyIdentity("key-b") == key || SessionIdentity("ciphertext-b") == session {
		t.Fatal("two different credentials produced one identity")
	}
	for _, got := range []string{session, key} {
		if contains(got, "ciphertext-a") || contains(got, "key-a") {
			t.Fatalf("identity %q leaks the raw credential", got)
		}
	}
}

// Transfer moves an accumulated failure count onto a rotated credential, so
// regenerating the API key cannot hand the caller a fresh allowance.
func TestLockoutTransfer(t *testing.T) {
	l, _ := newTestLockout()
	old, fresh := KeyIdentity("old-key"), KeyIdentity("new-key")

	// One short of tripping — the shape an attacker would rotate at.
	for i := 0; i < failureThreshold-1; i++ {
		l.Fail(old)
	}
	l.Transfer(old, fresh)

	if _, locked := l.Locked(fresh); locked {
		t.Fatal("the transferred bucket is already locked; the count was not carried, it was inflated")
	}
	// The very next failure on the NEW identity must trip it.
	l.Fail(fresh)
	if _, locked := l.Locked(fresh); !locked {
		t.Fatalf("after %d failures then a rotation, one more failure did not trip the lockout — rotation resets the counter", failureThreshold-1)
	}
	if _, locked := l.Locked(old); locked {
		t.Fatal("the old identity is still locked; Transfer must MOVE, not copy")
	}
}

// Transfer is a no-op with no prior history, so break-glass recovery with a
// freshly-minted key is never encumbered by a bucket it did not earn.
func TestLockoutTransferWithNoHistoryIsNoOp(t *testing.T) {
	l, _ := newTestLockout()
	fresh := KeyIdentity("new-key")
	l.Transfer(KeyIdentity("never-used"), fresh)

	if _, ok := l.entries[fresh]; ok {
		t.Fatal("Transfer seeded an entry for an identity with no failure history")
	}
	if _, locked := l.Locked(fresh); locked {
		t.Fatal("a freshly-minted key is locked out; break-glass recovery is broken")
	}
}

func contains(haystack, needle string) bool {
	return len(needle) > 0 && len(haystack) >= len(needle) &&
		(haystack == needle || indexOf(haystack, needle) >= 0)
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
