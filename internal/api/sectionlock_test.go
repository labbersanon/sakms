package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"testing"

	"github.com/labbersanon/sakms/internal/auth"
	"github.com/labbersanon/sakms/internal/secrets"
	"github.com/labbersanon/sakms/internal/sectionlock"
	"github.com/labbersanon/sakms/internal/settings"
)

// sectionlock_test.go — W1-b: the section lock's control surface, and the
// mounting shape cmd/sakms gives it.
//
// Every test here runs against a top-level mux built the way main.go builds
// its own — both section-lock mounts, plus a catch-all standing in for
// top.Handle("/api/", protectedAPI) — because two of the things this wave
// can get wrong (a missing mount, a gate installed on the wrong middleware)
// are invisible to a test that exercises the handlers directly.

const (
	sectionLockTestAPIKey = "section-lock-control-surface-key"
	sectionLockTestPin    = "correct-horse"

	// sentinelStatus is what the /api/ catch-all answers. It stands in for
	// NewMux's own mux: reaching it means a request fell PAST the two
	// section-lock mounts, and reaching it on a gated path means the section
	// gate allowed the request through.
	sentinelStatus = http.StatusTeapot

	// lockedRoutePath classifies as {organize, adult-content} — a real route
	// on the catch-all, so the gate is what decides whether the sentinel is
	// reached.
	lockedRoutePath = "/api/modes/adult/rename/scan"
)

type sectionLockFixture struct {
	srv       *httptest.Server
	client    *http.Client
	lockStore *sectionlock.Store
	authStore *auth.Store
	secrets   *secrets.Store
	// gate is the one shared Gate, exposed so a test can assert revocation
	// (Epoch) without reaching through the mux.
	gate *sectionlock.Gate
}

// newSectionLockFixture mirrors cmd/sakms/main.go's wiring: ONE
// sectionlock.Store and ONE Gate, shared by the control mux and by every
// auth.Middleware, with the gate installed via a variadic option slice that
// stays EMPTY when disabled — which is exactly how the disarm works in
// production, and why a test that instead flips a flag inside a live gate
// would be testing a path that does not exist.
func newSectionLockFixture(t *testing.T, disabled bool) *sectionLockFixture {
	t.Helper()
	ctx := context.Background()

	authStore, secretStore, sqlDB := testAuthStoreWithDB(t)
	if err := authStore.SetAuthMode(ctx, auth.ModePassword); err != nil {
		t.Fatalf("SetAuthMode: %v", err)
	}
	authStore.UseEnvAPIKey(sectionLockTestAPIKey)

	lockStore := sectionlock.NewStore(settings.New(sqlDB))
	gate := sectionlock.NewGate(lockStore, secretStore)

	var sectionGate []auth.MiddlewareOption
	if !disabled {
		sectionGate = append(sectionGate, auth.WithSectionGate(gate))
	}

	sentinel := auth.Middleware(secretStore, authStore,
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(sentinelStatus) }),
		sectionGate...)
	control := auth.Middleware(secretStore, authStore,
		NewSectionLockMux(gate, authStore, disabled), sectionGate...)

	top := http.NewServeMux()
	// Logout is mounted UNWRAPPED, exactly as main.go mounts NewAuthMux, so a
	// test can drive it through the same cookie jar a browser uses and observe
	// what the response actually clears (SL-19).
	top.HandleFunc("POST /api/auth/logout", authLogoutHandler())
	top.Handle("/api/section-lock", control)
	top.Handle("/api/section-lock/", control)
	top.Handle("/api/", sentinel)

	srv := httptest.NewServer(top)
	t.Cleanup(srv.Close)

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookiejar: %v", err)
	}
	return &sectionLockFixture{
		srv: srv,
		client: &http.Client{
			Jar: jar,
			// Redirects are NOT followed, so the bare-path mount test can tell
			// a real 404 from ServeMux's implicit subtree redirect.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		lockStore: lockStore,
		authStore: authStore,
		secrets:   secretStore,
		gate:      gate,
	}
}

func (f *sectionLockFixture) setPin(t *testing.T, pin string) {
	t.Helper()
	if err := f.lockStore.SetPin(context.Background(), pin); err != nil {
		t.Fatalf("SetPin: %v", err)
	}
}

func (f *sectionLockFixture) lockSections(t *testing.T, ids ...string) {
	t.Helper()
	if err := f.lockStore.SetSections(context.Background(), ids); err != nil {
		t.Fatalf("SetSections: %v", err)
	}
}

// withSession decorates a request with a valid session cookie — the browser
// credential.
func (f *sectionLockFixture) withSession(t *testing.T) func(*http.Request) {
	t.Helper()
	token, err := auth.IssueToken(f.secrets)
	if err != nil {
		t.Fatalf("IssueToken: %v", err)
	}
	return func(r *http.Request) { r.AddCookie(&http.Cookie{Name: auth.CookieName, Value: token}) }
}

// withKey decorates a request with the universal API key — the out-of-process
// script credential, which carries no cookie and so can only ever present the
// PIN as a header.
func withKey(r *http.Request) { r.Header.Set("X-Api-Key", sectionLockTestAPIKey) }

func withPinHeader(pin string) func(*http.Request) {
	return func(r *http.Request) { r.Header.Set(sectionlock.HeaderName, pin) }
}

func (f *sectionLockFixture) do(t *testing.T, method, path string, body any, decorate ...func(*http.Request)) *http.Response {
	t.Helper()
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshalling body: %v", err)
		}
		reader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequest(method, f.srv.URL+path, reader)
	if err != nil {
		t.Fatalf("building request: %v", err)
	}
	for _, d := range decorate {
		d(req)
	}
	resp, err := f.client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func (f *sectionLockFixture) status(t *testing.T, decorate ...func(*http.Request)) sectionLockStatusResponse {
	t.Helper()
	resp := f.do(t, http.MethodGet, "/api/section-lock/status", nil, decorate...)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET status = %d, want 200", resp.StatusCode)
	}
	var got sectionLockStatusResponse
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decoding status: %v", err)
	}
	return got
}

// --- mounting -----------------------------------------------------------

// The two mounts, asserted separately because they fail differently.
//
// The SUBTREE mount is what keeps every real route off the general "/api/"
// pattern; without it, GET /status reaches the catch-all and 404s inside a
// mux that has never heard of it (here: answers sentinelStatus).
//
// The EXACT mount covers the bare path. Without it, ServeMux's implicit
// subtree redirect claims that path instead and answers 301 — which is why
// this client does not follow redirects, and why the assertion is on 404
// specifically rather than "not the sentinel".
func TestSectionLockMux_MountedOnBothTheExactAndSubtreePatterns(t *testing.T) {
	f := newSectionLockFixture(t, false)
	session := f.withSession(t)

	resp := f.do(t, http.MethodGet, "/api/section-lock/status", nil, session)
	if resp.StatusCode == sentinelStatus {
		t.Fatal("GET /api/section-lock/status reached the /api/ catch-all — the subtree mount is missing")
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/section-lock/status = %d, want 200", resp.StatusCode)
	}

	bare := f.do(t, http.MethodGet, "/api/section-lock", nil, session)
	switch bare.StatusCode {
	case sentinelStatus:
		t.Fatal("GET /api/section-lock fell through to the /api/ catch-all — the exact mount is missing")
	case http.StatusMovedPermanently, http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
		t.Fatalf("GET /api/section-lock answered ServeMux's implicit subtree redirect (%d) — the exact mount is missing", bare.StatusCode)
	case http.StatusNotFound:
		// Correct: the request reached this file's mux, which registers no
		// route at the bare path.
	default:
		t.Fatalf("GET /api/section-lock = %d, want 404 from the section-lock mux", bare.StatusCode)
	}
}

// "Exempt" in §4.4's table means exempt from the SECTION gate, never from
// primary auth. An unauthenticated caller must learn nothing about the lock.
func TestSectionLockMux_StillRequiresPrimaryAuth(t *testing.T) {
	f := newSectionLockFixture(t, false)
	f.setPin(t, sectionLockTestPin)
	f.lockSections(t, sectionlock.SectionSettings)

	resp := f.do(t, http.MethodGet, "/api/section-lock/status", nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated GET status = %d, want 401", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if bytes.Contains(body, []byte("section")) {
		t.Fatalf("the 401 body leaked section-lock state: %q", body)
	}
}

// The chicken-and-egg exemption: the panel lives behind the `settings` lock,
// so status must answer while `settings` is locked or the frontend can never
// draw the badge that explains why everything else is denied.
func TestSectionLockStatus_AnswersWhileSettingsIsLocked(t *testing.T) {
	f := newSectionLockFixture(t, false)
	f.setPin(t, sectionLockTestPin)
	f.lockSections(t, sectionlock.SectionSettings)

	got := f.status(t, f.withSession(t))
	if !got.PinSet {
		t.Error("pinSet = false, want true")
	}
	if len(got.LockedSections) != 1 || got.LockedSections[0] != sectionlock.SectionSettings {
		t.Errorf("lockedSections = %v, want [settings]", got.LockedSections)
	}
	if got.Unlocked {
		t.Error("unlocked = true with no ticket presented")
	}
	if !got.EnforcementAvailable {
		t.Error("enforcementAvailable = false on a password-mode instance with the lock armed")
	}
}

// --- SL-13 (AC4) --------------------------------------------------------

// SL-13 — AC4. A curl-shaped request (X-Api-Key, no browser, no cookie) to a
// locked route is rejected; the SAME request carrying the PIN header
// succeeds. Both halves matter: the rejection proves the gate is installed,
// and the success proves the PIN is a real credential rather than a
// permanent denial.
func TestSectionLock_SL13_CurlShapedRequestIsRejectedThenSucceedsWithThePin(t *testing.T) {
	f := newSectionLockFixture(t, false)
	f.setPin(t, sectionLockTestPin)
	f.lockSections(t, sectionlock.SectionOrganize, sectionlock.SectionAdultContent)

	denied := f.do(t, http.MethodPost, lockedRoutePath, nil, withKey)
	if denied.StatusCode != http.StatusForbidden {
		t.Fatalf("POST %s with a key alone = %d, want 403", lockedRoutePath, denied.StatusCode)
	}
	assertSectionLockedJSON(t, denied)

	allowed := f.do(t, http.MethodPost, lockedRoutePath, nil, withKey, withPinHeader(sectionLockTestPin))
	if allowed.StatusCode != sentinelStatus {
		t.Fatalf("POST %s with the correct PIN = %d, want %d (the request should have reached the handler)",
			lockedRoutePath, allowed.StatusCode, sentinelStatus)
	}
}

// --- SL-14 (AC5) --------------------------------------------------------

// SL-14 — AC5. An X-Api-Key client is a genuinely scoped credential now: the
// key alone is NOT enough for a locked route. Asserted separately from SL-13
// because this is the CLAUDE.md divergence the amendment has to state — a
// key no longer inherits the operator's full access.
//
// 403, not 401, is the documented deviation (§10.1): client.ts reboots the
// SPA on any non-/api/auth/ 401. The confidentiality property survives
// through ordering, which TestSectionLockMux_StillRequiresPrimaryAuth pins.
func TestSectionLock_SL14_ApiKeyWithoutPinHeaderIsRejected(t *testing.T) {
	f := newSectionLockFixture(t, false)
	f.setPin(t, sectionLockTestPin)
	f.lockSections(t, sectionlock.SectionAdultContent)

	resp := f.do(t, http.MethodGet, "/api/modes/adult/discover", nil, withKey)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("X-Api-Key without X-Section-Pin = %d, want 403", resp.StatusCode)
	}
	assertSectionLockedJSON(t, resp)

	wrong := f.do(t, http.MethodGet, "/api/modes/adult/discover", nil, withKey, withPinHeader("not-the-pin"))
	if wrong.StatusCode != http.StatusForbidden {
		t.Fatalf("X-Api-Key with a wrong PIN = %d, want 403", wrong.StatusCode)
	}
}

// --- SL-21 --------------------------------------------------------------

// SL-21 — the recovery invariant. Locking anything while no PIN exists would
// deny every gated route with no credential in existence that could ever
// satisfy it: a one-request brick.
func TestSectionLock_SL21_LockingSectionsWithNoPinIsRejected(t *testing.T) {
	f := newSectionLockFixture(t, false)
	session := f.withSession(t)

	resp := f.do(t, http.MethodPut, "/api/section-lock/sections",
		sectionLockSectionsRequest{Sections: []string{sectionlock.SectionAdultContent}}, session)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("PUT sections with no PIN set = %d, want 400", resp.StatusCode)
	}

	// And nothing was written — a 400 that still persisted the array would
	// brick the instance just as thoroughly.
	if got := f.status(t, session); len(got.LockedSections) != 0 {
		t.Fatalf("the rejected write still stored %v", got.LockedSections)
	}

	// The empty array is always legal: it is how the panel unlocks
	// everything, and how a fresh install starts.
	empty := f.do(t, http.MethodPut, "/api/section-lock/sections",
		sectionLockSectionsRequest{Sections: []string{}}, session)
	if empty.StatusCode != http.StatusNoContent {
		t.Fatalf("PUT sections with an empty array and no PIN = %d, want 204", empty.StatusCode)
	}
}

// --- SL-22 --------------------------------------------------------------

// SL-22 — SAKMS_SECTION_LOCK_DISABLE=1.
//
// Two independent halves, and both are load-bearing:
//
//  1. Gating is off. The disarm works by NOT installing the gate at all
//     (main.go leaves the option slice empty), so a stored locked set is
//     simply never consulted.
//  2. PUT /pin and DELETE /pin skip the currentPin requirement. This is the
//     ONLY path out of a corrupt bcrypt hash — a PIN that no longer verifies
//     against anything the operator can type.
func TestSectionLock_SL22_DisableEnvDisablesGatingAndSkipsCurrentPin(t *testing.T) {
	f := newSectionLockFixture(t, true)
	f.setPin(t, sectionLockTestPin)
	f.lockSections(t, sectionlock.SectionOrganize, sectionlock.SectionAdultContent)
	session := f.withSession(t)

	// (1) A locked route answers normally, with no PIN anywhere.
	resp := f.do(t, http.MethodPost, lockedRoutePath, nil, withKey)
	if resp.StatusCode != sentinelStatus {
		t.Fatalf("POST %s while disarmed = %d, want %d", lockedRoutePath, resp.StatusCode, sentinelStatus)
	}

	if got := f.status(t, session); got.EnforcementAvailable {
		t.Error("enforcementAvailable = true while disarmed")
	}

	// (2a) Change the PIN with no currentPin at all.
	changed := f.do(t, http.MethodPut, "/api/section-lock/pin",
		sectionLockPinRequest{NewPin: "brand-new-pin"}, session)
	if changed.StatusCode != http.StatusNoContent {
		t.Fatalf("PUT pin without currentPin while disarmed = %d, want 204", changed.StatusCode)
	}

	// (2b) Clear it, with no body whatsoever — the shape a curl -X DELETE
	// takes by default.
	cleared := f.do(t, http.MethodDelete, "/api/section-lock/pin", nil, session)
	if cleared.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE pin without currentPin while disarmed = %d, want 204", cleared.StatusCode)
	}
	after := f.status(t, session)
	if after.PinSet {
		t.Error("pinSet = true after DELETE /pin")
	}
	if len(after.LockedSections) != 0 {
		t.Errorf("lockedSections = %v after DELETE /pin — clearing the PIN must clear the sections in the same write", after.LockedSections)
	}
}

// The other side of SL-22: while ARMED, the same two routes do require the
// current PIN. Without this the disarm assertion above proves nothing — it
// would pass on a build that never checked currentPin at all.
func TestSectionLock_ArmedPinChangeRequiresTheCurrentPin(t *testing.T) {
	f := newSectionLockFixture(t, false)
	f.setPin(t, sectionLockTestPin)
	session := f.withSession(t)

	missing := f.do(t, http.MethodPut, "/api/section-lock/pin",
		sectionLockPinRequest{NewPin: "brand-new-pin"}, session)
	if missing.StatusCode != http.StatusBadRequest {
		t.Fatalf("PUT pin with no currentPin = %d, want 400", missing.StatusCode)
	}

	wrong := f.do(t, http.MethodPut, "/api/section-lock/pin",
		sectionLockPinRequest{CurrentPin: "not-the-pin", NewPin: "brand-new-pin"}, session)
	if wrong.StatusCode != http.StatusForbidden {
		t.Fatalf("PUT pin with a wrong currentPin = %d, want 403", wrong.StatusCode)
	}

	tooShort := f.do(t, http.MethodPut, "/api/section-lock/pin",
		sectionLockPinRequest{CurrentPin: sectionLockTestPin, NewPin: "12345"}, session)
	if tooShort.StatusCode != http.StatusBadRequest {
		t.Fatalf("PUT pin with a 5-character newPin = %d, want 400", tooShort.StatusCode)
	}

	ok := f.do(t, http.MethodPut, "/api/section-lock/pin",
		sectionLockPinRequest{CurrentPin: sectionLockTestPin, NewPin: "a-longer-pin"}, session)
	if ok.StatusCode != http.StatusNoContent {
		t.Fatalf("PUT pin with the correct currentPin = %d, want 204", ok.StatusCode)
	}
	// The PIN change must take effect immediately, not at the next restart —
	// the store and the gate's memo cache are shared with the middleware.
	stale := f.do(t, http.MethodPut, "/api/section-lock/sections",
		sectionLockSectionsRequest{CurrentPin: sectionLockTestPin, Sections: []string{}}, session)
	if stale.StatusCode != http.StatusForbidden {
		t.Fatalf("the OLD PIN still authorised a write after the change (%d)", stale.StatusCode)
	}
}

// --- unlock / lock ------------------------------------------------------

// The browser credential end to end: POST /unlock with the PIN sets the
// ticket cookie, the cookie opens a locked route with no PIN header
// anywhere (EventSource and <img> cannot send one), and POST /lock closes it
// again.
func TestSectionLock_UnlockIssuesATicketThatOpensALockedRoute(t *testing.T) {
	f := newSectionLockFixture(t, false)
	f.setPin(t, sectionLockTestPin)
	f.lockSections(t, sectionlock.SectionAdultContent)
	session := f.withSession(t)

	before := f.do(t, http.MethodGet, "/api/modes/adult/discover", nil, session)
	if before.StatusCode != http.StatusForbidden {
		t.Fatalf("locked route before unlocking = %d, want 403", before.StatusCode)
	}

	unlocked := f.do(t, http.MethodPost, "/api/section-lock/unlock",
		sectionLockUnlockRequest{Pin: sectionLockTestPin}, session)
	if unlocked.StatusCode != http.StatusNoContent {
		t.Fatalf("POST unlock = %d, want 204", unlocked.StatusCode)
	}
	var ticket *http.Cookie
	for _, c := range unlocked.Cookies() {
		if c.Name == sectionlock.CookieName {
			ticket = c
		}
	}
	if ticket == nil {
		t.Fatal("POST unlock set no unlock cookie")
	}
	if !ticket.HttpOnly {
		t.Error("the unlock cookie is not HttpOnly")
	}
	if !ticket.Expires.IsZero() {
		t.Error("the unlock cookie carries an Expires — it must die when the browser closes")
	}

	after := f.do(t, http.MethodGet, "/api/modes/adult/discover", nil, session)
	if after.StatusCode != sentinelStatus {
		t.Fatalf("locked route with a live ticket = %d, want %d", after.StatusCode, sentinelStatus)
	}
	if got := f.status(t, session); !got.Unlocked {
		t.Error("status reports unlocked = false while a live ticket is held")
	}

	if locked := f.do(t, http.MethodPost, "/api/section-lock/lock", nil, session); locked.StatusCode != http.StatusNoContent {
		t.Fatalf("POST lock = %d, want 204", locked.StatusCode)
	}
	relocked := f.do(t, http.MethodGet, "/api/modes/adult/discover", nil, session)
	if relocked.StatusCode != http.StatusForbidden {
		t.Fatalf("locked route after POST /lock = %d, want 403", relocked.StatusCode)
	}
}

// A wrong PIN on the unlock endpoint is the same 403 section_locked shape
// §6 defines for the path gate, with an EMPTY section — the control surface
// classifies as no section, so there is none to name.
func TestSectionLockUnlock_WrongPinIsRejected(t *testing.T) {
	f := newSectionLockFixture(t, false)
	f.setPin(t, sectionLockTestPin)
	session := f.withSession(t)

	resp := f.do(t, http.MethodPost, "/api/section-lock/unlock",
		sectionLockUnlockRequest{Pin: "not-the-pin"}, session)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("POST unlock with a wrong PIN = %d, want 403", resp.StatusCode)
	}
	var body struct {
		Code    string `json:"code"`
		Section string `json:"section"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decoding rejection: %v", err)
	}
	if body.Code != "section_locked" {
		t.Errorf("code = %q, want section_locked", body.Code)
	}
	if body.Section != "" {
		t.Errorf("section = %q, want empty on the control surface", body.Section)
	}
	if resp.Cookies() != nil {
		for _, c := range resp.Cookies() {
			if c.Name == sectionlock.CookieName && c.Value != "" {
				t.Fatal("a rejected unlock still issued a ticket")
			}
		}
	}
}

// --- auth mode "none" ---------------------------------------------------

// GATE-1, Option A: the lock is inert in auth mode "none", so its
// configuration endpoints refuse rather than accepting settings that would
// never be honoured. Status still answers — the panel needs it to render
// itself disabled — and so does POST /lock, which gives up a credential and
// is never privileged.
func TestSectionLock_ConfigurationRefusedInAuthModeNone(t *testing.T) {
	f := newSectionLockFixture(t, false)
	if err := f.authStore.SetAuthMode(context.Background(), auth.ModeNone); err != nil {
		t.Fatalf("SetAuthMode: %v", err)
	}

	for _, tc := range []struct {
		method, path string
		body         any
	}{
		{http.MethodPut, "/api/section-lock/pin", sectionLockPinRequest{NewPin: "a-new-pin"}},
		{http.MethodDelete, "/api/section-lock/pin", nil},
		{http.MethodPut, "/api/section-lock/sections", sectionLockSectionsRequest{Sections: []string{}}},
		{http.MethodPost, "/api/section-lock/unlock", sectionLockUnlockRequest{Pin: sectionLockTestPin}},
	} {
		resp := f.do(t, tc.method, tc.path, tc.body)
		if resp.StatusCode != http.StatusConflict {
			t.Errorf("%s %s in auth mode none = %d, want 409", tc.method, tc.path, resp.StatusCode)
		}
	}

	got := f.status(t)
	if got.EnforcementAvailable {
		t.Error("enforcementAvailable = true in auth mode none")
	}
	if locked := f.do(t, http.MethodPost, "/api/section-lock/lock", nil); locked.StatusCode != http.StatusNoContent {
		t.Errorf("POST lock in auth mode none = %d, want 204", locked.StatusCode)
	}
}

func assertSectionLockedJSON(t *testing.T, resp *http.Response) {
	t.Helper()
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	var body struct {
		Error   string `json:"error"`
		Code    string `json:"code"`
		Section string `json:"section"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("the 403 body is not JSON: %v", err)
	}
	if body.Code != "section_locked" {
		t.Errorf("code = %q, want section_locked — the frontend keys its PIN overlay on this exact string", body.Code)
	}
	if body.Section == "" {
		t.Error("section is empty on a path-gated rejection")
	}
}
