package sectionlock

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// stream_test.go — SL-18's unit half: the decision rule §4.5's SSE tickers
// all share.
//
// The TTL branch lives here rather than in internal/api because forging an
// ALREADY-EXPIRED ticket needs issueTicketAt, which is unexported. An
// integration test cannot wait 30 real minutes, so testing it there would
// mean either exporting a clock seam for one assertion or not testing the
// branch at all. The config-change branch is exercised end-to-end against a
// live SSE handler in internal/api/sectionlock_w2_test.go; between the two
// files SL-18 has both halves.

const streamTestPath = "/api/modes/adult/dedup/scan/stream"

// streamRequest builds the request a gated auth.Middleware would have handed
// an SSE handler: the ticket in a cookie, the Decision and the live lookup in
// the context.
func streamRequest(token string, d Decision, live LiveLookup) *http.Request {
	r := httptest.NewRequest(http.MethodGet, streamTestPath, nil)
	if token != "" {
		r.AddCookie(&http.Cookie{Name: CookieName, Value: token})
	}
	ctx := WithDecision(r.Context(), d)
	if live != nil {
		ctx = WithLive(ctx, live)
	}
	return r.WithContext(ctx)
}

// failingLookup stands in for a gate whose configuration cannot be read.
type failingLookup struct{ inner LiveLookup }

func (f failingLookup) LockedNow(context.Context) (Set, error) {
	return nil, ErrLockConfigUnavailable
}

func (f failingLookup) ValidTicket(token string) (time.Time, bool) {
	return f.inner.ValidTicket(token)
}

func (f failingLookup) Epoch() int64 { return f.inner.Epoch() }

func TestStreamRevoked(t *testing.T) {
	adult := NewSet(SectionAdultContent)

	// One gate, reconfigured per case via its own store — the same object
	// production shares between the middleware and the control mux, so a
	// SetSections write here invalidates exactly the cache the ticker reads.
	newGate := func(t *testing.T, lockedIDs ...string) *Gate {
		t.Helper()
		gate, _ := newTestGate(t)
		if err := gate.Store().SetSections(context.Background(), lockedIDs); err != nil {
			t.Fatalf("SetSections: %v", err)
		}
		return gate
	}

	liveTicket := func(t *testing.T, gate *Gate) string {
		t.Helper()
		token, _, err := gate.IssueTicket()
		if err != nil {
			t.Fatalf("IssueTicket: %v", err)
		}
		return token
	}

	expiredTicket := func(t *testing.T, gate *Gate) string {
		t.Helper()
		// One minute past a TicketTTL-old issuance: expired by construction,
		// with no sleep and no clock injection in the code under test.
		token, _, err := issueTicketAt(gate.enc, time.Now().Add(-TicketTTL-time.Minute))
		if err != nil {
			t.Fatalf("issueTicketAt: %v", err)
		}
		return token
	}

	t.Run("an empty section set can never be revoked", func(t *testing.T) {
		// GET /api/notifications/stream's case: Classify returns nothing for
		// it (R-3), so its ticker is inert. Asserted so the day someone
		// classifies that route, this test fails and the behaviour is a
		// decision rather than a surprise.
		gate := newGate(t, SectionAdultContent)
		r := streamRequest("", Decision{Enforcing: true}, gate)
		if StreamRevoked(r, NewSet()) {
			t.Fatal("an unclassified stream was revoked")
		}
	})

	t.Run("a non-enforcing decision never revokes", func(t *testing.T) {
		gate := newGate(t, SectionAdultContent)
		r := streamRequest("", Decision{}, gate)
		if StreamRevoked(r, adult) {
			t.Fatal("revoked while the lock was inert")
		}
	})

	t.Run("an absent live lookup never revokes", func(t *testing.T) {
		// A request that never passed through a gated auth.Middleware — a
		// disarmed instance, auth mode none, or a test mux. Same
		// absent-allows rule Layers 2 and 3 use.
		r := streamRequest("", Decision{Enforcing: true}, nil)
		if StreamRevoked(r, adult) {
			t.Fatal("revoked with no gate installed")
		}
	})

	t.Run("nothing locked does not revoke", func(t *testing.T) {
		gate := newGate(t)
		r := streamRequest("", Decision{Enforcing: true}, gate)
		if StreamRevoked(r, adult) {
			t.Fatal("revoked while nothing was locked")
		}
	})

	t.Run("SL-18 a config change that newly locks the section revokes", func(t *testing.T) {
		// The stream opened on an instance with nothing locked, so Decide
		// returned early and left Unlocked false — then a config write locked
		// Adult underneath it.
		gate := newGate(t)
		r := streamRequest("", Decision{Enforcing: true}, gate)
		if StreamRevoked(r, adult) {
			t.Fatal("revoked before the config change")
		}
		if err := gate.Store().SetSections(context.Background(), []string{SectionAdultContent}); err != nil {
			t.Fatalf("SetSections: %v", err)
		}
		if !StreamRevoked(r, adult) {
			t.Fatal("a config-change re-lock did not revoke the stream")
		}
	})

	t.Run("a live ticket survives a config-change re-lock", func(t *testing.T) {
		// THE REGRESSION THIS RULE EXISTS FOR. Decide returns before it ever
		// looks at the ticket when nothing is locked, so this genuinely
		// unlocked operator's Decision reports Unlocked:false. A ticker
		// keying on Decision.Unlocked instead of re-reading the ticket would
		// tear down every open stream the moment anything was locked.
		gate := newGate(t)
		token := liveTicket(t, gate)
		r := streamRequest(token, Decision{Enforcing: true}, gate)
		if err := gate.Store().SetSections(context.Background(), []string{SectionAdultContent}); err != nil {
			t.Fatalf("SetSections: %v", err)
		}
		if StreamRevoked(r, adult) {
			t.Fatal("revoked an operator holding a valid unlock ticket")
		}
	})

	t.Run("SL-18 an expired ticket revokes", func(t *testing.T) {
		gate := newGate(t, SectionAdultContent)
		expiry := time.Now().Add(-time.Minute)
		r := streamRequest(expiredTicket(t, gate),
			Decision{Enforcing: true, Unlocked: true, ExpiresAt: expiry}, gate)
		if !StreamRevoked(r, adult) {
			t.Fatal("an expired ticket did not revoke the stream")
		}
	})

	t.Run("a live ticket on an already-locked section does not revoke", func(t *testing.T) {
		gate := newGate(t, SectionAdultContent)
		token, expiry, err := gate.IssueTicket()
		if err != nil {
			t.Fatalf("IssueTicket: %v", err)
		}
		r := streamRequest(token, Decision{Enforcing: true, Unlocked: true, ExpiresAt: expiry}, gate)
		if StreamRevoked(r, adult) {
			t.Fatal("revoked a stream whose ticket is still live")
		}
	})

	t.Run("a PIN-header stream does not revoke", func(t *testing.T) {
		// Unlocked by X-Section-Pin: a per-request credential with no
		// lifetime, so ExpiresAt is zero by construction and there is nothing
		// to expire. The caller already holds the PIN a re-lock would demand.
		gate := newGate(t, SectionAdultContent)
		r := streamRequest("", Decision{Enforcing: true, Unlocked: true}, gate)
		if StreamRevoked(r, adult) {
			t.Fatal("revoked a stream authenticated by the PIN header")
		}
	})

	t.Run("SL-18 an explicit revocation kills a stream whose ticket is still valid", func(t *testing.T) {
		// THE GAP THIS MECHANISM EXISTS FOR, and the unit half of QA-GATE-1
		// step 7. The ticket below is genuinely live — it decrypts, it carries
		// the right typ and AAD, and its 30-minute TTL has not run out — so
		// every pre-existing condition in StreamRevoked says "keep streaming".
		// Only the epoch says otherwise.
		gate := newGate(t, SectionAdultContent)
		token, expiry, err := gate.IssueTicket()
		if err != nil {
			t.Fatalf("IssueTicket: %v", err)
		}
		open := Decision{Enforcing: true, Unlocked: true, ExpiresAt: expiry, Epoch: gate.Epoch()}
		r := streamRequest(token, open, gate)
		if StreamRevoked(r, adult) {
			t.Fatal("revoked before the operator re-locked")
		}

		gate.Revoke() // what POST /api/section-lock/lock now does

		if _, stillValid := gate.ValidTicket(token); !stillValid {
			t.Fatal("precondition: the captured ticket should still validate cryptographically — " +
				"if it does not, this test is passing for the wrong reason")
		}
		if !StreamRevoked(r, adult) {
			t.Fatal("an explicit re-lock did not terminate a stream holding a pre-revocation ticket")
		}
	})

	t.Run("a stream opened AFTER a revocation is unaffected by it", func(t *testing.T) {
		// The counter is monotonic, not a flag: re-locking once must not
		// poison every stream opened from then on.
		gate := newGate(t, SectionAdultContent)
		gate.Revoke()
		token, expiry, err := gate.IssueTicket()
		if err != nil {
			t.Fatalf("IssueTicket: %v", err)
		}
		r := streamRequest(token, Decision{Enforcing: true, Unlocked: true, ExpiresAt: expiry, Epoch: gate.Epoch()}, gate)
		if StreamRevoked(r, adult) {
			t.Fatal("a stream opened after the revocation was revoked by it")
		}
	})

	t.Run("Decide stamps the epoch even when nothing is locked", func(t *testing.T) {
		// The regression the stamping placement guards against: Decide returns
		// EARLY when nothing is locked, and a Decision that missed the stamp
		// there would carry a stale zero — so an operator who had re-locked at
		// any point in the past would have every later stream torn down the
		// instant a section was locked, valid ticket and all.
		gate := newGate(t)
		gate.Revoke()
		decision, err := gate.Decide(context.Background(), Request{Enforcing: true})
		if err != nil {
			t.Fatalf("Decide: %v", err)
		}
		if decision.Epoch != gate.Epoch() {
			t.Fatalf("nothing-locked decision carried epoch %d, want %d", decision.Epoch, gate.Epoch())
		}
	})

	t.Run("a PIN-header stream survives a revocation", func(t *testing.T) {
		// Deliberate scoping, not an oversight: §4.5 revokes in-flight
		// TICKETS. An X-Section-Pin caller holds the PIN itself — the very
		// credential re-locking would demand — and carries no ticket to
		// revoke. EventSource cannot send headers, so this is a curl-shaped
		// client only.
		gate := newGate(t, SectionAdultContent)
		r := streamRequest("", Decision{Enforcing: true, Unlocked: true, Epoch: gate.Epoch()}, gate)
		gate.Revoke()
		if StreamRevoked(r, adult) {
			t.Fatal("revoked a stream authenticated by the PIN header")
		}
	})

	t.Run("an unreadable configuration revokes", func(t *testing.T) {
		gate := newGate(t, SectionAdultContent)
		r := streamRequest(liveTicket(t, gate), Decision{Enforcing: true}, failingLookup{inner: gate})
		if !StreamRevoked(r, adult) {
			t.Fatal("kept streaming on a configuration that could not be read")
		}
	})
}
