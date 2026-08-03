package api

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/labbersanon/sakms/internal/auth"
	"github.com/labbersanon/sakms/internal/sectionlock"
)

// sectionlock_gapclose_test.go — the findings three independent reviews raised
// against the shipped section lock.

// --- SL-19: logout drops the unlock ticket ------------------------------

// SL-19, the plan's own specified test, which had been dropped rather than
// deferred: logging out must clear the unlock ticket, and logging back in must
// NOT inherit it.
//
// Without this, "log out" does not re-secure a shared screen — the single
// scenario in this feature's whole threat model.
func TestLogoutClearsTheUnlockTicket(t *testing.T) {
	f := newSectionLockFixture(t, false)
	f.setPin(t, sectionLockTestPin)
	f.lockSections(t, sectionlock.SectionAdultContent, sectionlock.SectionOrganize)
	session := f.withSession(t)

	// Unlock, and confirm the locked route is genuinely reachable.
	if resp := f.do(t, http.MethodPost, "/api/section-lock/unlock",
		map[string]string{"pin": sectionLockTestPin}, session); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("unlock = %d, want 204", resp.StatusCode)
	}
	if resp := f.do(t, http.MethodGet, lockedRoutePath, nil, session); resp.StatusCode != sentinelStatus {
		t.Fatalf("after unlocking, the locked route = %d, want %d", resp.StatusCode, sentinelStatus)
	}

	// Log out through the real route, driven over the same cookie jar a browser
	// uses — so the jar applies the clearing Set-Cookie headers exactly as a
	// browser would, rather than the test asserting on them in isolation.
	logout := f.do(t, http.MethodPost, "/api/auth/logout", nil, session)
	if logout.StatusCode != http.StatusNoContent {
		t.Fatalf("logout = %d, want 204", logout.StatusCode)
	}

	var clearedUnlock, clearedSession bool
	for _, c := range logout.Cookies() {
		if c.Value != "" || c.MaxAge >= 0 {
			continue
		}
		switch c.Name {
		case sectionlock.CookieName:
			clearedUnlock = true
		case auth.CookieName:
			clearedSession = true
		}
	}
	if !clearedSession {
		t.Fatal("logout did not clear the session cookie")
	}
	if !clearedUnlock {
		t.Fatalf("logout did not clear the %s unlock ticket — logging back in inherits the "+
			"previous session's unlocked state, so logging out does not re-secure a shared "+
			"screen (SL-19)", sectionlock.CookieName)
	}

	// Log back in: a fresh session with no unlock ticket must find the section
	// still locked. Nothing auto-unlocks.
	fresh := f.withSession(t)
	resp := f.do(t, http.MethodGet, lockedRoutePath, nil, fresh)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("after logout and a fresh login, the locked route = %d, want 403 — the new "+
			"session inherited an unlock ticket it never earned", resp.StatusCode)
	}
	assertSectionLockedJSON(t, resp)
}

// --- Section-id validation ----------------------------------------------

// An unknown section id must be REJECTED on write. Storing it silently locks
// nothing (classification only ever emits canonical ids) while answering 204,
// so the operator believes a section is protected when it is not.
func TestSetSectionsRejectsUnknownIds(t *testing.T) {
	f := newSectionLockFixture(t, false)
	f.setPin(t, sectionLockTestPin)
	session := f.withSession(t)

	for _, tc := range []struct {
		name     string
		sections []string
		wantIn   string
	}{
		{"a typo", []string{"adult_content"}, "adult_content"},
		{"a miscasing", []string{"Discover"}, "Discover"},
		{"one bad id among good ones", []string{sectionlock.SectionQueue, "nonsense"}, "nonsense"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := f.do(t, http.MethodPut, "/api/section-lock/sections", map[string]any{
				"currentPin": sectionLockTestPin,
				"sections":   tc.sections,
			}, session)
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("PUT sections %v = %d, want 400 — an unrecognised id was accepted and "+
					"stored, which locks nothing while reporting success", tc.sections, resp.StatusCode)
			}
			body, _ := io.ReadAll(resp.Body)
			if !strings.Contains(string(body), tc.wantIn) {
				t.Fatalf("the 400 body %q does not name the invalid id %q", strings.TrimSpace(string(body)), tc.wantIn)
			}
			// Nothing was written.
			locked, err := f.lockStore.LockedSections(context.Background())
			if err != nil {
				t.Fatalf("LockedSections: %v", err)
			}
			if locked.Len() != 0 {
				t.Fatalf("a rejected request still wrote %v", locked.Sorted())
			}
		})
	}
}

// Every canonical id is still accepted — the validator must not be so strict it
// rejects the real set.
func TestSetSectionsAcceptsEveryCanonicalId(t *testing.T) {
	f := newSectionLockFixture(t, false)
	f.setPin(t, sectionLockTestPin)
	session := f.withSession(t)

	all := sectionlock.AllSections()
	resp := f.do(t, http.MethodPut, "/api/section-lock/sections", map[string]any{
		"currentPin": sectionLockTestPin,
		"sections":   all,
	}, session)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("PUT sections with the full canonical set = %d, want 204", resp.StatusCode)
	}
	locked, err := f.lockStore.LockedSections(context.Background())
	if err != nil {
		t.Fatalf("LockedSections: %v", err)
	}
	if locked.Len() != len(all) {
		t.Fatalf("stored %d sections, want %d", locked.Len(), len(all))
	}
}

// §5.2's preserve-unknown rule is about STORAGE, not writes: an id an older
// build wrote stays readable, so a temporarily-removed tab re-locks if it
// returns. Validation on write must not have broken that.
func TestUnknownStoredIdIsStillPreservedOnRead(t *testing.T) {
	f := newSectionLockFixture(t, false)
	ctx := context.Background()
	// Written directly to the store, as an older build would have.
	if err := f.lockStore.SetSections(ctx, []string{"a-tab-from-a-future-version"}); err != nil {
		t.Fatalf("SetSections: %v", err)
	}
	locked, err := f.lockStore.LockedSections(ctx)
	if err != nil {
		t.Fatalf("LockedSections: %v", err)
	}
	if !locked.Has("a-tab-from-a-future-version") {
		t.Fatal("an unknown id already in storage was dropped on read — §5.2 requires it be " +
			"preserved so a temporarily-removed tab re-locks if it returns")
	}
}

// --- setPin revokes outstanding tickets ---------------------------------

// Changing the PIN is the natural response to "someone else learned it", so it
// must revoke outstanding tickets the way lock and clearPin already do.
func TestSetPinRevokesOutstandingTickets(t *testing.T) {
	f := newSectionLockFixture(t, false)
	f.setPin(t, sectionLockTestPin)
	f.lockSections(t, sectionlock.SectionAdultContent)
	session := f.withSession(t)

	if resp := f.do(t, http.MethodPost, "/api/section-lock/unlock",
		map[string]string{"pin": sectionLockTestPin}, session); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("unlock = %d, want 204", resp.StatusCode)
	}
	before := f.gate.Epoch()

	if resp := f.do(t, http.MethodPut, "/api/section-lock/pin", map[string]string{
		"currentPin": sectionLockTestPin,
		"newPin":     "a-brand-new-pin",
	}, session); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("PUT pin = %d, want 204", resp.StatusCode)
	}

	if got := f.gate.Epoch(); got == before {
		t.Fatal("changing the PIN did not bump the revocation epoch — every already-issued " +
			"unlock ticket stays live on open SSE streams for the rest of its TTL, so the " +
			"change accomplishes nothing against the person who learned the old PIN")
	}
	// And the old PIN genuinely stops working.
	resp := f.do(t, http.MethodPost, "/api/section-lock/unlock",
		map[string]string{"pin": sectionLockTestPin})
	if resp.StatusCode == http.StatusNoContent {
		t.Fatal("the OLD PIN still unlocks after a PIN change")
	}
}
