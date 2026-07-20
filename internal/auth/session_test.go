package auth

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labbersanon/sakms/internal/secrets"
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
	SetSessionCookie(rec, token, false)
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

// TestMiddleware_UnaffectedByProxyIdentityHeaders proves a reverse-proxy
// identity header never influences a real authorization decision. Password
// mode (the default), a protected route, and a request carrying two such
// headers but no valid credential at all must still 401 — a spoofed header
// can never bypass Middleware.
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

// TestMiddleware_OIDCMode covers oidc mode's per-request gate, which is
// cookie-only: after the operator completes the IdP redirect dance (exercised
// end-to-end at the api layer, see internal/api's oidc_test.go), the callback
// issues the SAME session cookie password mode uses, so Middleware's ongoing
// check is identical to password mode's. A valid cookie passes; no cookie
// (and no key) is rejected. No OIDC config or discovery is needed here — the
// per-request path never calls the IdP.
func TestMiddleware_OIDCMode(t *testing.T) {
	enc := testEncryptor(t)
	store := newTestStore(t)
	ctx := context.Background()
	if err := store.SetAuthMode(ctx, ModeOIDC); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	t.Run("valid session cookie passes", func(t *testing.T) {
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
			t.Fatalf("expected 200 for a valid session cookie in oidc mode, got %d", resp.StatusCode)
		}
		if !*called {
			t.Error("expected the inner handler to run for a valid cookie in oidc mode")
		}
	})

	t.Run("no cookie and no key 401", func(t *testing.T) {
		srv, called := middlewareTestServer(t, enc, store)
		resp, err := http.Get(srv.URL + "/")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("expected 401 with neither cookie nor key in oidc mode, got %d", resp.StatusCode)
		}
		if *called {
			t.Error("inner handler must not run with neither credential in oidc mode")
		}
	})
}

// TestMiddleware_StaleCookieIgnoredOutsidePassword covers fail-closed
// dispatch: a valid session cookie must never authenticate a request once the
// active mode is an unknown/corrupt value (neither password nor oidc, both of
// which legitimately honor the cookie, nor "none", which passes everything).
// The cookie is ignored and the request rejected, since the default switch
// arm honors no credential and no key is presented.
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
