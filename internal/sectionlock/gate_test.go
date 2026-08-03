package sectionlock

import (
	"context"
	"errors"
	"testing"
	"time"
)

const testPin = "correct-horse"

// memoWarmFor reports whether the gate holds a positive memo entry for pin
// against the currently stored hash. Reading the internals directly keeps
// the cache tests deterministic — the alternative is timing bcrypt, which
// is exactly the kind of assertion that goes flaky on a loaded CI box.
func memoWarmFor(t *testing.T, g *Gate, pin string) bool {
	t.Helper()
	hash, err := g.store.PinHash(context.Background())
	if err != nil {
		t.Fatalf("PinHash: %v", err)
	}
	return g.memoHit(pin, hashFingerprint(hash))
}

// SL-1: PIN set/verify roundtrip.
func TestVerifyPinRoundTrip(t *testing.T) {
	gate, _ := newTestGate(t)
	ctx := context.Background()

	// Before a PIN exists, nothing verifies — and the reason is
	// distinguishable from a wrong PIN, because the API answers them
	// differently.
	if err := gate.VerifyPin(ctx, "session:a", testPin); !errors.Is(err, ErrNoPinSet) {
		t.Fatalf("VerifyPin with no PIN set = %v, want ErrNoPinSet", err)
	}

	if err := gate.store.SetPin(ctx, testPin); err != nil {
		t.Fatalf("SetPin: %v", err)
	}
	if err := gate.VerifyPin(ctx, "session:a", testPin); err != nil {
		t.Fatalf("VerifyPin with the correct PIN = %v, want nil", err)
	}
	if err := gate.VerifyPin(ctx, "session:a", "wrong-horse"); !errors.Is(err, ErrWrongPin) {
		t.Fatalf("VerifyPin with a wrong PIN = %v, want ErrWrongPin", err)
	}
}

// SL-3 (fourth clause): a request carrying NO PIN is not a failed attempt.
//
// Every gated request the browser makes carries no PIN header — the
// browser uses the cookie. Counting those would inflict a permanent
// lockout within seconds of locking a section, on the operator's own
// session.
func TestMissingPinIsNotAFailure(t *testing.T) {
	gate, _ := newTestGate(t)
	ctx := context.Background()
	if err := gate.store.SetPin(ctx, testPin); err != nil {
		t.Fatalf("SetPin: %v", err)
	}

	for i := 0; i < failureThreshold*4; i++ {
		if err := gate.VerifyPin(ctx, "session:a", ""); !errors.Is(err, ErrNoPinPresented) {
			t.Fatalf("VerifyPin with no PIN = %v, want ErrNoPinPresented", err)
		}
	}
	if _, locked := gate.lockout.Locked("session:a"); locked {
		t.Fatal("PIN-less requests tripped the lockout")
	}
	if err := gate.VerifyPin(ctx, "session:a", testPin); err != nil {
		t.Fatalf("the correct PIN was refused after %d PIN-less requests: %v", failureThreshold*4, err)
	}
}

// SL-4: the brute-force lockout is checked BEFORE the memo cache.
//
// The reverse order is a real bypass, and an easy one to write by
// accident: a memo entry warmed by an earlier correct PIN sails straight
// through an active refusal window — which is precisely the window an
// attacker who has just guessed the PIN lands in.
func TestLockoutIsCheckedBeforeMemoCache(t *testing.T) {
	gate, _ := newTestGate(t)
	ctx := context.Background()
	const id = "session:a"

	if err := gate.store.SetPin(ctx, testPin); err != nil {
		t.Fatalf("SetPin: %v", err)
	}

	// Warm the memo with a genuine success.
	if err := gate.VerifyPin(ctx, id, testPin); err != nil {
		t.Fatalf("VerifyPin: %v", err)
	}
	if !memoWarmFor(t, gate, testPin) {
		t.Fatal("precondition failed: the memo cache is not warm, so this test would prove nothing")
	}

	// Trip the lockout on the SAME identity.
	for i := 0; i < failureThreshold; i++ {
		if err := gate.VerifyPin(ctx, id, "wrong-horse"); !errors.Is(err, ErrWrongPin) {
			t.Fatalf("attempt %d = %v, want ErrWrongPin", i+1, err)
		}
	}
	if _, locked := gate.lockout.Locked(id); !locked {
		t.Fatalf("precondition failed: %d wrong PINs did not trip the lockout", failureThreshold)
	}

	// The memo is still warm. The correct PIN must STILL be refused.
	if !memoWarmFor(t, gate, testPin) {
		t.Fatal("precondition failed: the memo entry vanished, so this test would prove nothing")
	}
	err := gate.VerifyPin(ctx, id, testPin)
	if !errors.Is(err, ErrLockedOut) {
		t.Fatalf("a warmed memo cache sailed through an active lockout: VerifyPin = %v, want ErrLockedOut", err)
	}

	var lockedOut *LockedOutError
	if !errors.As(err, &lockedOut) {
		t.Fatal("the lockout error does not carry a retry duration; the unlock endpoint cannot answer distinguishably")
	}
	if lockedOut.RetryAfter <= 0 || lockedOut.RetryAfter > baseBackoff {
		t.Fatalf("RetryAfter = %v, want (0, %v]", lockedOut.RetryAfter, baseBackoff)
	}
}

// SL-5: the memo cache invalidates on a PIN change, without a restart.
//
// The mechanism is that the memo's VALUE is a fingerprint of the stored
// hash, re-read on every verification — so a new hash misses every
// existing entry rather than needing an explicit invalidation call that
// some future write path could forget to make.
func TestMemoCacheInvalidatesOnPinChange(t *testing.T) {
	gate, _ := newTestGate(t)
	ctx := context.Background()
	const id = "session:a"
	const oldPin, newPin = "old-pin-value", "new-pin-value"

	if err := gate.store.SetPin(ctx, oldPin); err != nil {
		t.Fatalf("SetPin: %v", err)
	}
	if err := gate.VerifyPin(ctx, id, oldPin); err != nil {
		t.Fatalf("VerifyPin: %v", err)
	}
	if !memoWarmFor(t, gate, oldPin) {
		t.Fatal("precondition failed: the memo did not warm on a successful verification")
	}

	// Change the PIN on the SAME running gate — no new Gate, no restart.
	if err := gate.store.SetPin(ctx, newPin); err != nil {
		t.Fatalf("SetPin: %v", err)
	}

	if err := gate.VerifyPin(ctx, id, oldPin); !errors.Is(err, ErrWrongPin) {
		t.Fatalf("the OLD PIN still verified after a PIN change: %v", err)
	}
	if err := gate.VerifyPin(ctx, id, newPin); err != nil {
		t.Fatalf("the NEW PIN did not verify: %v", err)
	}
	if memoWarmFor(t, gate, oldPin) {
		t.Fatal("the old PIN's memo entry is still live against the new hash")
	}
}

// Negative results are never cached: caching them would make a wrong PIN
// cheap to retry, and would let a stale negative survive a PIN change.
func TestMemoCacheNeverCachesNegatives(t *testing.T) {
	gate, _ := newTestGate(t)
	ctx := context.Background()
	if err := gate.store.SetPin(ctx, testPin); err != nil {
		t.Fatalf("SetPin: %v", err)
	}
	if err := gate.VerifyPin(ctx, "session:a", "wrong-horse"); !errors.Is(err, ErrWrongPin) {
		t.Fatalf("VerifyPin = %v, want ErrWrongPin", err)
	}
	if memoWarmFor(t, gate, "wrong-horse") {
		t.Fatal("a failed verification was cached")
	}
	if len(gate.memo) != 0 {
		t.Fatalf("memo holds %d entries after only a failure, want 0", len(gate.memo))
	}
}

// The memo key must be an HMAC under a process-random key, not a bare
// digest of the PIN — a short PIN is trivially rainbow-tableable from a
// heap dump. Two gates in one process must therefore key the same PIN
// differently.
func TestMemoKeyIsProcessRandom(t *testing.T) {
	a, _ := newTestGate(t)
	b, _ := newTestGate(t)
	if a.memoKeyFor(testPin) == b.memoKeyFor(testPin) {
		t.Fatal("two gates derived the same memo key for one PIN; the key is not process-random")
	}
	if a.memoKeyFor(testPin) != a.memoKeyFor(testPin) {
		t.Fatal("one gate derived two different memo keys for one PIN")
	}
}

func lockedGate(t *testing.T, sections ...string) *Gate {
	t.Helper()
	gate, _ := newTestGate(t)
	ctx := context.Background()
	if err := gate.store.SetPin(ctx, testPin); err != nil {
		t.Fatalf("SetPin: %v", err)
	}
	if err := gate.store.SetSections(ctx, sections); err != nil {
		t.Fatalf("SetSections: %v", err)
	}
	return gate
}

func TestDecide(t *testing.T) {
	ctx := context.Background()
	organizeRoute := Classify("/api/modes/movies/rename/scan")
	adultRoute := Classify("/api/modes/adult/discover")
	queueRoute := Classify("/api/downloads")

	t.Run("inert when not enforcing", func(t *testing.T) {
		gate := lockedGate(t, SectionOrganize)
		d, err := gate.Decide(ctx, Request{Enforcing: false})
		if err != nil {
			t.Fatalf("Decide: %v", err)
		}
		if !d.Allows(organizeRoute) {
			t.Fatal("a non-enforcing decision denied a locked route")
		}
	})

	t.Run("nothing locked allows everything", func(t *testing.T) {
		gate, _ := newTestGate(t)
		d, err := gate.Decide(ctx, Request{Enforcing: true})
		if err != nil {
			t.Fatalf("Decide: %v", err)
		}
		if !d.Allows(organizeRoute) || !d.Allows(adultRoute) {
			t.Fatal("a decision with nothing locked denied a route")
		}
	})

	t.Run("locked route denied without a credential", func(t *testing.T) {
		gate := lockedGate(t, SectionOrganize)
		d, err := gate.Decide(ctx, Request{Enforcing: true})
		if err != nil {
			t.Fatalf("Decide: %v", err)
		}
		if d.Allows(organizeRoute) {
			t.Fatal("a locked route was allowed with no credential")
		}
		if d.FirstLocked(organizeRoute) != SectionOrganize {
			t.Fatalf("FirstLocked = %q, want organize", d.FirstLocked(organizeRoute))
		}
		// An unlocked section is unaffected.
		if !d.Allows(queueRoute) {
			t.Fatal("locking organize also denied an unrelated queue route")
		}
	})

	t.Run("a valid ticket unlocks", func(t *testing.T) {
		gate := lockedGate(t, SectionOrganize)
		ticket, expiry, err := gate.IssueTicket()
		if err != nil {
			t.Fatalf("IssueTicket: %v", err)
		}
		d, err := gate.Decide(ctx, Request{Enforcing: true, Ticket: ticket})
		if err != nil {
			t.Fatalf("Decide: %v", err)
		}
		if !d.Allows(organizeRoute) {
			t.Fatal("a valid unlock ticket did not unlock a locked route")
		}
		if d.ExpiresAt.Unix() != expiry.Unix() {
			t.Fatalf("ExpiresAt = %v, want %v", d.ExpiresAt, expiry)
		}
	})

	t.Run("the correct PIN header unlocks, per request", func(t *testing.T) {
		gate := lockedGate(t, SectionOrganize)
		d, err := gate.Decide(ctx, Request{Enforcing: true, Pin: testPin, Identity: "key:a"})
		if err != nil {
			t.Fatalf("Decide: %v", err)
		}
		if !d.Allows(organizeRoute) {
			t.Fatal("the correct PIN header did not unlock a locked route")
		}
		if !d.ExpiresAt.IsZero() {
			t.Fatalf("ExpiresAt = %v; a header carries no lifetime of its own", d.ExpiresAt)
		}
	})

	t.Run("a wrong PIN denies and is reported distinguishably", func(t *testing.T) {
		gate := lockedGate(t, SectionOrganize)
		d, err := gate.Decide(ctx, Request{Enforcing: true, Pin: "nope-nope", Identity: "key:a"})
		if err != nil {
			t.Fatalf("Decide: %v", err)
		}
		if d.Allows(organizeRoute) {
			t.Fatal("a wrong PIN unlocked a locked route")
		}
		if !errors.Is(d.Err, ErrWrongPin) {
			t.Fatalf("Decision.Err = %v, want ErrWrongPin", d.Err)
		}
	})

	t.Run("an active lockout surfaces on the Decision", func(t *testing.T) {
		gate := lockedGate(t, SectionOrganize)
		for i := 0; i < failureThreshold; i++ {
			if _, err := gate.Decide(ctx, Request{Enforcing: true, Pin: "nope-nope", Identity: "key:a"}); err != nil {
				t.Fatalf("Decide: %v", err)
			}
		}
		d, err := gate.Decide(ctx, Request{Enforcing: true, Pin: testPin, Identity: "key:a"})
		if err != nil {
			t.Fatalf("Decide: %v", err)
		}
		if d.Allows(organizeRoute) {
			t.Fatal("the correct PIN unlocked during an active lockout")
		}
		if !errors.Is(d.Err, ErrLockedOut) {
			t.Fatalf("Decision.Err = %v, want ErrLockedOut — the unlock endpoint cannot "+
				"tell a lockout from a wrong PIN", d.Err)
		}
	})

	// A valid ticket deliberately bypasses the lockout: the lockout bounds
	// GUESSING, and a ticket is proof the PIN was already entered. Without
	// this, anyone able to reach the API could self-DoS the already
	// unlocked operator by spamming wrong PINs on their identity.
	//
	// This does NOT contradict SL-4, which is about the memo cache on the
	// PIN path. It is asserted separately so that a future "fix" hoisting
	// the lockout check above the ticket check cannot pass silently.
	t.Run("a valid ticket bypasses an active lockout", func(t *testing.T) {
		gate := lockedGate(t, SectionOrganize)
		const id = "session:a"

		ticket, _, err := gate.IssueTicket()
		if err != nil {
			t.Fatalf("IssueTicket: %v", err)
		}
		for i := 0; i < failureThreshold; i++ {
			if _, err := gate.Decide(ctx, Request{Enforcing: true, Pin: "nope-nope", Identity: id}); err != nil {
				t.Fatalf("Decide: %v", err)
			}
		}
		if _, locked := gate.lockout.Locked(id); !locked {
			t.Fatal("precondition failed: the lockout did not trip")
		}

		d, err := gate.Decide(ctx, Request{Enforcing: true, Ticket: ticket, Identity: id})
		if err != nil {
			t.Fatalf("Decide: %v", err)
		}
		if !d.Allows(organizeRoute) {
			t.Fatal("an active lockout refused an already-issued unlock ticket — " +
				"anyone can now self-DoS the unlocked operator by spamming wrong PINs")
		}
		if d.Err != nil {
			t.Fatalf("Decision.Err = %v, want nil on a ticket-unlocked request", d.Err)
		}
	})

	t.Run("an unreadable configuration fails closed", func(t *testing.T) {
		fake := newFakeSettings()
		fake.values[SectionsKey] = `{"corrupt":true}`
		gate := NewGate(NewStore(fake), newTestCrypto(t))
		if _, err := gate.Decide(ctx, Request{Enforcing: true}); !errors.Is(err, ErrLockConfigUnavailable) {
			t.Fatalf("Decide = %v, want ErrLockConfigUnavailable so the caller denies", err)
		}
	})

	// Sections locked with no PIN in existence: unreachable through the
	// API (PUT /sections refuses a non-empty array with no PIN, and
	// clearing the PIN clears the sections) but reachable via a corrupt
	// hash or a hand-edited database. It DENIES rather than falling open;
	// SAKMS_SECTION_LOCK_DISABLE=1 is the documented way out.
	t.Run("locked sections with no PIN deny", func(t *testing.T) {
		fake := newFakeSettings()
		fake.values[SectionsKey] = `["organize"]`
		gate := NewGate(NewStore(fake), newTestCrypto(t))
		d, err := gate.Decide(ctx, Request{Enforcing: true})
		if err != nil {
			t.Fatalf("Decide: %v", err)
		}
		if d.Allows(organizeRoute) {
			t.Fatal("a locked section with no PIN set fell OPEN; it must fail closed")
		}
		if !errors.Is(d.Err, ErrNoPinSet) {
			t.Fatalf("Decision.Err = %v, want ErrNoPinSet", d.Err)
		}
	})
}

// The anti-RBAC invariant, asserted rather than merely documented: ONE
// ticket unlocks EVERY locked section at once, and carries no per-section
// scope. Unlocking to read Discover therefore also unlocks Adult.
//
// With a single shared secret any other behaviour would be security
// theatre and a de-facto role layer — the thing this feature exists to not
// become.
func TestOneTicketUnlocksEverySection(t *testing.T) {
	ctx := context.Background()
	gate := lockedGate(t, SectionOrganize, SectionDiscover, SectionAdultContent, SectionSettings)

	ticket, _, err := gate.IssueTicket()
	if err != nil {
		t.Fatalf("IssueTicket: %v", err)
	}
	d, err := gate.Decide(ctx, Request{Enforcing: true, Ticket: ticket})
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	for _, p := range []string{
		"/api/modes/movies/rename/scan",
		"/api/modes/adult/rename/scan",
		"/api/discover/rss-feeds",
		"/api/settings/dedup-scan-interval",
		"/api/modes/adult/discover",
	} {
		if !d.Allows(Classify(p)) {
			t.Errorf("one unlock ticket did not cover %s", p)
		}
	}
}

// A route belonging to two sections is gated when EITHER is locked — the
// property AC6 and AC7 jointly require.
func TestRouteInTwoSectionsIsGatedByEither(t *testing.T) {
	ctx := context.Background()
	adultRename := Classify("/api/modes/adult/rename/scan")

	for _, locked := range []string{SectionOrganize, SectionAdultContent} {
		gate := lockedGate(t, locked)
		d, err := gate.Decide(ctx, Request{Enforcing: true})
		if err != nil {
			t.Fatalf("Decide: %v", err)
		}
		if d.Allows(adultRename) {
			t.Errorf("locking %q did not gate /api/modes/adult/rename/scan", locked)
		}
	}

	// Locking only adult-content must NOT gate the Mainstream sibling.
	gate := lockedGate(t, SectionAdultContent)
	d, err := gate.Decide(ctx, Request{Enforcing: true})
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if !d.Allows(Classify("/api/modes/movies/rename/scan")) {
		t.Error("locking adult-content also gated the Mainstream route; AC6 forbids that")
	}
}

// Layer 2/3's context plumbing: an ABSENT decision allows, because four
// background schedulers call mode.Build from context.Background() and a
// PIN lock must not stop background work.
func TestRequireWithoutDecisionAllows(t *testing.T) {
	ctx := context.Background()
	if err := RequireAdult(ctx); err != nil {
		t.Fatalf("RequireAdult with no decision = %v, want nil", err)
	}
	for _, m := range []string{"movies", "series", "adult"} {
		if err := RequireMode(ctx, m); err != nil {
			t.Fatalf("RequireMode(%q) with no decision = %v, want nil", m, err)
		}
	}
}

func TestRequireMode(t *testing.T) {
	locked := WithDecision(context.Background(), Decision{
		Enforcing: true,
		Locked:    NewSet(SectionAdultContent),
	})
	if err := RequireMode(locked, "adult"); !errors.Is(err, ErrSectionLocked) {
		t.Fatalf("RequireMode(adult) with adult-content locked = %v, want ErrSectionLocked", err)
	}
	for _, m := range []string{"movies", "series"} {
		if err := RequireMode(locked, m); err != nil {
			t.Errorf("RequireMode(%q) = %v, want nil — Build is a validation chokepoint, "+
				"not an authorization one, for non-Adult modes", m, err)
		}
	}

	unlocked := WithDecision(context.Background(), Decision{
		Enforcing: true,
		Locked:    NewSet(SectionAdultContent),
		Unlocked:  true,
		ExpiresAt: time.Now().Add(time.Minute),
	})
	if err := RequireMode(unlocked, "adult"); err != nil {
		t.Fatalf("RequireMode(adult) while unlocked = %v, want nil", err)
	}

	inert := WithDecision(context.Background(), Decision{Locked: NewSet(SectionAdultContent)})
	if err := RequireMode(inert, "adult"); err != nil {
		t.Fatalf("RequireMode(adult) with a non-enforcing decision = %v, want nil", err)
	}
}
