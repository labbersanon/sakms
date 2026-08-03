package sectionlock

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

var (
	// ErrNoPinPresented means the request carried no PIN. It is NOT a
	// failed attempt and never touches the lockout counter.
	ErrNoPinPresented = errors.New("sectionlock: no PIN presented")

	// ErrNoPinSet means sections are locked but no PIN exists to unlock
	// them. See Decide for why this state fails closed.
	ErrNoPinSet = errors.New("sectionlock: no PIN is set")

	// ErrWrongPin is a genuine failed attempt; it increments the counter.
	ErrWrongPin = errors.New("sectionlock: incorrect PIN")

	// ErrLockedOut means the identity is inside an active refusal window.
	// Match with errors.Is; use errors.As with *LockedOutError for the
	// remaining duration.
	ErrLockedOut = errors.New("sectionlock: too many failed attempts")
)

// LockedOutError carries how long the refusal window still has to run, so
// the unlock endpoint can answer distinguishably rather than reporting a
// lockout as though it were one more wrong PIN.
type LockedOutError struct {
	RetryAfter time.Duration
}

func (e *LockedOutError) Error() string {
	return fmt.Sprintf("%s (retry in %s)", ErrLockedOut.Error(), e.RetryAfter.Round(time.Second))
}

func (e *LockedOutError) Unwrap() error { return ErrLockedOut }

// memoCapacity bounds the positive-result cache. In practice it holds one
// entry (there is one PIN); the cap exists so a PIN-change history cannot
// accumulate stale entries for the process's lifetime.
const memoCapacity = 32

// Gate combines the two credentials — unlock ticket and PIN header — into
// one allow/deny decision.
type Gate struct {
	store   *Store
	enc     AADEncryptor
	lockout *Lockout

	// memo caches POSITIVE PIN verifications only. bcrypt at DefaultCost
	// is ~60-100ms and the header path would otherwise pay it on every
	// single request an out-of-process script makes.
	//
	// Keyed on HMAC-SHA256 of the presented PIN under memoKey, a
	// process-random value — NOT a bare SHA-256. A 4-6 character PIN is
	// trivially rainbow-tableable, so a bare digest recovered from a heap
	// dump or core file would hand over the PIN itself.
	//
	// The VALUE is a fingerprint of the stored hash the PIN was verified
	// against, re-read on every verification. So changing the PIN changes
	// the fingerprint, every cached entry misses, and the change takes
	// effect immediately rather than at the next restart.
	//
	// Negative results are NEVER cached: caching them would make a wrong
	// PIN cheap to retry and would let a stale negative survive a PIN
	// change.
	memoMu  sync.Mutex
	memo    map[string]string
	memoKey []byte

	// epoch is the revocation counter. See Revoke for why an unlock ticket
	// needs one at all.
	epoch atomic.Int64
}

// Revoke bumps the revocation counter that ALREADY-OPEN SSE streams consult
// on their next periodic re-check tick, terminating them (see StreamRevoked).
//
// # What it does NOT do — read this before relying on it
//
// It does not invalidate any ticket's cryptographic validity. A ticket is a
// stateless, AES-GCM-sealed token with no server-side registry; nothing here
// reaches into one. A ticket that was valid before the revoke remains
// decryptable and unexpired afterwards, so it can still authorize a brand-new
// request, and it can still authorize a NEWLY-OPENED stream — Decide stamps
// the CURRENT epoch onto every decision it makes rather than checking the
// epoch a ticket was issued under, so a fresh stream opened with a pre-revoke
// ticket starts life with a matching epoch and is not revoked.
//
// The counter's whole reach is streams that were ALREADY open when it was
// bumped: those captured an older epoch in their frozen Decision, and that
// mismatch is what tears them down. The practical effect an operator sees
// comes from Revoke's pairing with the cookie clear at its call sites — POST
// /api/section-lock/lock and DELETE /api/section-lock/pin both delete the
// browser's copy of the ticket, so the browser has nothing left to re-present.
//
// # Why a counter, and why this is the whole mechanism
//
// The unlock ticket is a STATELESS, AES-GCM-sealed token: there is no
// server-side registry of issued tickets, so nothing can reach into a token
// already in a browser's cookie jar and mark it dead. POST
// /api/section-lock/lock clears the BROWSER'S cookie, which is the entire
// effect it can have on a stateless credential — and that is enough for
// every ordinary request, because the next one simply arrives without a
// cookie.
//
// It is NOT enough for an SSE stream. An open stream's *http.Request is
// frozen at open time, cookie header included, so its re-check ticker
// re-validates the SAME captured ticket forever. It stays cryptographically
// valid and unexpired, so the ticker keeps deciding the stream may continue
// — even after the operator has explicitly clicked "Re-lock now". That is
// QA-GATE-1 step 7 ("open the Adult dedup stream unlocked, then POST /lock →
// terminates ≤30s") failing.
//
// A monotonic counter closes it without a ticket registry: a Decision
// records the epoch it was made under, and a ticker that sees the epoch has
// advanced treats its captured ticket as dead. Scoped to this Gate rather
// than to a package var for the same reason every other piece of
// process-lifetime state here is (the brute-force counter, the bcrypt memo):
// cmd/sakms constructs exactly ONE Gate and shares it between the
// middleware, the control mux and the auth-mode mux, while every test
// constructs its own — a package var would leak revocations between tests.
//
// # What it deliberately does NOT do
//
// Survive a restart, or revoke anything for a NON-stream request. A restart
// makes every process-memory credential moot anyway (§2/R-6 accepts exactly
// this for the lockout counter), and an ordinary request re-presents its
// cookie, which POST /lock has already deleted client-side. Widening this
// into a real server-side ticket registry was priced and rejected: it is a
// much larger design change than the threat model in §2 — a household member
// on the LAN — warrants.
func (g *Gate) Revoke() { g.epoch.Add(1) }

// Epoch returns the current revocation epoch. Together with ValidTicket and
// LockedNow this satisfies LiveLookup.
func (g *Gate) Epoch() int64 { return g.epoch.Load() }

// NewGate builds a Gate. enc must implement the AAD-taking encryptor —
// see AADEncryptor for exactly what W1 adds to *secrets.Store.
// It PANICS if crypto/rand cannot produce the memo cache's HMAC key.
//
// Claude 2026-08-03: was `key = nil` on a rand failure.
// Reason: hmac.New(sha256.New, nil) is a perfectly functional DETERMINISTIC
// keyed hash, so the old fallback did not degrade the cache — it silently
// destroyed the one property the cache depends on. The key exists because a
// 4-6 character PIN is trivially rainbow-tableable; with a fixed empty key the
// memo map's keys become a plain SHA-256 of the PIN, and anyone who can read
// the process's heap recovers the PIN itself. Failing loudly is the only
// correct response to a condition that quietly removes a security property.
// Troubleshooting: silent-degradation finding, section-lock security review.
// Review if: NewGate acquires an error return for some other reason — then
// propagate rather than panic.
//
// Panic rather than an error return is deliberate and is the smaller change:
// cmd/sakms constructs exactly ONE Gate, at boot, before serving (main.go),
// so this can only ever fire during startup where a panic and a fatal error
// are the same observable outcome. An error return would instead thread a
// never-non-nil error through ten call sites, nine of them tests. On Linux a
// crypto/rand read failure is not reachable at all.
func NewGate(store *Store, enc AADEncryptor) *Gate {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		panic(fmt.Sprintf("sectionlock: crypto/rand unavailable, cannot key the PIN memo cache: %v", err))
	}
	return &Gate{
		store:   store,
		enc:     enc,
		lockout: NewLockout(),
		memo:    make(map[string]string),
		memoKey: key,
	}
}

// Store exposes the underlying configuration store, for the API routes
// that read and write it.
func (g *Gate) Store() *Store { return g.store }

// TransferLockout moves the brute-force counter from one identity to another,
// so that rotating a credential does not reset it.
//
// The one caller is POST /api/apikey/regenerate. That route is deliberately
// exempt from section gating — it is the break-glass recovery path — and it
// returns a plaintext key, so without this an attacker holding a valid key
// could guess four PINs, rotate to a fresh identity, and repeat indefinitely
// without ever reaching failureThreshold. See Lockout.Transfer.
func (g *Gate) TransferLockout(from, to string) { g.lockout.Transfer(from, to) }

// ValidTicket reports whether token is a live unlock ticket, and returns
// its absolute expiry.
func (g *Gate) ValidTicket(token string) (time.Time, bool) {
	return ValidateTicket(g.enc, token)
}

// LockedNow returns the CURRENTLY locked set — §4.5's live read, served
// from Store's process-lifetime cache rather than the request context's
// frozen Decision.
//
// Together with ValidTicket this satisfies LiveLookup, so *Gate is what
// auth.Middleware attaches for SSE handlers. It is deliberately NOT used
// anywhere else: a normal handler reading live state instead of the
// context Decision is the drift this design exists to prevent.
func (g *Gate) LockedNow(ctx context.Context) (Set, error) {
	return g.store.LockedSections(ctx)
}

// IssueTicket mints a fresh unlock ticket. The caller is responsible for
// having verified the PIN first.
func (g *Gate) IssueTicket() (string, time.Time, error) {
	return IssueTicket(g.enc)
}

// VerifyPin checks pin for identity, returning nil on success.
//
// # Ordering is load-bearing and is pinned by a test
//
// The BRUTE-FORCE LOCKOUT IS CHECKED BEFORE THE MEMO CACHE. The reverse
// order is a real bypass and an easy one to write by accident: a memo
// entry warmed by an earlier correct PIN would sail straight through an
// active refusal window, which is precisely the window an attacker who has
// just guessed the PIN on attempt six lands in.
func (g *Gate) VerifyPin(ctx context.Context, identity, pin string) error {
	if pin == "" {
		// Not a failure. The browser's own gated requests carry no PIN
		// header by design; counting them would inflict a permanent
		// lockout within seconds of locking a section.
		return ErrNoPinPresented
	}

	if retry, locked := g.lockout.Locked(identity); locked {
		return &LockedOutError{RetryAfter: retry}
	}

	hash, err := g.store.PinHash(ctx)
	if err != nil {
		return err
	}
	if hash == "" {
		return ErrNoPinSet
	}
	fingerprint := hashFingerprint(hash)

	if g.memoHit(pin, fingerprint) {
		g.lockout.Reset(identity)
		return nil
	}
	if VerifyPinHash(hash, pin) {
		g.memoPut(pin, fingerprint)
		g.lockout.Reset(identity)
		return nil
	}

	g.lockout.Fail(identity)
	return ErrWrongPin
}

// Request is the per-request input to Decide.
//
// It takes explicit values rather than an *http.Request so that Enforcing
// — which depends on the SAKMS_SECTION_LOCK_DISABLE env var and the
// instance's auth mode, neither of which this package should know about —
// stays the caller's decision, and so the whole gate is testable without
// HTTP.
type Request struct {
	// Enforcing is false when the lock is inert. See Decision.Enforcing.
	Enforcing bool

	// Ticket is the sakms_unlock cookie value, "" if absent.
	Ticket string

	// Pin is the X-Section-Pin header value, "" if absent.
	Pin string

	// Identity keys the brute-force counter — see Identity(r).
	Identity string
}

// Decide resolves one request's section-lock state.
//
// A returned error means the configuration could not be read or parsed
// (ErrLockConfigUnavailable). Callers MUST fail closed on it — deny, do
// not proceed with an empty Decision.
func (g *Gate) Decide(ctx context.Context, req Request) (Decision, error) {
	if !req.Enforcing {
		return Decision{}, nil
	}

	locked, err := g.store.LockedSections(ctx)
	if err != nil {
		return Decision{}, err
	}
	// The epoch is stamped HERE, into the one literal every enforcing return
	// path below inherits — including the nothing-is-locked early return.
	// Stamping it only on the ticket-success path would look natural ("the
	// ticket is as-of an epoch") and would reintroduce exactly the regression
	// StreamRevoked's own doc warns about: an operator who re-locked once at
	// some point in the past, then opened a stream while nothing was locked,
	// would carry a stale zero and have their stream torn down the moment
	// anything was locked, valid ticket and all.
	decision := Decision{Enforcing: true, Locked: locked, Epoch: g.epoch.Load()}
	if locked.Len() == 0 {
		// Nothing is locked, so no credential can matter. Returning here
		// keeps bcrypt off the hot path of every request on the
		// overwhelmingly common install where the feature is unused.
		return decision, nil
	}

	hash, err := g.store.PinHash(ctx)
	if err != nil {
		return Decision{}, err
	}
	if hash == "" {
		// Sections locked with no PIN in existence. Nothing can satisfy
		// the gate, so this DENIES — fail closed, deliberately, rather
		// than treating "no PIN" as "not enforcing".
		//
		// The state is unreachable through the API: PUT /sections rejects
		// a non-empty array while no PIN is set, and clearing the PIN by
		// any path also clears the sections (Store.ClearPin). It is
		// reachable by a corrupt bcrypt hash or a hand-edited database,
		// and for those SAKMS_SECTION_LOCK_DISABLE=1 is the documented —
		// and only — way out.
		decision.Err = ErrNoPinSet
		return decision, nil
	}

	// Ticket first, for two reasons — one an optimisation, one a
	// deliberate security decision.
	//
	// The optimisation: a browser request pays no bcrypt at all.
	//
	// The decision: a valid ticket DELIBERATELY BYPASSES THE BRUTE-FORCE
	// LOCKOUT. The lockout bounds GUESSING, and a ticket is proof the PIN
	// was already entered correctly — so refusing it during a refusal
	// window would let anyone who can reach the API self-DoS the already
	// unlocked operator by spamming wrong PINs, which is the exact
	// shared-screen self-DoS the per-identity counter exists to prevent.
	//
	// This is NOT the ordering pinned by §3.3. That rule — lockout before
	// the MEMO CACHE — is about the PIN path below, where a warmed cache
	// would otherwise let a guesser through. Do not "fix" the apparent
	// inconsistency by hoisting the lockout check above this branch;
	// TestValidTicketBypassesLockout is what says so.
	if expiry, ok := g.ValidTicket(req.Ticket); ok {
		decision.Unlocked = true
		decision.ExpiresAt = expiry
		return decision, nil
	}

	if req.Pin == "" {
		return decision, nil
	}
	if err := g.VerifyPin(ctx, req.Identity, req.Pin); err != nil {
		decision.Err = err
		return decision, nil
	}
	// Unlocked per-request; ExpiresAt stays zero because a header carries
	// no lifetime of its own.
	decision.Unlocked = true
	return decision, nil
}

func (g *Gate) memoHit(pin, fingerprint string) bool {
	key := g.memoKeyFor(pin)
	g.memoMu.Lock()
	defer g.memoMu.Unlock()
	return g.memo[key] == fingerprint
}

func (g *Gate) memoPut(pin, fingerprint string) {
	key := g.memoKeyFor(pin)
	g.memoMu.Lock()
	defer g.memoMu.Unlock()
	if len(g.memo) >= memoCapacity {
		g.memo = make(map[string]string, 1)
	}
	g.memo[key] = fingerprint
}

func (g *Gate) memoKeyFor(pin string) string {
	mac := hmac.New(sha256.New, g.memoKey)
	mac.Write([]byte(pin))
	return hex.EncodeToString(mac.Sum(nil))
}

// hashFingerprint tags a stored bcrypt hash so a PIN change is detectable
// without comparing (or retaining) the hash itself.
func hashFingerprint(hash string) string {
	sum := sha256.Sum256([]byte(hash))
	return hex.EncodeToString(sum[:16])
}
