package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// fakeKeyStore is a minimal NodeKeyValidator for testing.
type fakeKeyStore struct {
	valid map[string]string // rawKey → name
}

func (f *fakeKeyStore) Validate(_ context.Context, rawKey string) (id, name string, ok bool) {
	name, ok = f.valid[rawKey]
	if !ok {
		return "", "", false
	}
	return "id-" + name, name, true
}

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
}

func TestNodeKeyMiddleware_ValidBearer(t *testing.T) {
	store := &fakeKeyStore{valid: map[string]string{"goodkey": "wade-pc"}}
	h := NodeKeyMiddleware(store, okHandler())

	req := httptest.NewRequest("GET", "/api/nodes/stream", nil)
	req.Header.Set("Authorization", "Bearer goodkey")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

func TestNodeKeyMiddleware_WrongKey(t *testing.T) {
	store := &fakeKeyStore{valid: map[string]string{"goodkey": "wade-pc"}}
	h := NodeKeyMiddleware(store, okHandler())

	req := httptest.NewRequest("GET", "/api/nodes/stream", nil)
	req.Header.Set("Authorization", "Bearer wrongkey")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}

func TestNodeKeyMiddleware_NoHeader(t *testing.T) {
	store := &fakeKeyStore{valid: map[string]string{"goodkey": "wade-pc"}}
	h := NodeKeyMiddleware(store, okHandler())

	req := httptest.NewRequest("GET", "/api/nodes/stream", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}

// Operator-style X-Api-Key header must NOT satisfy NodeKeyMiddleware.
func TestNodeKeyMiddleware_OperatorKeyRejected(t *testing.T) {
	store := &fakeKeyStore{valid: map[string]string{"nodekey": "wade-pc"}}
	h := NodeKeyMiddleware(store, okHandler())

	req := httptest.NewRequest("GET", "/api/nodes/stream", nil)
	req.Header.Set("X-Api-Key", "nodekey") // operator pattern, not bearer
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("X-Api-Key should be rejected by NodeKeyMiddleware; got %d", w.Code)
	}
}

// TestNodeKeyMiddleware_InjectsIdentityIntoContext confirms the new
// context-injection behavior: on a successful validation, the durable id and
// name are readable downstream via NodeIdentityFromContext. Prior to this
// change, NodeKeyMiddleware discarded both and injected nothing.
func TestNodeKeyMiddleware_InjectsIdentityIntoContext(t *testing.T) {
	store := &fakeKeyStore{valid: map[string]string{"goodkey": "wade-pc"}}

	var gotID, gotName string
	var gotOK bool
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotID, gotName, gotOK = NodeIdentityFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})
	h := NodeKeyMiddleware(store, next)

	req := httptest.NewRequest("GET", "/api/nodes/stream", nil)
	req.Header.Set("Authorization", "Bearer goodkey")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if !gotOK {
		t.Fatal("expected NodeIdentityFromContext to find an injected identity")
	}
	if gotName != "wade-pc" {
		t.Fatalf("name: got %q, want wade-pc", gotName)
	}
	if gotID != "id-wade-pc" {
		t.Fatalf("id: got %q, want id-wade-pc", gotID)
	}
}

// TestNodeIdentityFromContext_AbsentWithoutMiddleware confirms the accessor
// returns ok=false when called outside a request that passed through
// NodeKeyMiddleware, rather than panicking or returning zero-value garbage
// indistinguishable from a real (if empty) identity.
func TestNodeIdentityFromContext_AbsentWithoutMiddleware(t *testing.T) {
	_, _, ok := NodeIdentityFromContext(context.Background())
	if ok {
		t.Fatal("expected ok=false for a context never touched by NodeKeyMiddleware")
	}
}
