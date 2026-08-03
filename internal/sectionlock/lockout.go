package sectionlock

import (
	"crypto/sha256"
	"encoding/hex"
	"net"
	"net/http"
	"sync"
	"time"
)

const (
	// failureThreshold is how many CONSECUTIVE wrong PINs trip a refusal.
	failureThreshold = 5

	// baseBackoff is the first refusal window; each further failure
	// doubles it, up to maxBackoff.
	baseBackoff = 60 * time.Second
	maxBackoff  = 15 * time.Minute

	// maxTrackedIdentities / identityRetention bound the counter map. It
	// is keyed partly by client IP, so without a bound a long-running
	// process on a busy network grows it without limit.
	maxTrackedIdentities = 256
	identityRetention    = time.Hour
)

// Lockout is the brute-force counter: consecutive wrong PINs per identity.
//
// It lives in PROCESS MEMORY and clears on restart. That is a deliberate
// consequence of the threat model — restarting SAK requires host access,
// which is already well past the household-member/shared-screen attacker
// this feature defends against — and it keeps a security control out of
// the database, where a corrupt row could brick the operator out.
//
// # Per-identity, never global
//
// A GLOBAL counter would let the stated threat actor — someone with a
// shared screen or LAN access — self-DoS the operator indefinitely by
// spamming wrong PINs from their own browser. Keying on the presented
// primary credential (see Identity) means one person's failures cannot
// refuse anybody else's correct PIN.
//
// # A missing PIN is not a failure
//
// Only a WRONG PIN increments. A request carrying no PIN at all must not
// count, because the browser's own routine gated requests carry no PIN
// header by design — counting them would inflict a permanent lockout
// within seconds of locking a section.
type Lockout struct {
	mu      sync.Mutex
	entries map[string]*lockoutEntry

	// now is injectable so backoff progression is testable without
	// sleeping through a 15-minute cap.
	now func() time.Time
}

type lockoutEntry struct {
	failures int
	backoff  time.Duration
	until    time.Time
	lastSeen time.Time
}

// NewLockout builds an empty counter.
func NewLockout() *Lockout {
	return &Lockout{entries: make(map[string]*lockoutEntry), now: time.Now}
}

// Locked reports whether identity is currently refused, and for how much
// longer.
func (l *Lockout) Locked(identity string) (time.Duration, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	entry := l.entries[identity]
	if entry == nil {
		return 0, false
	}
	now := l.now()
	if now.Before(entry.until) {
		return entry.until.Sub(now), true
	}
	return 0, false
}

// Fail records one wrong PIN for identity, tripping or extending the
// refusal window once the threshold is reached.
//
// Note the consequence of the failure count surviving an elapsed window:
// once an identity has tripped, the NEXT single wrong PIN re-trips
// immediately at the doubled duration, however long the quiet gap was. So
// an operator who fat-fingers once, hours after a bad run, can land
// straight back in a 15-minute refusal. That is deliberate — the counter
// is consecutive-failures-since-last-SUCCESS, and only a correct PIN
// clears it — but it is surprising enough to be worth knowing before
// running the manual QA lockout check.
func (l *Lockout) Fail(identity string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	entry := l.entries[identity]
	if entry == nil {
		entry = &lockoutEntry{}
		l.entries[identity] = entry
	}
	entry.failures++
	entry.lastSeen = now
	if entry.failures < failureThreshold {
		return
	}
	switch {
	case entry.backoff == 0:
		entry.backoff = baseBackoff
	case entry.backoff >= maxBackoff:
		entry.backoff = maxBackoff
	default:
		entry.backoff *= 2
		if entry.backoff > maxBackoff {
			entry.backoff = maxBackoff
		}
	}
	entry.until = now.Add(entry.backoff)
	l.pruneLocked(now)
}

// Reset clears identity's counter after a correct PIN.
//
// It drops the entry outright rather than zeroing the failure count, so
// the BACKOFF resets too — otherwise a success followed by five fresh
// failures would resume at the previously doubled window, which is not
// what "reset on success" means.
func (l *Lockout) Reset(identity string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.entries, identity)
}

// Claude 2026-08-03: added so rotating the API key cannot mint a fresh,
// never-before-seen lockout bucket.
// Reason: Identity keys on the credential that authenticated, so a NEW key is
// a NEW bucket. POST /api/apikey/regenerate is deliberately exempt from
// section gating (it is the break-glass recovery route — see
// docs/break-glass-recovery.md), so without this an attacker holding a valid
// key could spend four guesses, rotate, and repeat forever, never reaching
// failureThreshold. Moving the counter onto the new key's bucket makes the
// failure count survive the rotation.
// Troubleshooting: closes the second brute-force vector found alongside the
// unvalidated-cookie Identity bug; see apikeyRegenerateHandler.
// Review if: key rotation stops being reachable without a section PIN.
//
// Move, not copy: the old credential is dead the instant the new one is
// minted, so leaving its entry behind would only consume a tracked slot.
//
// A MISSING entry transfers nothing and creates nothing. That is what keeps
// break-glass working: an operator recovering with a freshly-minted key has
// no failure history, and seeding an empty bucket for them would be a
// gratuitous side effect on the one path that must never surprise anyone.
func (l *Lockout) Transfer(from, to string) {
	if from == "" || to == "" || from == to {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	entry := l.entries[from]
	if entry == nil {
		return
	}
	delete(l.entries, from)
	l.entries[to] = entry
}

// pruneLocked drops identities that have been quiet for identityRetention
// once the map grows past maxTrackedIdentities. Caller holds l.mu.
func (l *Lockout) pruneLocked(now time.Time) {
	if len(l.entries) <= maxTrackedIdentities {
		return
	}
	for id, entry := range l.entries {
		if now.Sub(entry.lastSeen) > identityRetention && !now.Before(entry.until) {
			delete(l.entries, id)
		}
	}
}

// Claude 2026-08-03: Identity no longer derives a bucket from raw request
// values; auth.Middleware computes it from the VERIFIED credential and passes
// it down via WithIdentity.
// Reason: the old body fingerprinted the sakms_session cookie straight off the
// request without validating it. auth.Middleware checks X-Api-Key FIRST and
// skips cookie validation entirely when the key passes — so a caller holding a
// valid API key could attach a fresh random sakms_session value to every
// PIN-guess request, land in a brand-new bucket every time, and never reach
// failureThreshold. That defeated the whole brute-force control.
// Troubleshooting: PIN-cracking time for a 6-digit PIN collapsed from
// bcrypt+lockout bounded (~decades) to bcrypt-only bounded (~an hour).
// Review if: Middleware stops being the sole gated entry point.

// Identity returns the lockout bucket for r.
//
// It reads ONLY the identity auth.Middleware resolved from the credential that
// ACTUALLY authenticated the request (see WithIdentity), and falls back to the
// client IP when there is none — a background caller, a test mux with no
// middleware, or a disarmed instance.
//
// # Nothing caller-controlled may ever become a bucket key
//
// This is the same principle the X-Forwarded-For note below states, applied to
// the session cookie as well. A value the caller can vary at will is a value
// the caller can use to mint unlimited distinct identities, and an attacker
// with unlimited identities never trips a per-identity counter. The cookie
// looked safe because a GENUINE session cookie is unforgeable — but the
// counter never checked that it was genuine, and Middleware does not either
// when an API key is what passed. So the rule is now structural rather than
// incidental: this function derives nothing from r beyond the transport-level
// RemoteAddr, and the credential-derived forms are built by KeyIdentity /
// SessionIdentity at the one place a credential has just been verified.
//
// X-Forwarded-For is deliberately NOT consulted, for exactly the same reason.
// It is caller-controlled, so honouring it would let one attacker present
// unlimited distinct identities and never trip the counter at all.
//
// The IP fallback is the weakest of the three and is the reason the counter is
// not the feature's only defence.
func Identity(r *http.Request) string {
	if id, ok := IdentityFrom(r.Context()); ok {
		return id
	}
	return IPIdentity(r)
}

// KeyIdentity is the bucket for a request an API key authenticated. presented
// must be the key that was just VERIFIED, never a raw header value that has
// not been checked.
//
// Only a fingerprint is retained — enough to tell two credentials apart as map
// keys, never enough to reconstruct one from a heap dump.
func KeyIdentity(presented string) string { return "key:" + fingerprint(presented) }

// SessionIdentity is the bucket for a request a session cookie authenticated.
// token must be a cookie value that has just passed auth.ValidateToken.
//
// Only the cookie's CIPHERTEXT is fingerprinted, never a decrypted payload:
// this runs on the hot path and must not add a second decrypt.
func SessionIdentity(token string) string { return "session:" + fingerprint(token) }

// IPIdentity is the weakest bucket — the transport-level peer address, which
// no header can influence.
func IPIdentity(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	return "ip:" + host
}

// fingerprint is a short, non-reversible tag for a credential — enough to
// tell two credentials apart as map keys, never enough to reconstruct one
// from a heap dump.
func fingerprint(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:8])
}
