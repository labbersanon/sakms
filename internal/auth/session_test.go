package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/curtiswtaylorjr/sak/internal/secrets"
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
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("inner handler must not run for an unauthenticated request")
	})
	srv := httptest.NewServer(Middleware(enc, inner))
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
	var innerCalled bool
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { innerCalled = true; w.WriteHeader(http.StatusOK) })
	srv := httptest.NewServer(Middleware(enc, inner))
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
