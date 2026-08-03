package sectionlock

import (
	"context"
	"net/http"
	"time"
)

// StreamRecheckInterval is how often a long-lived SSE handler re-checks its
// own section (§4.5). 30s is a deliberate compromise: short enough that a
// re-lock takes effect while the operator is still looking at the screen,
// long enough that four idle streams cost four cache reads a minute rather
// than four a second.
const StreamRecheckInterval = 30 * time.Second

// LiveLookup is the ONE narrow exception to this package's "Layers 2 and 3
// read the request context and nothing else" rule.
//
// # Why an exception is unavoidable here
//
// A Decision is a snapshot frozen at request start. That is exactly right
// for a request that lives for milliseconds, and structurally insufficient
// for an SSE stream that lives for the tab's lifetime: the snapshot can
// compute TTL expiry from its carried exp, but it cannot observe a config
// change that locks a section AFTER the stream opened. A stream that
// opened while Adult was unlocked would otherwise keep streaming Adult
// progress frames forever.
//
// # Why this is still not a "second verify path"
//
// The prohibition §4.2 states is on a second DECRYPT/VERIFY path — a
// second place that decides what a credential means, which is how two
// layers drift apart until one says yes while the other says no. This
// interface exposes neither: LockedNow is a read of the same
// process-lifetime configuration cache Layer 1 reads, and ValidTicket
// delegates to the same ValidateTicket Layer 1 calls. There is no second
// implementation of anything.
//
// Only SSE handlers may use it. Every other caller reads the context
// Decision via RequireSection/RequireAdult/RequireMode.
type LiveLookup interface {
	// LockedNow returns the CURRENTLY locked set, not the set that was
	// locked when the stream opened.
	LockedNow(ctx context.Context) (Set, error)

	// ValidTicket reports whether token is still a live unlock ticket.
	ValidTicket(token string) (time.Time, bool)

	// Epoch returns the CURRENT revocation epoch, to be compared against the
	// one the stream's Decision captured at open time. See Gate.Revoke.
	Epoch() int64
}

type liveKey struct{}

// WithLive attaches the live lookup to ctx. Called once, by Layer 1, on the
// same success exit that carries the Decision.
func WithLive(ctx context.Context, l LiveLookup) context.Context {
	return context.WithValue(ctx, liveKey{}, l)
}

// LiveFrom returns the live lookup Layer 1 attached, and whether there was
// one. Absent on every request that did not pass through a gated
// auth.Middleware — a disarmed instance, auth mode "none", or a test mux
// with no middleware at all.
func LiveFrom(ctx context.Context) (LiveLookup, bool) {
	l, ok := ctx.Value(liveKey{}).(LiveLookup)
	return l, ok
}

// StreamRevoked reports whether an SSE stream that Layer 1 ALLOWED at open
// must now terminate. sections is the stream's own section set — pass
// Classify(r.URL.Path), the same classification Layer 1 used, so a stream
// and its gate can never disagree about what a route covers.
//
// Three termination conditions — §4.5's two, plus the explicit revocation
// §4.5 assumed the first two already covered:
//
//  1. The carried unlock ticket's absolute 30-minute TTL has expired.
//  2. A configuration change has newly locked the stream's section since
//     the stream opened.
//  3. The operator explicitly revoked — POST /api/section-lock/lock ("Re-lock
//     now") or DELETE /api/section-lock/pin. Neither can invalidate the ticket
//     VALUE (it is stateless and sealed, with no server-side registry), and an
//     open stream re-checks the ticket it CAPTURED at open time rather than the
//     browser's current cookie — so without this condition "Re-lock now" is
//     visibly ignored by every stream already running, which is QA-GATE-1 step
//     7 failing. Gate.Revoke documents the counter that closes it.
//
// # Read the ticket, NEVER decision.Unlocked
//
// Gate.Decide returns EARLY — before it ever looks at the ticket — when
// nothing is locked, so Decision.Unlocked is false for a genuinely unlocked
// operator on an unlocked instance. That is precisely the state condition 2
// starts from, so keying on Unlocked would tear down the stream of every
// operator holding a perfectly valid ticket the instant anything was
// locked. internal/api's section-lock status handler documents the same
// trap and takes the same way out.
//
// One ValidTicket call settles BOTH conditions at once, because
// ValidateTicket already rejects an expired ticket.
//
// # A stream with an empty section set can never be revoked
//
// GET /api/notifications/stream classifies as no section at all,
// deliberately (R-3: it is one global stream, not a per-section one). Its
// ticker therefore never terminates anything, and that is correct rather
// than a gap: Layer 1 does not gate that route either, so a terminated
// stream would be re-established by the browser's own EventSource
// reconnect within a second. Terminating it would be pure churn, not
// enforcement. The ticker is still wired there so that the day
// classification changes, the behaviour follows without a second edit.
func StreamRevoked(r *http.Request, sections Set) bool {
	ctx := r.Context()
	if len(sections) == 0 {
		return false
	}
	decision, ok := FromContext(ctx)
	if !ok || !decision.Enforcing {
		return false
	}
	live, ok := LiveFrom(ctx)
	if !ok {
		return false
	}
	locked, err := live.LockedNow(ctx)
	if err != nil {
		// Fail closed. A short-lived request answers 500 for an unreadable
		// configuration; a stream has no status line left to change, so the
		// equivalent is to hang up. An unattended stream is the worst place
		// to keep serving on a configuration nobody can read.
		return true
	}
	if !locked.Intersects(sections) {
		return false
	}
	// Condition 3. The revocation check GUARDS the ticket check rather than
	// replacing it: a live ticket keeps the stream open exactly as before,
	// unless it was issued under an older epoch than the one in force now.
	//
	// Deliberately NOT applied to the header branch below. §4.5 says a
	// revocation invalidates in-flight TICKETS; an X-Section-Pin caller holds
	// the PIN itself, the very credential a re-lock demands, and DELETE /pin —
	// the case where that PIN stops existing — also clears the locked set
	// (Store.ClearPin), so such a stream has already returned above on the
	// no-intersection check.
	if live.Epoch() == decision.Epoch {
		if _, ok := live.ValidTicket(TicketFrom(r)); ok {
			return false
		}
	}
	// Opened with a correct X-Section-Pin header rather than a cookie. A
	// per-request header carries no lifetime of its own (ExpiresAt is zero
	// by construction on that path), so there is no TTL to expire and the
	// caller already proved it holds the PIN — the credential a re-lock
	// would demand. EventSource cannot send headers, so this is only ever a
	// curl-shaped client.
	if decision.Unlocked && decision.ExpiresAt.IsZero() {
		return false
	}
	return true
}
