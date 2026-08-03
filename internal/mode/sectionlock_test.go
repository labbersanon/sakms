package mode

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/labbersanon/sakms/internal/sectionlock"
)

// sectionlock_test.go — W1-c: Layer 2 of the section PIN lock, the check
// Build makes immediately after its unknown-mode switch.
//
// SL-23 is the test that matters here, and it is written as a CONTRAST PAIR
// on purpose. A lone context.Background() call proves almost nothing: with
// no decision in the context RequireMode returns nil whether anything is
// locked or not, so the test would pass identically against a Build that
// had never been touched. Only asserting BOTH halves against the SAME
// locked state shows that the absence of a decision — not the absence of a
// lock — is what lets background work through.

// lockedAdult is the decision auth.Middleware resolves for a request that
// presented no unlock credential while adult-content is locked.
func lockedAdult() sectionlock.Decision {
	return sectionlock.Decision{
		Enforcing: true,
		Locked:    sectionlock.NewSet(sectionlock.SectionAdultContent),
		Unlocked:  false,
	}
}

func buildAdult(t *testing.T, ctx context.Context) error {
	t.Helper()
	store, settingsStore := newTestStores(t)
	_, err := Build(ctx, store, nil, settingsStore, &http.Client{Timeout: time.Second}, nil, Adult)
	return err
}

// SL-23 — the four background schedulers call Build from
// context.Background(), and a PIN lock must never stop background work.
// Absent decision ⇒ ALLOW, asserted against the very same locked state that
// denies an HTTP-request-driven build.
func TestBuild_SectionLock_BackgroundContextIsUnaffectedByALockedAdult(t *testing.T) {
	// Half one: a real request context carrying the locked decision IS
	// denied. Without this half the second half is vacuous.
	denied := buildAdult(t, sectionlock.WithDecision(context.Background(), lockedAdult()))
	if !errors.Is(denied, sectionlock.ErrSectionLocked) {
		t.Fatalf("a request-context Adult build with adult-content locked should fail with ErrSectionLocked, got %v", denied)
	}

	// Half two: the identical lock state, reached from context.Background()
	// the way airdatemonitor.go, usenetretry.go and watchfolders.go reach it,
	// builds normally.
	if err := buildAdult(t, context.Background()); err != nil {
		t.Fatalf("a background Adult build must not be blocked by a locked section, got %v", err)
	}
}

// The unlock credential is what clears it — one ticket unlocks every locked
// section, so there is no per-section scope to get wrong here.
func TestBuild_SectionLock_UnlockedDecisionAllowsAdult(t *testing.T) {
	d := lockedAdult()
	d.Unlocked = true
	if err := buildAdult(t, sectionlock.WithDecision(context.Background(), d)); err != nil {
		t.Fatalf("an unlocked decision must allow an Adult build, got %v", err)
	}
}

// A non-enforcing decision (SAKMS_SECTION_LOCK_DISABLE, or auth mode "none")
// never denies, even with adult-content in the locked set.
func TestBuild_SectionLock_NonEnforcingDecisionAllowsAdult(t *testing.T) {
	d := lockedAdult()
	d.Enforcing = false
	if err := buildAdult(t, sectionlock.WithDecision(context.Background(), d)); err != nil {
		t.Fatalf("a non-enforcing decision must allow an Adult build, got %v", err)
	}
}

// Build is a VALIDATION chokepoint, not an authorization one: only Adult is
// ever gated here. Locking adult-content must not touch Movies or Series,
// which is what keeps AC6's "without locking Mainstream" true on this layer.
func TestBuild_SectionLock_MainstreamModesUnaffectedByALockedAdult(t *testing.T) {
	ctx := sectionlock.WithDecision(context.Background(), lockedAdult())
	for _, m := range []Mode{Movies, Series} {
		store, settingsStore := newTestStores(t)
		if _, err := Build(ctx, store, nil, settingsStore, &http.Client{Timeout: time.Second}, nil, m); err != nil {
			t.Errorf("%s must be unaffected by a locked adult-content, got %v", m, err)
		}
	}
}

// The whole-tab case: locking `discover` locks Adult's discover routes too
// (Layer 1's job), but it must not make mode.Build reject an Adult session —
// Layer 2 is scoped to adult-content alone.
func TestBuild_SectionLock_ANonAdultLockedSectionDoesNotGateBuild(t *testing.T) {
	d := sectionlock.Decision{Enforcing: true, Locked: sectionlock.NewSet(sectionlock.SectionDiscover)}
	if err := buildAdult(t, sectionlock.WithDecision(context.Background(), d)); err != nil {
		t.Fatalf("only adult-content gates Build; a locked discover must not, got %v", err)
	}
}
