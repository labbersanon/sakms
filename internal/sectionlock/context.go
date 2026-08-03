package sectionlock

import (
	"context"
	"errors"
	"time"
)

// ErrSectionLocked is what Layer 2 and Layer 3 return for a request that a
// locked section covers. The API layer renders it as
// 403 {"error":"section locked","code":"section_locked","section":"<id>"}.
//
// 403 rather than 401 is a documented deviation from the spec's "rejected
// exactly as an unauthenticated one would be": the frontend's client.ts
// reboots the SPA on any non-/api/auth/ 401, so a 401 here would log the
// operator out or boot-loop. The confidentiality property survives through
// ORDERING instead — primary auth runs first and answers 401, the section
// gate runs second and answers 403, so an unauthenticated caller never
// learns anything about section-lock state.
var ErrSectionLocked = errors.New("section locked")

// Decision is the resolved section-lock state for ONE request, computed
// once by Layer 1 and carried down in the request context.
//
// It exists so that Layers 2 and 3 need no second decrypt/verify path. A
// second path is not merely wasteful — it is how the two layers drift
// apart and one of them starts saying yes when the other says no.
type Decision struct {
	// Enforcing is false when the lock is inert: SAKMS_SECTION_LOCK_DISABLE
	// is set, or the instance runs auth mode "none". A non-enforcing
	// decision never denies anything.
	//
	// Why "none" is inert (GATE-1, decided by Wade — the plan's default,
	// Option A): the "none" banner already declares the instance fully
	// open, and there is no authenticated caller to bound a config write,
	// an unlock, or the failure counter — so enforcing there would create
	// an unauthenticated remote-brick surface and false assurance.
	Enforcing bool

	// Locked is the set of currently locked sections. Populated even when
	// Unlocked is true, so the frontend can still draw lock badges.
	Locked Set

	// Unlocked is true when this request presented a valid credential —
	// either a live unlock ticket or a correct PIN header.
	//
	// ONE ticket unlocks EVERY locked section. There is deliberately no
	// per-section scope here: with a single shared secret, per-section
	// scoping would be security theatre and a de-facto role layer.
	Unlocked bool

	// ExpiresAt is the ticket's absolute expiry, zero when the request was
	// unlocked by a per-request PIN header instead. SSE handlers use it to
	// terminate a stream at TTL without re-verifying anything.
	ExpiresAt time.Time

	// Epoch is the Gate's revocation counter as it stood when this request
	// was decided. ONLY the SSE re-check reads it: a ticker whose captured
	// Decision carries an older epoch than the Gate's current one knows its
	// captured ticket has been explicitly revoked (POST /api/section-lock/lock
	// or DELETE /api/section-lock/pin) even though that ticket still decrypts
	// and has not expired. See Gate.Revoke for why a stateless ticket needs
	// an out-of-band counter to be revocable at all.
	//
	// Zero on a non-enforcing decision, which never revokes anything.
	Epoch int64

	// Err carries a lockout or wrong-PIN result so the unlock endpoint can
	// answer distinguishably (an active lockout is not a plain wrong PIN),
	// and so an SSE handler does not mistake one for the other. It is NOT
	// the gate's allow/deny signal — Allows is.
	Err error
}

// Allows reports whether a request classified into sections may proceed.
func (d Decision) Allows(sections Set) bool {
	if !d.Enforcing || len(sections) == 0 {
		return true
	}
	if !d.Locked.Intersects(sections) {
		return true
	}
	return d.Unlocked
}

// FirstLocked returns one locked section id from sections, for the
// "section" field of the rejection body. Deterministic so the response is
// stable across requests.
func (d Decision) FirstLocked(sections Set) string {
	for _, id := range sections.Sorted() {
		if d.Locked.Has(id) {
			return id
		}
	}
	return ""
}

type contextKey struct{}

// WithDecision attaches d to ctx. Called once, by Layer 1.
func WithDecision(ctx context.Context, d Decision) context.Context {
	return context.WithValue(ctx, contextKey{}, d)
}

// FromContext returns the Decision Layer 1 attached, and whether there was
// one.
func FromContext(ctx context.Context) (Decision, bool) {
	d, ok := ctx.Value(contextKey{}).(Decision)
	return d, ok
}

type identityKey struct{}

// WithIdentity attaches the brute-force lockout bucket to ctx.
//
// Called once, by auth.Middleware, from the credential that ACTUALLY
// authenticated the request — and only after that credential has been
// verified. It is attached UNCONDITIONALLY, on every request that clears
// primary auth, whether or not a section gate is installed: routes that verify
// a PIN outside the gate (the section-lock control surface, PUT
// /api/auth/mode) and the API-key rotation route all read it, and those are
// mounted on middlewares that may carry no gate at all.
//
// See Identity for why the bucket cannot be re-derived from the raw request.
func WithIdentity(ctx context.Context, identity string) context.Context {
	return context.WithValue(ctx, identityKey{}, identity)
}

// IdentityFrom returns the identity auth.Middleware resolved, and whether
// there was one. Absent on any request that did not pass through
// auth.Middleware — a background scheduler, auth mode "none", or a test mux
// with no middleware.
func IdentityFrom(ctx context.Context) (string, bool) {
	id, ok := ctx.Value(identityKey{}).(string)
	if !ok || id == "" {
		return "", false
	}
	return id, true
}

// RequireSection is Layer 2/3's check: nil to proceed, ErrSectionLocked to
// deny.
//
// # An ABSENT decision ALLOWS, deliberately
//
// Four background schedulers call mode.Build from context.Background(),
// and a PIN lock must not stop background work — nor 500 the existing
// tests of all 32 Build call sites. Fail-closed-on-absent would do both.
//
// This is safe because Layer 1 is INDEPENDENT and PRIMARY: it gates every
// request that reaches an auth.Middleware-wrapped mux, which is every
// production mount. Layers 2 and 3 only add coverage for cases Layer 1
// structurally cannot see — a mode that lives in a proposal row or a
// request body rather than in the URL. The residual (a Build caller
// mounted on an un-middlewared mux) is closed by the enforcement-coverage
// static test, SL-10, not by this function.
func RequireSection(ctx context.Context, section string) error {
	d, ok := FromContext(ctx)
	if !ok {
		return nil
	}
	if d.Allows(NewSet(section)) {
		return nil
	}
	return ErrSectionLocked
}

// RequireAdult is Layer 3's check for handlers whose Adult scope is not
// visible in the URL path — the RSS feed rows carrying target:"adult", the
// grab rows carrying mode, the proposal handlers that never call
// mode.Build.
func RequireAdult(ctx context.Context) error {
	return RequireSection(ctx, SectionAdultContent)
}

// RequireMode is Layer 2's check, called by mode.Build immediately after
// its unknown-mode switch. Pass string(m); this package takes a string
// rather than a mode.Mode so internal/mode can import it without a cycle.
//
// Every non-Adult mode passes straight through: Build is a VALIDATION
// chokepoint, not an authorization one, and treating it as the latter was
// the root error of an earlier draft of this design.
func RequireMode(ctx context.Context, m string) error {
	if m != "adult" {
		return nil
	}
	return RequireAdult(ctx)
}
