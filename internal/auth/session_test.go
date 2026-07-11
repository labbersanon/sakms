package auth

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/curtiswtaylorjr/sakms/internal/secrets"
)

func testEncryptor(t *testing.T) *secrets.Store {
	t.Helper()
	s, err := secrets.New(make([]byte, 32))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	return s
}

func TestIssueToken_ThenValidateSucceeds(t *testing.T) {
	enc := testEncryptor(t)
	token, err := IssueToken(enc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ValidateToken(enc, token) {
		t.Error("expected a freshly issued token to validate")
	}
}

func TestValidateToken_RejectsGarbage(t *testing.T) {
	enc := testEncryptor(t)
	if ValidateToken(enc, "not-a-real-token") {
		t.Error("expected garbage input to fail validation")
	}
}

func TestValidateToken_RejectsTokenFromDifferentKey(t *testing.T) {
	encA := testEncryptor(t)
	encB, err := secrets.New(append(make([]byte, 31), 1))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	token, err := IssueToken(encA)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ValidateToken(encB, token) {
		t.Error("expected a token encrypted under a different key to fail validation")
	}
}

func TestAuthenticated_NoCookie(t *testing.T) {
	enc := testEncryptor(t)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	if Authenticated(enc, req) {
		t.Error("expected no cookie to mean not authenticated")
	}
}

func TestAuthenticated_ValidCookie(t *testing.T) {
	enc := testEncryptor(t)
	token, err := IssueToken(enc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: CookieName, Value: token})
	if !Authenticated(enc, req) {
		t.Error("expected a valid session cookie to authenticate")
	}
}

func TestSetSessionCookie_ThenAuthenticatedRoundTrip(t *testing.T) {
	enc := testEncryptor(t)
	token, err := IssueToken(enc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	rec := httptest.NewRecorder()
	SetSessionCookie(rec, token)
	resp := rec.Result()

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	for _, c := range resp.Cookies() {
		req.AddCookie(c)
	}
	if !Authenticated(enc, req) {
		t.Error("expected the cookie set by SetSessionCookie to authenticate")
	}
}

func TestMiddleware_RejectsUnauthenticatedRequest(t *testing.T) {
	enc := testEncryptor(t)
	store := newTestStore(t)
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("inner handler must not run for an unauthenticated request")
	})
	srv := httptest.NewServer(Middleware(enc, store, inner))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}
}

func TestMiddleware_AllowsAuthenticatedRequest(t *testing.T) {
	enc := testEncryptor(t)
	store := newTestStore(t)
	var innerCalled bool
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { innerCalled = true; w.WriteHeader(http.StatusOK) })
	srv := httptest.NewServer(Middleware(enc, store, inner))
	defer srv.Close()

	token, err := IssueToken(enc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/", nil)
	req.AddCookie(&http.Cookie{Name: CookieName, Value: token})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if !innerCalled {
		t.Error("expected the inner handler to run for an authenticated request")
	}
}

// --- API-key path (X-Api-Key header) ---

func middlewareTestServer(t *testing.T, enc TokenEncryptor, store *Store) (*httptest.Server, *bool) {
	t.Helper()
	innerCalled := false
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		innerCalled = true
		w.WriteHeader(http.StatusOK)
	})
	srv := httptest.NewServer(Middleware(enc, store, inner))
	t.Cleanup(srv.Close)
	return srv, &innerCalled
}

// TestMiddleware_UnaffectedByProxyIdentityHeaders (autopilot-impl-wizard-
// autodetect plan, guardrails table, EC3) proves ProxyHeadersPresent's
// detection signal (proxydetect.go), consumed only by the status handler's
// wizard pre-select, never influences a real authorization decision here.
// Password mode (the default), a protected route, and a request carrying
// two of the recognized proxy identity headers but no valid credential at
// all must still 401 — a spoofed header can at most flip a first-run
// dropdown default, never bypass Middleware.
func TestMiddleware_UnaffectedByProxyIdentityHeaders(t *testing.T) {
	enc := testEncryptor(t)
	store := newTestStore(t)
	srv, called := middlewareTestServer(t, enc, store)

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/", nil)
	req.Header.Set("X-authentik-username", "someone")
	req.Header.Set("Remote-User", "someone")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 — proxy identity headers must never authorize a request, got %d", resp.StatusCode)
	}
	if *called {
		t.Error("expected the inner handler to never run for a request with only spoofed identity headers and no real credential")
	}
}

// TestMiddleware_NoCookieValidKey_Passes covers AC1: a valid X-Api-Key
// header with no session cookie at all authenticates.
func TestMiddleware_NoCookieValidKey_Passes(t *testing.T) {
	enc := testEncryptor(t)
	store := newTestStore(t)
	raw, err := store.EnsureAPIKey(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if raw == "" {
		t.Fatal("expected a freshly generated key")
	}

	srv, called := middlewareTestServer(t, enc, store)
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/", nil)
	req.Header.Set("X-Api-Key", raw)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if !*called {
		t.Error("expected the inner handler to run for a valid API key")
	}
}

// TestMiddleware_NoCookieNoKey_401 covers AC2: neither cookie nor header
// present, message stays "authentication required" (unchanged).
func TestMiddleware_NoCookieNoKey_401(t *testing.T) {
	enc := testEncryptor(t)
	store := newTestStore(t)
	srv, called := middlewareTestServer(t, enc, store)

	resp, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if got := strings.TrimSpace(string(body)); got != "authentication required" {
		t.Errorf("expected unchanged message %q, got %q", "authentication required", got)
	}
	if *called {
		t.Error("inner handler must not run")
	}
}

// TestMiddleware_InvalidKey_401 covers AC2: a well-formed but wrong key.
func TestMiddleware_InvalidKey_401(t *testing.T) {
	enc := testEncryptor(t)
	store := newTestStore(t)
	if _, err := store.EnsureAPIKey(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	srv, called := middlewareTestServer(t, enc, store)
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/", nil)
	req.Header.Set("X-Api-Key", "definitely-the-wrong-key")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}
	if *called {
		t.Error("inner handler must not run for an invalid key")
	}
}

// TestMiddleware_CookieValid_KeyAbsent_Passes is a regression check: the
// pre-existing cookie-only behavior must be untouched.
func TestMiddleware_CookieValid_KeyAbsent_Passes(t *testing.T) {
	enc := testEncryptor(t)
	store := newTestStore(t)
	token, err := IssueToken(enc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	srv, called := middlewareTestServer(t, enc, store)
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/", nil)
	req.AddCookie(&http.Cookie{Name: CookieName, Value: token})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if !*called {
		t.Error("expected the inner handler to run")
	}
}

// TestMiddleware_CookieValid_InvalidKey_Passes covers Edge 2: cookie wins
// first, so an invalid/garbage X-Api-Key header alongside a valid cookie
// must not block the request.
func TestMiddleware_CookieValid_InvalidKey_Passes(t *testing.T) {
	enc := testEncryptor(t)
	store := newTestStore(t)
	token, err := IssueToken(enc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	srv, called := middlewareTestServer(t, enc, store)
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/", nil)
	req.AddCookie(&http.Cookie{Name: CookieName, Value: token})
	req.Header.Set("X-Api-Key", "garbage-invalid-key")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 (cookie wins first, invalid key never blocks), got %d", resp.StatusCode)
	}
	if !*called {
		t.Error("expected the inner handler to run")
	}
}

// TestMiddleware_WhitespaceKeyHeader_401 covers Edge 3 at the middleware
// level: a header that's present but all whitespace must be treated as
// absent, not compared.
func TestMiddleware_WhitespaceKeyHeader_401(t *testing.T) {
	enc := testEncryptor(t)
	store := newTestStore(t)
	srv, called := middlewareTestServer(t, enc, store)

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/", nil)
	req.Header.Set("X-Api-Key", "   ")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}
	if *called {
		t.Error("inner handler must not run for a whitespace-only key header")
	}
}

// TestMiddleware_StoreReadError_500 covers Edge 4: a genuine settings-store
// read error must fail CLOSED (500), never fall through to allow.
func TestMiddleware_StoreReadError_500(t *testing.T) {
	enc := testEncryptor(t)
	store, sqlDB := newTestStoreWithDB(t)
	if _, err := sqlDB.Exec(`DROP TABLE settings`); err != nil {
		t.Fatalf("dropping settings table: %v", err)
	}

	srv, called := middlewareTestServer(t, enc, store)
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/", nil)
	req.Header.Set("X-Api-Key", "any-nonempty-key")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("expected 500 (fail closed on store error), got %d", resp.StatusCode)
	}
	if *called {
		t.Error("inner handler must NEVER run when the store errors — that would be a fail-open bug")
	}
}

// --- Mode-aware dispatch (slice 1) ---

// TestMiddleware_ModeReadError_500 covers G1: a store error while reading
// auth_mode itself (not the key lookup) must fail closed, before any
// mode-specific or key logic ever runs.
func TestMiddleware_ModeReadError_500(t *testing.T) {
	enc := testEncryptor(t)
	store, sqlDB := newTestStoreWithDB(t)
	if _, err := sqlDB.Exec(`DROP TABLE settings`); err != nil {
		t.Fatalf("dropping settings table: %v", err)
	}

	srv, called := middlewareTestServer(t, enc, store)
	// No cookie, no key at all — the mode read itself must be what fails,
	// not a fallthrough to "no credentials presented".
	resp, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("expected 500 (fail closed on auth_mode read error), got %d", resp.StatusCode)
	}
	if *called {
		t.Error("inner handler must NEVER run when the mode read errors")
	}
}

// TestMiddleware_NoneMode_AllPass asserts that once auth_mode is "none",
// every request passes through with no credential at all.
func TestMiddleware_NoneMode_AllPass(t *testing.T) {
	enc := testEncryptor(t)
	store := newTestStore(t)
	if err := store.SetAuthMode(context.Background(), ModeNone); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	srv, called := middlewareTestServer(t, enc, store)
	resp, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 in none mode with no credential, got %d", resp.StatusCode)
	}
	if !*called {
		t.Error("expected the inner handler to run in none mode")
	}
}

// TestMiddleware_APIKeyWorksRegardlessOfMode proves the universal
// X-Api-Key hoist (§0.4/Human Decision #2): a valid key passes even when
// auth_mode is something other than "password". Uses a stubbed/unknown
// mode value (which the slice-1 dispatch's default case fails closed on
// for the mode-specific branch) to prove the key check truly happens
// independent of the mode switch, not just for the two modes slice 1
// implements end-to-end.
func TestMiddleware_APIKeyWorksRegardlessOfMode(t *testing.T) {
	enc := testEncryptor(t)
	store := newTestStore(t)
	ctx := context.Background()
	raw, err := store.EnsureAPIKey(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, mode := range []string{ModeNone, "some-future-mode-not-yet-implemented"} {
		mode := mode
		t.Run(mode, func(t *testing.T) {
			if err := store.SetAuthMode(ctx, mode); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			srv, called := middlewareTestServer(t, enc, store)
			req, _ := http.NewRequest(http.MethodGet, srv.URL+"/", nil)
			req.Header.Set("X-Api-Key", raw)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("expected a valid X-Api-Key to pass in mode %q, got %d", mode, resp.StatusCode)
			}
			if !*called {
				t.Error("expected the inner handler to run for a valid key")
			}
		})
	}
}

// TestMiddleware_PasswordMode_CookieOrKey is the regression check for
// slice 1's mode-aware rewrite: with auth_mode explicitly "password"
// (rather than relying on the unset default), the cookie-or-key behavior
// from before this slice must be unchanged.
func TestMiddleware_PasswordMode_CookieOrKey(t *testing.T) {
	enc := testEncryptor(t)
	store := newTestStore(t)
	ctx := context.Background()
	if err := store.SetAuthMode(ctx, ModePassword); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	t.Run("cookie passes", func(t *testing.T) {
		token, err := IssueToken(enc)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		srv, called := middlewareTestServer(t, enc, store)
		req, _ := http.NewRequest(http.MethodGet, srv.URL+"/", nil)
		req.AddCookie(&http.Cookie{Name: CookieName, Value: token})
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200, got %d", resp.StatusCode)
		}
		if !*called {
			t.Error("expected the inner handler to run for a valid cookie")
		}
	})

	t.Run("valid key passes", func(t *testing.T) {
		raw, err := store.EnsureAPIKey(ctx)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		srv, called := middlewareTestServer(t, enc, store)
		req, _ := http.NewRequest(http.MethodGet, srv.URL+"/", nil)
		req.Header.Set("X-Api-Key", raw)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200, got %d", resp.StatusCode)
		}
		if !*called {
			t.Error("expected the inner handler to run for a valid key")
		}
	})

	t.Run("neither passes", func(t *testing.T) {
		srv, called := middlewareTestServer(t, enc, store)
		resp, err := http.Get(srv.URL + "/")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("expected 401, got %d", resp.StatusCode)
		}
		if *called {
			t.Error("inner handler must not run with neither credential")
		}
	})
}

// TestMiddleware_StaleCookieIgnoredOutsidePassword covers Edge Case #3: a
// valid session cookie must never authenticate a request once the active
// mode is not "password" — here, a valid cookie is set but the mode is
// "none" (where it's simply moot, everything passes anyway) and a stubbed
// non-password mode (where the cookie must be ignored and the request
// rejected, since no mode-specific helper honors it and no key is
// presented).
// TestMiddleware_ForwardMode covers AC3/Edge Case #3 for forward mode: a
// correct secret header (with an identity header also present) passes;
// a wrong or absent secret header — even with an identity header present —
// is rejected; and a valid session cookie never substitutes for the
// secret header outside password mode.
func TestMiddleware_ForwardMode(t *testing.T) {
	enc := testEncryptor(t)
	store := newTestStore(t)
	ctx := context.Background()

	raw, err := store.GenerateForwardSecret(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := store.SetAuthMode(ctx, ModeForward); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	userHeader, secretHeader, err := store.ForwardHeaders(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	t.Run("correct secret and identity passes", func(t *testing.T) {
		srv, called := middlewareTestServer(t, enc, store)
		req, _ := http.NewRequest(http.MethodGet, srv.URL+"/", nil)
		req.Header.Set(secretHeader, raw)
		req.Header.Set(userHeader, "wade")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200, got %d", resp.StatusCode)
		}
		if !*called {
			t.Error("expected the inner handler to run for a correct secret")
		}
	})

	t.Run("wrong secret with identity present is rejected", func(t *testing.T) {
		srv, called := middlewareTestServer(t, enc, store)
		req, _ := http.NewRequest(http.MethodGet, srv.URL+"/", nil)
		req.Header.Set(secretHeader, "definitely-the-wrong-secret")
		req.Header.Set(userHeader, "wade")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("expected 401, got %d", resp.StatusCode)
		}
		if *called {
			t.Error("inner handler must not run for a wrong secret, even with identity present")
		}
	})

	t.Run("absent secret with identity present is rejected", func(t *testing.T) {
		srv, called := middlewareTestServer(t, enc, store)
		req, _ := http.NewRequest(http.MethodGet, srv.URL+"/", nil)
		req.Header.Set(userHeader, "wade")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("expected 401, got %d", resp.StatusCode)
		}
		if *called {
			t.Error("inner handler must not run for an absent secret, even with identity present")
		}
	})

	t.Run("stale cookie present but wrong secret is rejected", func(t *testing.T) {
		token, err := IssueToken(enc)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		srv, called := middlewareTestServer(t, enc, store)
		req, _ := http.NewRequest(http.MethodGet, srv.URL+"/", nil)
		req.AddCookie(&http.Cookie{Name: CookieName, Value: token})
		req.Header.Set(secretHeader, "definitely-the-wrong-secret")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("expected a stale cookie to never substitute for a wrong forward secret (401), got %d", resp.StatusCode)
		}
		if *called {
			t.Error("inner handler must not run — a cookie must never authenticate forward mode")
		}
	})
}

// TestMiddleware_AuthentikMode covers AC4/G5: active introspection passes,
// inactive/error/timeout all fail closed to 401. Also covers Edge Case #7 —
// an empty/whitespace bearer must be treated as absent and NEVER trigger an
// introspection call at all (proven here with a call counter; the
// amplification-avoidance proof for the STATUS endpoint specifically lives
// in internal/api/authentik_test.go's TestStatus_AuthentikMode_
// PresenceOnly_NeverIntrospects — this test is about Middleware's own
// dispatch, a different code path).
func TestMiddleware_AuthentikMode(t *testing.T) {
	enc := testEncryptor(t)
	store := newTestStore(t)
	ctx := context.Background()

	var introspectionCalls int32
	var responseMode atomic.Value // "active" | "inactive" | "error"
	responseMode.Store("active")
	fakeIntrospect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&introspectionCalls, 1)
		switch responseMode.Load().(string) {
		case "active":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"active": true}`))
		case "inactive":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"active": false}`))
		case "error":
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer fakeIntrospect.Close()

	cipher, err := enc.Encrypt("the-client-secret")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := store.SetAuthentikConfig(ctx, fakeIntrospect.URL, "the-client-id", cipher); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := store.SetAuthMode(ctx, ModeAuthentik); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	t.Run("active token passes", func(t *testing.T) {
		responseMode.Store("active")
		srv, called := middlewareTestServer(t, enc, store)
		req, _ := http.NewRequest(http.MethodGet, srv.URL+"/", nil)
		req.Header.Set("Authorization", "Bearer some-valid-token")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200 for an active token, got %d", resp.StatusCode)
		}
		if !*called {
			t.Error("expected the inner handler to run for an active token")
		}
	})

	t.Run("inactive token 401", func(t *testing.T) {
		responseMode.Store("inactive")
		srv, called := middlewareTestServer(t, enc, store)
		req, _ := http.NewRequest(http.MethodGet, srv.URL+"/", nil)
		req.Header.Set("Authorization", "Bearer some-inactive-token")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("expected 401 for an inactive token, got %d", resp.StatusCode)
		}
		if *called {
			t.Error("inner handler must not run for an inactive token")
		}
	})

	t.Run("introspection error 401", func(t *testing.T) {
		responseMode.Store("error")
		srv, called := middlewareTestServer(t, enc, store)
		req, _ := http.NewRequest(http.MethodGet, srv.URL+"/", nil)
		req.Header.Set("Authorization", "Bearer any-token")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("expected 401 (fail closed) on an introspection server error, got %d", resp.StatusCode)
		}
		if *called {
			t.Error("inner handler must not run when introspection errors")
		}
	})

	t.Run("empty bearer 401, never introspected", func(t *testing.T) {
		responseMode.Store("active") // would pass if (wrongly) introspected
		before := atomic.LoadInt32(&introspectionCalls)
		srv, called := middlewareTestServer(t, enc, store)
		resp, err := http.Get(srv.URL + "/") // no Authorization header at all
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("expected 401 for an absent bearer, got %d", resp.StatusCode)
		}
		if *called {
			t.Error("inner handler must not run for an absent bearer")
		}
		if got := atomic.LoadInt32(&introspectionCalls); got != before {
			t.Errorf("expected an absent bearer to never trigger introspection (EC7), calls went from %d to %d", before, got)
		}
		// A whitespace-only bearer ("Authorization: Bearer   ", no real
		// token) is covered separately by TestAuthentikAuth_
		// WhitespaceBearer_NeverIntrospected below, calling AuthentikAuth
		// directly against an in-memory *http.Request — real HTTP transit
		// strips trailing OWS from header values (RFC 7230), so a request
		// sent over an actual httptest.NewServer round trip can't
		// distinguish "Bearer" from "Bearer   " at the wire level the way
		// an in-process Request can.
	})

	t.Run("timeout 401", func(t *testing.T) {
		slowServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			time.Sleep(200 * time.Millisecond)
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"active": true}`))
		}))
		defer slowServer.Close()

		slowStore := New(store.settings, enc, &http.Client{Timeout: 20 * time.Millisecond})
		cipher, err := enc.Encrypt("the-client-secret")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if err := slowStore.SetAuthentikConfig(ctx, slowServer.URL, "the-client-id", cipher); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if err := slowStore.SetAuthMode(ctx, ModeAuthentik); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		srv, called := middlewareTestServer(t, enc, slowStore)
		req, _ := http.NewRequest(http.MethodGet, srv.URL+"/", nil)
		req.Header.Set("Authorization", "Bearer any-token")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("expected 401 (fail closed) on a bounded-timeout introspection call, got %d", resp.StatusCode)
		}
		if *called {
			t.Error("inner handler must not run when introspection times out")
		}
	})
}

// TestAuthentikAuth_WhitespaceBearer_NeverIntrospected covers EC7's other
// half against AuthentikAuth directly (not through a real HTTP round trip —
// see the comment in TestMiddleware_AuthentikMode's "empty bearer" subtest
// for why): "Authorization: Bearer   " (whitespace after the scheme, no
// real token) must be treated as absent, never introspected.
func TestAuthentikAuth_WhitespaceBearer_NeverIntrospected(t *testing.T) {
	enc := testEncryptor(t)
	store := newTestStore(t)
	ctx := context.Background()

	var introspected bool
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		introspected = true
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"active": true}`))
	}))
	defer fake.Close()

	cipher, err := enc.Encrypt("the-client-secret")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := store.SetAuthentikConfig(ctx, fake.URL, "the-client-id", cipher); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer    ")
	allowed, err := AuthentikAuth(ctx, store, req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if allowed {
		t.Error("expected a whitespace-only bearer to be rejected")
	}
	if introspected {
		t.Error("expected a whitespace-only bearer to never trigger introspection (EC7)")
	}
}

func TestMiddleware_StaleCookieIgnoredOutsidePassword(t *testing.T) {
	enc := testEncryptor(t)
	store := newTestStore(t)
	ctx := context.Background()
	token, err := IssueToken(enc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if err := store.SetAuthMode(ctx, "some-future-mode-not-yet-implemented"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	srv, called := middlewareTestServer(t, enc, store)
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/", nil)
	req.AddCookie(&http.Cookie{Name: CookieName, Value: token})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected a stale cookie to be ignored outside password mode (401), got %d", resp.StatusCode)
	}
	if *called {
		t.Error("inner handler must not run — a cookie must never authenticate a non-password mode")
	}
}
