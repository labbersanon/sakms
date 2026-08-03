package sectionlock

import (
	"context"
	"testing"
)

// store_race_test.go — the cache must never mark a stale value valid.
//
// # The race
//
// PinHash and LockedSections both RELEASE the mutex to do the database read.
// Without a generation counter the sequence below poisons the cache for the
// process's lifetime:
//
//	reader  misses the cache, releases the lock, reads the OLD value
//	writer  stores a NEW value and invalidates
//	reader  re-acquires the lock and caches the OLD value, marking it VALID
//
// The consequence is not a stale read once — it is a PIN change that silently
// does not take effect (the old PIN keeps working), or a "lock everything" that
// silently does not (the newly-locked sections keep passing), until the next
// write or a restart. Gate.LockedNow reads the same cache, so §4.5's SSE
// re-check inherits the poisoning too.
//
// The interleaving is forced deterministically with a blocking fake rather than
// stress-looped: a stress test can only ever show the race is RARE, never that
// it is closed.

// blockingSettings lets a test park exactly one Get mid-flight, so the
// invalidation can be landed at the one instant that matters.
type blockingSettings struct {
	*fakeSettings

	// entered is closed by Get once it is inside the read, i.e. once the
	// caller has definitively released the store mutex.
	entered chan struct{}
	// release blocks Get until the test closes it.
	release chan struct{}
	// blockKey is the one key that blocks; every other key reads normally.
	blockKey string
	tripped  bool
}

func newBlockingSettings(key string) *blockingSettings {
	return &blockingSettings{
		fakeSettings: newFakeSettings(),
		entered:      make(chan struct{}),
		release:      make(chan struct{}),
		blockKey:     key,
	}
}

func (b *blockingSettings) Get(ctx context.Context, key string) (string, error) {
	// Only the FIRST read of the watched key blocks. The write's own
	// invalidation path and any later re-read must not deadlock.
	if key != b.blockKey || b.tripped {
		return b.fakeSettings.Get(ctx, key)
	}
	b.tripped = true

	// Capture the value BEFORE parking. This is what makes the fake model a
	// slow read rather than a delayed one: a real in-flight SELECT has already
	// observed the pre-write row, so it must return the OLD value even though
	// the write lands before it returns. Reading after the park would observe
	// the new value and the race would not be reproduced at all.
	value, err := b.fakeSettings.Get(ctx, key)

	close(b.entered)
	<-b.release
	return value, err
}

// A PIN read that is overtaken by a PIN change must not cache the value it read
// before the change.
func TestPinHashDoesNotCacheAValueRacedByAnInvalidation(t *testing.T) {
	ctx := context.Background()
	fake := newBlockingSettings(PinHashKey)
	fake.values[PinHashKey] = "OLD-HASH"
	store := NewStore(fake)

	read := make(chan string, 1)
	go func() {
		hash, err := store.PinHash(ctx)
		if err != nil {
			t.Errorf("PinHash: %v", err)
		}
		read <- hash
	}()

	// The reader is now inside the DB read, holding no lock, about to come
	// back with OLD-HASH.
	<-fake.entered

	// The write lands entirely within that window.
	if err := store.settings.Set(ctx, PinHashKey, "NEW-HASH"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	store.invalidatePin()

	close(fake.release)
	if got := <-read; got != "OLD-HASH" {
		t.Fatalf("the racing read returned %q, want OLD-HASH — the test did not reproduce the race", got)
	}

	// THE ASSERTION. A subsequent read must see the new value. If the racing
	// read cached its stale result and marked it valid, this returns OLD-HASH
	// and the PIN change silently never took effect.
	got, err := store.PinHash(ctx)
	if err != nil {
		t.Fatalf("PinHash: %v", err)
	}
	if got != "NEW-HASH" {
		t.Fatalf("PinHash = %q, want NEW-HASH — a read that raced an invalidation "+
			"poisoned the cache with a pre-change value, so the PIN change never took "+
			"effect and the OLD PIN still unlocks every locked section", got)
	}
}

// The same race on the locked-section set: a "lock everything" write overtaken
// by an in-flight read must still take effect.
func TestLockedSectionsDoesNotCacheAValueRacedByAnInvalidation(t *testing.T) {
	ctx := context.Background()
	fake := newBlockingSettings(SectionsKey)
	fake.values[SectionsKey] = `[]`
	store := NewStore(fake)

	read := make(chan Set, 1)
	go func() {
		set, err := store.LockedSections(ctx)
		if err != nil {
			t.Errorf("LockedSections: %v", err)
		}
		read <- set
	}()

	<-fake.entered

	if err := store.settings.Set(ctx, SectionsKey, `["adult-content"]`); err != nil {
		t.Fatalf("Set: %v", err)
	}
	store.invalidateSections()

	close(fake.release)
	if got := <-read; got.Len() != 0 {
		t.Fatalf("the racing read returned %v, want the empty pre-change set — "+
			"the test did not reproduce the race", got.Sorted())
	}

	got, err := store.LockedSections(ctx)
	if err != nil {
		t.Fatalf("LockedSections: %v", err)
	}
	if !got.Has(SectionAdultContent) {
		t.Fatalf("LockedSections = %v, want adult-content locked — a read that raced an "+
			"invalidation poisoned the cache with a pre-change value, so the section stayed "+
			"open and the SSE re-check never terminated its streams", got.Sorted())
	}
}

// Invalidate (the exported wipe-and-restore hook) must bump the generation too,
// or the same poisoning is reachable through it.
func TestInvalidateBumpsTheGeneration(t *testing.T) {
	ctx := context.Background()
	fake := newBlockingSettings(PinHashKey)
	fake.values[PinHashKey] = "OLD-HASH"
	store := NewStore(fake)

	read := make(chan struct{})
	go func() {
		defer close(read)
		if _, err := store.PinHash(ctx); err != nil {
			t.Errorf("PinHash: %v", err)
		}
	}()

	<-fake.entered
	if err := store.settings.Set(ctx, PinHashKey, "NEW-HASH"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	store.Invalidate()
	close(fake.release)
	<-read

	got, err := store.PinHash(ctx)
	if err != nil {
		t.Fatalf("PinHash: %v", err)
	}
	if got != "NEW-HASH" {
		t.Fatalf("PinHash = %q, want NEW-HASH — Invalidate does not bump the generation, "+
			"so a racing read can still cache a pre-wipe value", got)
	}
}

// The counter must not make the cache useless: an UNRACED read still caches, or
// Layer 1 pays two database reads on every request in the app.
func TestUnracedReadStillCaches(t *testing.T) {
	ctx := context.Background()
	fake := newFakeSettings()
	fake.values[PinHashKey] = "HASH"
	store := NewStore(fake)

	for i := 0; i < 3; i++ {
		if _, err := store.PinHash(ctx); err != nil {
			t.Fatalf("PinHash: %v", err)
		}
	}
	if got := fake.calls(PinHashKey); got != 1 {
		t.Fatalf("PinHash hit the settings store %d times over 3 calls, want 1 — the "+
			"generation check is discarding reads that never raced anything, which puts "+
			"a database read back on every request", got)
	}
}
