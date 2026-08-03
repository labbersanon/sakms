package sectionlock

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

// SL-8 (first half): a malformed section_lock_sections value fails CLOSED,
// immediately, with no retry and no fall-through to a default.
//
// This is the deliberate divergence from adult_mode_enabled, which
// tolerates a malformed value and computes a default instead. That is
// correct for a pure visibility switch and wrong here: there, a corrupt
// value shows a tab; here, it would UNLOCK one.
func TestLockedSectionsMalformedFailsClosed(t *testing.T) {
	for _, raw := range []string{
		`{}`,         // wrong shape
		`[1,2]`,      // wrong element type
		`["a"`,       // truncated
		`not json`,   //
		`{"a":true}`, // the shape someone might reach for
		`"organize"`, // a bare string rather than an array
	} {
		fake := newFakeSettings()
		fake.values[SectionsKey] = raw
		store := NewStore(fake)

		got, err := store.LockedSections(context.Background())
		if !errors.Is(err, ErrLockConfigUnavailable) {
			t.Errorf("LockedSections with %q: err = %v, want ErrLockConfigUnavailable", raw, err)
		}
		if got != nil {
			t.Errorf("LockedSections with %q returned %v; a fail-closed read must not hand back a usable set", raw, got.Sorted())
		}
		// No retry: re-reading a corrupt value cannot un-corrupt it, and
		// a retry here would double the database load of every request on
		// a broken install.
		if n := fake.calls(SectionsKey); n != 1 {
			t.Errorf("LockedSections with %q made %d reads, want 1 (malformed must not retry)", raw, n)
		}
	}
}

// The boundary between "empty" and "malformed" is load-bearing in the
// other direction too: because the policy is fail-closed, reading a
// legitimately cleared value as malformed would BRICK the operator out
// with no credential able to help.
func TestLockedSectionsEmptyValues(t *testing.T) {
	for _, raw := range []string{"", "  ", "null", "[]"} {
		fake := newFakeSettings()
		fake.values[SectionsKey] = raw
		got, err := NewStore(fake).LockedSections(context.Background())
		if err != nil {
			t.Errorf("LockedSections with %q: unexpected error %v", raw, err)
			continue
		}
		if got.Len() != 0 {
			t.Errorf("LockedSections with %q = %v, want empty", raw, got.Sorted())
		}
	}
}

// An unset key is simply "nothing locked" — the state of every install
// that has never used the feature.
func TestLockedSectionsUnsetIsEmpty(t *testing.T) {
	got, err := NewStore(newFakeSettings()).LockedSections(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Len() != 0 {
		t.Fatalf("LockedSections on a fresh install = %v, want empty", got.Sorted())
	}
}

// SL-8 (second half): a TRANSIENT read error is retried exactly once, then
// fails closed.
//
// Both halves are asserted through the CALL COUNT, not the returned value.
// An implementation that never retries produces an identical error, and
// one that retries in a loop produces an identical success — only the
// count tells them apart.
func TestLockedSectionsTransientRetriesOnce(t *testing.T) {
	t.Run("recovers on the retry", func(t *testing.T) {
		fake := newFakeSettings()
		fake.values[SectionsKey] = `["organize","adult-content"]`
		fake.failNext[SectionsKey] = 1

		got, err := NewStore(fake).LockedSections(context.Background())
		if err != nil {
			t.Fatalf("expected the retry to recover, got %v", err)
		}
		if want := []string{SectionAdultContent, SectionOrganize}; !reflect.DeepEqual(got.Sorted(), want) {
			t.Fatalf("LockedSections = %v, want %v", got.Sorted(), want)
		}
		if n := fake.calls(SectionsKey); n != 2 {
			t.Fatalf("made %d reads, want exactly 2 (one failure + one retry)", n)
		}
	})

	t.Run("fails closed when the retry also fails", func(t *testing.T) {
		fake := newFakeSettings()
		fake.values[SectionsKey] = `["organize"]`
		fake.failAlways[SectionsKey] = true

		got, err := NewStore(fake).LockedSections(context.Background())
		if !errors.Is(err, ErrLockConfigUnavailable) {
			t.Fatalf("err = %v, want ErrLockConfigUnavailable", err)
		}
		if got != nil {
			t.Fatalf("a fail-closed read returned %v; callers must get nothing usable", got.Sorted())
		}
		if n := fake.calls(SectionsKey); n != 2 {
			t.Fatalf("made %d reads, want exactly 2 — one retry, not a loop", n)
		}
	})
}

// A failed read must not be cached. Caching one would mean a transient
// SQLite lock during a Dedup scan denied every request for the rest of the
// process's life.
func TestFailedReadIsNotCached(t *testing.T) {
	fake := newFakeSettings()
	fake.values[SectionsKey] = `["organize"]`
	fake.failAlways[SectionsKey] = true
	store := NewStore(fake)

	if _, err := store.LockedSections(context.Background()); err == nil {
		t.Fatal("expected the first read to fail")
	}
	fake.failAlways[SectionsKey] = false

	got, err := store.LockedSections(context.Background())
	if err != nil {
		t.Fatalf("expected recovery once the store came back, got %v", err)
	}
	if !got.Has(SectionOrganize) {
		t.Fatalf("LockedSections = %v, want organize", got.Sorted())
	}
}

// A malformed value must not poison the cache either — a corrected write
// has to take effect without a restart.
func TestMalformedValueDoesNotPoisonCache(t *testing.T) {
	fake := newFakeSettings()
	fake.values[SectionsKey] = `{"broken":true}`
	store := NewStore(fake)

	if _, err := store.LockedSections(context.Background()); !errors.Is(err, ErrLockConfigUnavailable) {
		t.Fatalf("expected a malformed read to fail closed, got %v", err)
	}
	if err := store.SetSections(context.Background(), []string{SectionQueue}); err != nil {
		t.Fatalf("SetSections: %v", err)
	}
	got, err := store.LockedSections(context.Background())
	if err != nil {
		t.Fatalf("expected the corrected value to be readable, got %v", err)
	}
	if !reflect.DeepEqual(got.Sorted(), []string{SectionQueue}) {
		t.Fatalf("LockedSections = %v, want [queue]", got.Sorted())
	}
}

// The gate runs on every API request, so a repeated read must not hit the
// database a second time — and a write must invalidate.
func TestStoreCachesAndInvalidatesOnWrite(t *testing.T) {
	fake := newFakeSettings()
	fake.values[SectionsKey] = `["tag"]`
	store := NewStore(fake)
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		if _, err := store.LockedSections(ctx); err != nil {
			t.Fatalf("LockedSections: %v", err)
		}
	}
	if n := fake.calls(SectionsKey); n != 1 {
		t.Fatalf("five reads hit the store %d times, want 1 — the gate must not add a query per request", n)
	}

	if err := store.SetSections(ctx, []string{SectionTag, SectionQueue}); err != nil {
		t.Fatalf("SetSections: %v", err)
	}
	got, err := store.LockedSections(ctx)
	if err != nil {
		t.Fatalf("LockedSections: %v", err)
	}
	if want := []string{SectionQueue, SectionTag}; !reflect.DeepEqual(got.Sorted(), want) {
		t.Fatalf("after SetSections, LockedSections = %v, want %v", got.Sorted(), want)
	}
}

// An unknown stored id is preserved rather than dropped — a temporarily
// removed tab re-locks if it returns — and gates nothing, because
// classification only ever emits canonical ids.
func TestUnknownStoredIDsArePreserved(t *testing.T) {
	fake := newFakeSettings()
	fake.values[SectionsKey] = `["organize","some-removed-tab"]`
	got, err := NewStore(fake).LockedSections(context.Background())
	if err != nil {
		t.Fatalf("an unknown id is not malformed: %v", err)
	}
	if !got.Has("some-removed-tab") {
		t.Fatal("an unknown stored id must be preserved, not silently dropped")
	}
	if got.Intersects(Classify("/api/modes/movies/discover")) {
		t.Fatal("an unknown stored id must gate nothing")
	}
}

// SL-1 (second half): the minimum PIN length is enforced server-side, not
// only in the panel — the API is reachable by curl.
func TestSetPinRejectsShortPin(t *testing.T) {
	store := NewStore(newFakeSettings())
	for _, pin := range []string{"", "1", "12345"} {
		if err := store.SetPin(context.Background(), pin); !errors.Is(err, ErrPinTooShort) {
			t.Errorf("SetPin(%q) = %v, want ErrPinTooShort", pin, err)
		}
	}
	if err := store.SetPin(context.Background(), "123456"); err != nil {
		t.Errorf("SetPin with exactly %d characters: %v", MinPinLength, err)
	}
}

// Clearing the PIN must always clear the locked set in the same operation.
// Leaving sections locked with no PIN in existence is the brick: nothing
// can satisfy the gate.
func TestClearPinAlsoClearsSections(t *testing.T) {
	fake := newFakeSettings()
	store := NewStore(fake)
	ctx := context.Background()

	if err := store.SetPin(ctx, "hunter2"); err != nil {
		t.Fatalf("SetPin: %v", err)
	}
	if err := store.SetSections(ctx, []string{SectionOrganize, SectionAdultContent}); err != nil {
		t.Fatalf("SetSections: %v", err)
	}
	if err := store.ClearPin(ctx); err != nil {
		t.Fatalf("ClearPin: %v", err)
	}

	set, err := store.PinSet(ctx)
	if err != nil {
		t.Fatalf("PinSet: %v", err)
	}
	if set {
		t.Error("PinSet is still true after ClearPin")
	}
	locked, err := store.LockedSections(ctx)
	if err != nil {
		t.Fatalf("LockedSections: %v", err)
	}
	if locked.Len() != 0 {
		t.Fatalf("ClearPin left %v locked with no PIN able to unlock them", locked.Sorted())
	}
}

// The hash is never handed back as a value the API could echo; PinSet is
// the only thing status reports.
func TestPinHashRoundTrip(t *testing.T) {
	fake := newFakeSettings()
	store := NewStore(fake)
	ctx := context.Background()

	if set, err := store.PinSet(ctx); err != nil || set {
		t.Fatalf("PinSet on a fresh install = %v, %v; want false, nil", set, err)
	}
	if err := store.SetPin(ctx, "correct-horse"); err != nil {
		t.Fatalf("SetPin: %v", err)
	}
	hash, err := store.PinHash(ctx)
	if err != nil {
		t.Fatalf("PinHash: %v", err)
	}
	if hash == "" || hash == "correct-horse" {
		t.Fatalf("stored hash = %q; want a bcrypt hash, not the PIN", hash)
	}
	if !VerifyPinHash(hash, "correct-horse") {
		t.Error("the stored hash does not verify the PIN it was made from")
	}
	if VerifyPinHash(hash, "wrong-horse") {
		t.Error("the stored hash verified a wrong PIN")
	}
	if VerifyPinHash("", "correct-horse") {
		t.Error("an empty hash must never verify anything")
	}
}
