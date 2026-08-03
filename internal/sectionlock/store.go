package sectionlock

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"golang.org/x/crypto/bcrypt"

	"github.com/labbersanon/sakms/internal/settings"
)

// Settings keys. Both live in the flat settings KV table — this feature
// allocates no migration.
const (
	// PinHashKey holds the bcrypt hash of the one PIN. Empty (or unset)
	// means no PIN has been set.
	PinHashKey = "section_lock_pin_hash"

	// SectionsKey holds a JSON array of canonical section ids.
	SectionsKey = "section_lock_sections"
)

// MinPinLength is enforced server-side, not just in the panel — the API is
// reachable by curl.
const MinPinLength = 6

var (
	// ErrPinTooShort is returned by SetPin for a PIN under MinPinLength.
	ErrPinTooShort = fmt.Errorf("sectionlock: PIN must be at least %d characters", MinPinLength)

	// ErrLockConfigUnavailable means the lock configuration could not be
	// read or made sense of. Callers MUST fail closed on it — deny the
	// request — never fall through to a computed default.
	//
	// This is the deliberate divergence from adult_mode_enabled, which
	// tolerates a malformed stored value and falls through to a default
	// (internal/api/adult_mode.go). That is correct for a pure visibility
	// switch and wrong for a security boundary: a corrupted value there
	// shows a tab, here it would unlock one.
	ErrLockConfigUnavailable = errors.New("sectionlock: lock configuration unavailable")
)

// SettingsStore is the subset of *settings.Store this package needs.
// Narrow by design so tests can inject failures without a database.
type SettingsStore interface {
	Get(ctx context.Context, key string) (string, error)
	Set(ctx context.Context, key, value string) error
}

// Store owns the two settings keys and caches both for the process's
// lifetime.
//
// The cache is not an optimisation nicety: Layer 1 runs on EVERY API
// request, and settings.Store.Get is an uncached SELECT per call with no
// caching anywhere in that package — so without this, the gate would add
// two synchronous database reads to every single request in the app.
// Invalidation is trivially correct because this package is the only
// writer of either key: every write here drops the cached value.
// Claude 2026-08-03: added the gen counter to close a cache TOCTOU.
// Reason: both accessors below RELEASE the mutex to do the database read, then
// re-acquire it and store the result unconditionally. A reader that missed the
// cache, read the OLD value, and was overtaken by a writer that stored a NEW
// value and invalidated, would then overwrite the cache with the stale value it
// had in flight AND mark it valid. The stale value survives until the next
// write or a restart — so a PIN change silently does not take effect (the old
// PIN keeps working) or a "lock everything" silently does not (the newly-locked
// sections keep passing). Gate.LockedNow reads the same cache, so §4.5's SSE
// re-check inherits the poisoning and an open stream is not torn down either.
// Troubleshooting: only reachable under concurrent read+write, which is the
// normal state of this cache — Layer 1 reads it on EVERY request.
// Review if: the read ever moves back inside the lock, which would make the
// window nonexistent and the counter redundant.
type Store struct {
	settings SettingsStore

	mu            sync.Mutex
	pinHash       string
	pinHashCached bool
	sections      Set
	sectionsValid bool

	// gen is bumped by every invalidation. A reader captures it before
	// releasing the mutex and discards its result if it has moved by the time
	// the read completes — the read raced a write, so what it holds may be
	// pre-write. Discarding is always safe: the next call simply re-reads.
	//
	// ONE counter covers BOTH keys rather than one counter each. That is
	// deliberately more conservative than strictly necessary — a sections write
	// also discards an in-flight PIN read — and the cost of being wrong in that
	// direction is one extra database read, against a correctness bug in the
	// other. It is not an oversight.
	gen uint64
}

// NewStore builds a Store over the app's settings KV store.
func NewStore(s SettingsStore) *Store {
	return &Store{settings: s}
}

// PinHash returns the stored bcrypt hash, or "" if no PIN is set.
func (s *Store) PinHash(ctx context.Context) (string, error) {
	s.mu.Lock()
	if s.pinHashCached {
		hash := s.pinHash
		s.mu.Unlock()
		return hash, nil
	}
	// Captured under the lock, immediately before releasing it — see gen.
	startGen := s.gen
	s.mu.Unlock()

	raw, _, err := s.read(ctx, PinHashKey)
	if err != nil {
		return "", err
	}
	hash := strings.TrimSpace(raw)

	s.mu.Lock()
	// An invalidation landed while this read was in flight, so hash may predate
	// it. Return it to THIS caller (it was correct when read, and the caller
	// asked for a value, not for the latest one) but do not cache it — caching
	// would mark a possibly-stale value valid for the process's lifetime.
	if s.gen == startGen {
		s.pinHash, s.pinHashCached = hash, true
	}
	s.mu.Unlock()
	return hash, nil
}

// PinSet reports whether a PIN exists. The hash itself is never returned
// over the API — status reports this boolean only.
func (s *Store) PinSet(ctx context.Context) (bool, error) {
	hash, err := s.PinHash(ctx)
	if err != nil {
		return false, err
	}
	return hash != "", nil
}

// SetPin hashes and stores pin (bcrypt at DefaultCost — the repo's
// existing password primitive, not new crypto).
func (s *Store) SetPin(ctx context.Context, pin string) error {
	if len(pin) < MinPinLength {
		return ErrPinTooShort
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(pin), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("sectionlock: hashing PIN: %w", err)
	}
	if err := s.settings.Set(ctx, PinHashKey, string(hash)); err != nil {
		return fmt.Errorf("sectionlock: saving PIN hash: %w", err)
	}
	s.invalidatePin()
	return nil
}

// ClearPin removes the PIN AND the locked-section set, in that order.
//
// Clearing one without the other is the brick: a non-empty locked set with
// no PIN in existence denies every gated route with no credential that
// could ever satisfy it. §6's recovery invariant is that clearing the PIN
// by ANY path also clears the sections, so it is enforced here, at the
// bottom, rather than in each of the routes that can reach it.
func (s *Store) ClearPin(ctx context.Context) error {
	if err := s.settings.Set(ctx, SectionsKey, "[]"); err != nil {
		return fmt.Errorf("sectionlock: clearing locked sections: %w", err)
	}
	s.invalidateSections()
	if err := s.settings.Set(ctx, PinHashKey, ""); err != nil {
		return fmt.Errorf("sectionlock: clearing PIN hash: %w", err)
	}
	s.invalidatePin()
	return nil
}

// LockedSections returns the currently locked section ids.
//
// Failure policy, split — this is where the divergence from
// adult_mode_enabled bites:
//
//   - A TRANSIENT read error (SQLite "database is locked" during a Dedup
//     scan, say) is retried exactly once, then fails closed.
//   - A MALFORMED stored value fails closed immediately, with no retry and
//     no fall-through-to-default tolerance. Re-reading a corrupt value
//     cannot un-corrupt it.
//
// Both surface as ErrLockConfigUnavailable. An UNKNOWN id inside a
// well-formed array is kept rather than dropped (a temporarily-removed tab
// re-locks if it returns); it gates nothing, because classification only
// ever emits canonical ids.
func (s *Store) LockedSections(ctx context.Context) (Set, error) {
	s.mu.Lock()
	if s.sectionsValid {
		cached := s.sections
		s.mu.Unlock()
		return cached, nil
	}
	// Captured under the lock, immediately before releasing it — see gen.
	startGen := s.gen
	s.mu.Unlock()

	raw, _, err := s.read(ctx, SectionsKey)
	if err != nil {
		return nil, err
	}
	parsed, err := parseSections(raw)
	if err != nil {
		// Deliberately NOT cached: a corrupt value must not poison the
		// cache such that a corrected write cannot recover without a
		// restart.
		return nil, err
	}

	s.mu.Lock()
	// An invalidation landed while this read was in flight — see PinHash for
	// why the value is still returned but not cached.
	if s.gen == startGen {
		s.sections, s.sectionsValid = parsed, true
	}
	s.mu.Unlock()
	return parsed, nil
}

// SetSections replaces the locked-section set.
//
// Callers are responsible for §6's "non-empty requires a PIN" check — it
// lives at the route so it can return 400 with a message, and so this
// method stays usable by ClearPin's own write ordering.
func (s *Store) SetSections(ctx context.Context, ids []string) error {
	if ids == nil {
		ids = []string{}
	}
	encoded, err := json.Marshal(ids)
	if err != nil {
		return fmt.Errorf("sectionlock: encoding locked sections: %w", err)
	}
	if err := s.settings.Set(ctx, SectionsKey, string(encoded)); err != nil {
		return fmt.Errorf("sectionlock: saving locked sections: %w", err)
	}
	s.invalidateSections()
	return nil
}

// Invalidate drops both cached values. Exported for the one case this
// package cannot see: a wipe or restore that rewrites the settings table
// underneath a running process.
func (s *Store) Invalidate() {
	s.mu.Lock()
	s.pinHash, s.pinHashCached = "", false
	s.sections, s.sectionsValid = nil, false
	s.gen++
	s.mu.Unlock()
}

func (s *Store) invalidatePin() {
	s.mu.Lock()
	s.pinHash, s.pinHashCached = "", false
	s.gen++
	s.mu.Unlock()
}

func (s *Store) invalidateSections() {
	s.mu.Lock()
	s.sections, s.sectionsValid = nil, false
	s.gen++
	s.mu.Unlock()
}

// read fetches key, treating "unset" as an empty value and retrying a
// transient failure exactly once before failing closed.
func (s *Store) read(ctx context.Context, key string) (value string, found bool, err error) {
	value, err = s.settings.Get(ctx, key)
	switch {
	case err == nil:
		return value, true, nil
	case errors.Is(err, settings.ErrNotFound):
		return "", false, nil
	}

	// One retry, then closed. Once — not a loop: the gate is on the hot
	// path of every request, and a genuinely down database must surface as
	// a denial rather than as a request that hangs retrying.
	value, err = s.settings.Get(ctx, key)
	switch {
	case err == nil:
		return value, true, nil
	case errors.Is(err, settings.ErrNotFound):
		return "", false, nil
	}
	return "", false, fmt.Errorf("%w: reading %s: %v", ErrLockConfigUnavailable, key, err)
}

// parseSections decodes the stored JSON array.
//
// The boundary between "empty" and "malformed" is load-bearing, because
// fail-closed makes a false malformed reading a brick:
//
//	""      unset or cleared  -> empty
//	"null"  a cleared value   -> empty (json.Unmarshal yields nil, no error)
//	"[]"    explicitly empty  -> empty
//	"{}"    wrong shape       -> MALFORMED
//	"[1,2]" wrong element type-> MALFORMED
func parseSections(raw string) (Set, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return NewSet(), nil
	}
	var ids []string
	if err := json.Unmarshal([]byte(trimmed), &ids); err != nil {
		return nil, fmt.Errorf("%w: malformed %s (%v)", ErrLockConfigUnavailable, SectionsKey, err)
	}
	return NewSet(ids...), nil
}

// VerifyPinHash reports whether pin matches a stored bcrypt hash. Returns
// false for an empty hash (no PIN set) without paying the bcrypt cost.
func VerifyPinHash(hash, pin string) bool {
	if hash == "" {
		return false
	}
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(pin)) == nil
}
