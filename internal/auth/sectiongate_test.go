package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/labbersanon/sakms/internal/secrets"
	"github.com/labbersanon/sakms/internal/sectionlock"
	"github.com/labbersanon/sakms/internal/settings"
)

// sectiongate_test.go — W1-a: the section PIN lock as Layer 1 (Middleware's
// dispatch step 5), plus SL-2's deferred half.
//
// SL-2 is split across two packages on purpose. internal/sectionlock's
// ticket_test.go owns the "a session cookie is refused as an unlock ticket"
// direction and covers the reverse one only at the crypto layer; it cannot
// reach auth.ValidateToken at all, because auth imports sectionlock and the
// import back would be a cycle. The companion half lives here.

// --- SL-2, deferred half: ValidateToken rejects an unlock ticket ---------

// The discriminating test. The forgery is sealed with the NIL-AAD Encrypt —
// the same call auth.IssueToken uses — so it decrypts cleanly and the ONLY
// thing that can reject it is ValidateToken's typ check.
//
// Minting a real sectionlock ticket here would NOT discriminate: a real
// ticket is AAD-sealed and dies inside Decrypt, so that assertion passes
// whether or not the typ check exists at all. TestValidateToken_
// RejectsRealUnlockTicket below covers that layer separately, for the same
// reason ticket_test.go asserts its two mechanisms separately.
func TestValidateToken_RejectsUnlockTicketPayload(t *testing.T) {
	enc := testEncryptor(t)

	forged := sealPayload(t, enc, `{"typ":"unlock","exp":`+futureUnix()+`}`)
	if ValidateToken(enc, forged) {
		t.Fatal("a payload declaring typ=\"unlock\" was accepted as a session token — " +
			"an unlock ticket that ever reached the nil-AAD Encrypt would be a full session")
	}
}

// The asymmetry, which is the load-bearing half: an ABSENT typ is a valid
// session. Every sakms_session issued before this feature shipped has no typ
// field at all, so rejecting those would force-logout every operator on
// deploy — and in oidc mode, a redirect dance mid-upgrade.
//
// The payload is hand-built rather than produced by IssueToken so this test
// keeps meaning "a pre-deploy token still works" even if a future change
// starts stamping typ on issue.
func TestValidateToken_AcceptsPayloadWithNoTypField(t *testing.T) {
	enc := testEncryptor(t)

	preDeploy := sealPayload(t, enc, `{"exp":`+futureUnix()+`}`)
	if !ValidateToken(enc, preDeploy) {
		t.Fatal("a session payload with no typ field was rejected — every already-issued " +
			"session cookie looks exactly like this, so this is a forced logout on deploy")
	}
}

// The wire format IssueToken emits must stay byte-identical to what it was
// before sessionPayload grew a Typ field. omitempty is the mechanism; this
// is the assertion that the mechanism is actually in place.
func TestIssueToken_WireFormatUnchanged(t *testing.T) {
	enc := testEncryptor(t)

	token, err := IssueToken(enc)
	if err != nil {
		t.Fatalf("IssueToken: %v", err)
	}
	plaintext, err := enc.Decrypt(token)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(plaintext), &raw); err != nil {
		t.Fatalf("unmarshalling issued payload %q: %v", plaintext, err)
	}
	if _, present := raw["typ"]; present {
		t.Errorf("IssueToken emitted a typ field (%q) — the omitempty tag is gone, and every "+
			"issued token now differs in shape from the ones already in operators' browsers", plaintext)
	}
	if len(raw) != 1 {
		t.Errorf("issued payload = %q, want exactly one key (exp)", plaintext)
	}
}

// The crypto layer, asserted separately: a REAL unlock ticket — AAD-sealed
// by the shipped *secrets.Store — never even decrypts as a session token.
// This also proves end-to-end that *secrets.Store satisfies
// sectionlock.AADEncryptor, which W0 declared and nothing implemented.
func TestValidateToken_RejectsRealUnlockTicket(t *testing.T) {
	enc := testEncryptor(t)

	ticket, _, err := sectionlock.IssueTicket(enc)
	if err != nil {
		t.Fatalf("IssueTicket: %v", err)
	}
	if ValidateToken(enc, ticket) {
		t.Fatal("a real unlock ticket validated as a session cookie")
	}
}

// --- SL-12: the enforcement matrix --------------------------------------

// SL-12 — 3 auth modes x {locked, unlocked} x four credential shapes.
//
// The "none" rows are the highest-value ones here and are why the mode-none
// early return is preserved verbatim as an UNGATED exit: GATE-1 decided the
// lock is INERT in auth mode "none", so a locked section with no credential
// of any kind must still return 200.
func TestMiddleware_SectionLockMatrix(t *testing.T) {
	const lockedPath = "/api/modes/adult/discover" // classifies {discover, adult-content}
	const pin = "correct-horse"

	credentials := []struct {
		name string
		// apply decorates the request with whatever credentials this row
		// presents. session says whether a valid session cookie is included,
		// which is what decides the 401 rows in password/oidc mode.
		apply   func(t *testing.T, r *http.Request, enc *secrets.Store)
		session bool
		// wantLocked is the status when adult-content IS locked, in
		// password/oidc mode. Unlocked always expects 200 for a row that
		// satisfies primary auth.
		wantLocked int
	}{
		{
			name:       "session cookie, no unlock ticket",
			apply:      func(t *testing.T, r *http.Request, enc *secrets.Store) { addSession(t, r, enc) },
			session:    true,
			wantLocked: http.StatusForbidden,
		},
		{
			name: "session cookie plus a live unlock ticket",
			apply: func(t *testing.T, r *http.Request, enc *secrets.Store) {
				addSession(t, r, enc)
				addUnlockTicket(t, r, enc)
			},
			session:    true,
			wantLocked: http.StatusOK,
		},
		{
			name:       "X-Api-Key with no PIN header",
			apply:      func(t *testing.T, r *http.Request, _ *secrets.Store) { r.Header.Set("X-Api-Key", testAPIKey) },
			wantLocked: http.StatusForbidden,
		},
		{
			name: "X-Api-Key with the correct PIN header",
			apply: func(t *testing.T, r *http.Request, _ *secrets.Store) {
				r.Header.Set("X-Api-Key", testAPIKey)
				r.Header.Set(sectionlock.HeaderName, pin)
			},
			wantLocked: http.StatusOK,
		},
		{
			name: "X-Api-Key with a wrong PIN header",
			apply: func(t *testing.T, r *http.Request, _ *secrets.Store) {
				r.Header.Set("X-Api-Key", testAPIKey)
				r.Header.Set(sectionlock.HeaderName, "not-the-pin")
			},
			wantLocked: http.StatusForbidden,
		},
		{
			name:       "no credential at all",
			apply:      func(*testing.T, *http.Request, *secrets.Store) {},
			wantLocked: http.StatusUnauthorized,
		},
	}

	for _, mode := range []string{ModeNone, ModePassword, ModeOIDC} {
		for _, locked := range []bool{true, false} {
			for _, cred := range credentials {
				name := mode + "/" + lockedLabel(locked) + "/" + cred.name
				t.Run(name, func(t *testing.T) {
					enc := testEncryptor(t)
					handler, _ := gatedTestHandler(t, enc, mode, pin, locked)

					req := httptest.NewRequest(http.MethodGet, lockedPath, nil)
					cred.apply(t, req, enc)
					rec := httptest.NewRecorder()
					handler.ServeHTTP(rec, req)

					want := expectedStatus(mode, locked, cred.session, cred.wantLocked)
					if rec.Code != want {
						t.Fatalf("status = %d, want %d (body %q)", rec.Code, want, rec.Body.String())
					}
					if rec.Code == http.StatusForbidden {
						assertSectionLockedBody(t, rec)
					}
				})
			}
		}
	}
}

// The gate must never run before primary auth has answered. An
// unauthenticated caller hitting a locked route gets 401 with NO
// section_locked body — otherwise the lock's own configuration leaks to
// anyone who can reach the port.
func TestMiddleware_UnauthenticatedLearnsNothingAboutTheLock(t *testing.T) {
	enc := testEncryptor(t)
	handler, _ := gatedTestHandler(t, enc, ModePassword, "correct-horse", true)

	req := httptest.NewRequest(http.MethodGet, "/api/modes/adult/discover", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if body := rec.Body.String(); strings.Contains(body, "section") {
		t.Fatalf("the 401 body mentioned the section lock: %q", body)
	}
}

// A route that classifies as no section at all stays reachable while a
// section is locked — this is the section-lock control surface, /api/auth/*
// and the SPA shell, i.e. the routes the operator recovers THROUGH.
func TestMiddleware_UnclassifiedRoutesAreNotGated(t *testing.T) {
	enc := testEncryptor(t)
	handler, _ := gatedTestHandler(t, enc, ModePassword, "correct-horse", true)

	for _, path := range []string{"/api/section-lock/status", "/api/auth/status", "/index.html"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		addSession(t, req, enc)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Errorf("%s = %d, want 200 — an unclassified route must not be gated", path, rec.Code)
		}
	}
}

// Layers 2 and 3 read the Decision out of the request context rather than
// re-verifying anything. Forgetting r.WithContext in the middleware is a
// SILENT fail-open (an absent decision ALLOWS by design), so it needs its
// own assertion rather than being implied by a passing status code.
func TestMiddleware_WritesTheDecisionIntoTheRequestContext(t *testing.T) {
	enc := testEncryptor(t)
	handler, seen := gatedTestHandler(t, enc, ModePassword, "correct-horse", true)

	// A route that is NOT adult, so it passes the gate while adult-content
	// stays locked — and Layer 2 must still be able to deny an Adult mode
	// read from the body.
	req := httptest.NewRequest(http.MethodPost, "/api/autograb-batch", nil)
	addSession(t, req, enc)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	decision, ok := sectionlock.FromContext(*seen)
	if !ok {
		t.Fatal("no Decision in the request context — Layers 2 and 3 would silently allow everything")
	}
	if !decision.Enforcing {
		t.Error("the context Decision is not enforcing")
	}
	if err := sectionlock.RequireAdult(*seen); err == nil {
		t.Error("RequireAdult allowed an Adult body item while adult-content was locked — " +
			"Layer 2 is reading a decision that says nothing")
	}
}

// Without WithSectionGate, Middleware behaves exactly as it did before the
// lock existed: no gating, and no Decision in the context (so Layers 2 and
// 3 fall through to their allow-on-absent default).
func TestMiddleware_NoGateInstalledIsUngated(t *testing.T) {
	enc := testEncryptor(t)
	store := newTestStore(t)
	if err := store.SetAuthMode(context.Background(), ModePassword); err != nil {
		t.Fatalf("SetAuthMode: %v", err)
	}
	var seen context.Context
	handler := Middleware(enc, store, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Context()
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/modes/adult/discover", nil)
	addSession(t, req, enc)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if _, ok := sectionlock.FromContext(seen); ok {
		t.Error("a Decision was attached with no gate installed")
	}
}

// The brick state: sections locked with no PIN in existence. It denies —
// fail closed, deliberately, rather than treating "no PIN" as "not
// enforcing" — but with 500, NOT 403 section_locked, because no credential
// can clear it and a section_locked code would raise a frontend PIN overlay
// guaranteed to fail forever. Recovery is SAKMS_SECTION_LOCK_DISABLE=1.
func TestMiddleware_LockedWithNoPinDeniesWithoutOfferingAPinPrompt(t *testing.T) {
	enc := testEncryptor(t)
	ctx := context.Background()

	authStore := newTestStore(t)
	if err := authStore.SetAuthMode(ctx, ModePassword); err != nil {
		t.Fatalf("SetAuthMode: %v", err)
	}
	// Written straight past SetPin/SetSections, because the API deliberately
	// cannot produce this state — it is reachable only via a corrupt bcrypt
	// hash or a hand-edited database.
	raw := newMemorySettings()
	if err := raw.Set(ctx, sectionlock.SectionsKey, `["adult-content"]`); err != nil {
		t.Fatalf("seeding sections: %v", err)
	}
	lockStore := sectionlock.NewStore(raw)
	handler := Middleware(enc, authStore,
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }),
		WithSectionGate(sectionlock.NewGate(lockStore, enc)))

	req := httptest.NewRequest(http.MethodGet, "/api/modes/adult/discover", nil)
	addSession(t, req, enc)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (body %q)", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "section_locked") {
		t.Error("an unclearable state answered section_locked — the frontend would raise a " +
			"PIN overlay that can never succeed")
	}
}

// --- helpers ------------------------------------------------------------

const testAPIKey = "section-lock-matrix-api-key"

// gatedTestHandler builds a Middleware with a live section gate over an
// in-memory settings store, and returns the context the inner handler saw.
func gatedTestHandler(t *testing.T, enc *secrets.Store, mode, pin string, locked bool) (http.Handler, *context.Context) {
	t.Helper()
	ctx := context.Background()

	authStore := newTestStore(t)
	if err := authStore.SetAuthMode(ctx, mode); err != nil {
		t.Fatalf("SetAuthMode(%q): %v", mode, err)
	}
	authStore.UseEnvAPIKey(testAPIKey)

	lockStore := sectionlock.NewStore(newMemorySettings())
	if err := lockStore.SetPin(ctx, pin); err != nil {
		t.Fatalf("SetPin: %v", err)
	}
	sections := []string{}
	if locked {
		sections = []string{sectionlock.SectionAdultContent}
	}
	if err := lockStore.SetSections(ctx, sections); err != nil {
		t.Fatalf("SetSections: %v", err)
	}

	var seen context.Context
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Context()
		w.WriteHeader(http.StatusOK)
	})
	handler := Middleware(enc, authStore, inner,
		WithSectionGate(sectionlock.NewGate(lockStore, enc)))
	return handler, &seen
}

func expectedStatus(mode string, locked, session bool, wantLocked int) int {
	if mode == ModeNone {
		// The lock is inert in "none": the early return never reaches the
		// gate, so even a credential-less request to a locked route passes.
		return http.StatusOK
	}
	if !session && wantLocked == http.StatusUnauthorized {
		return http.StatusUnauthorized
	}
	if !locked {
		return http.StatusOK
	}
	return wantLocked
}

func lockedLabel(locked bool) string {
	if locked {
		return "locked"
	}
	return "unlocked"
}

func addSession(t *testing.T, r *http.Request, enc *secrets.Store) {
	t.Helper()
	token, err := IssueToken(enc)
	if err != nil {
		t.Fatalf("IssueToken: %v", err)
	}
	r.AddCookie(&http.Cookie{Name: CookieName, Value: token})
}

func addUnlockTicket(t *testing.T, r *http.Request, enc *secrets.Store) {
	t.Helper()
	ticket, _, err := sectionlock.IssueTicket(enc)
	if err != nil {
		t.Fatalf("IssueTicket: %v", err)
	}
	r.AddCookie(&http.Cookie{Name: sectionlock.CookieName, Value: ticket})
}

func assertSectionLockedBody(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	var body struct {
		Error   string `json:"error"`
		Code    string `json:"code"`
		Section string `json:"section"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("the 403 body is not JSON (%q): %v", rec.Body.String(), err)
	}
	// The frontend keys its PIN overlay on this exact code, and treats any
	// other 403 as a hard error.
	if body.Code != "section_locked" {
		t.Errorf("403 code = %q, want %q", body.Code, "section_locked")
	}
	if body.Section != sectionlock.SectionAdultContent {
		t.Errorf("403 section = %q, want %q", body.Section, sectionlock.SectionAdultContent)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("403 Content-Type = %q, want application/json", ct)
	}
}

func sealPayload(t *testing.T, enc *secrets.Store, plaintext string) string {
	t.Helper()
	token, err := enc.Encrypt(plaintext)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	return token
}

func futureUnix() string {
	return strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10)
}

// memorySettings is the smallest thing satisfying sectionlock.SettingsStore.
// sectionlock's own fakeSettings is unexported to its test package, and this
// needs no failure injection — the failure-policy cases are covered there.
type memorySettings struct {
	mu     sync.Mutex
	values map[string]string
}

func newMemorySettings() *memorySettings {
	return &memorySettings{values: map[string]string{}}
}

func (m *memorySettings) Get(_ context.Context, key string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.values[key]
	if !ok {
		return "", settings.ErrNotFound
	}
	return v, nil
}

func (m *memorySettings) Set(_ context.Context, key, value string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.values[key] = value
	return nil
}
