package auth

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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
