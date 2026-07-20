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

func (f *fakeKeyStore) Validate(_ context.Context, rawKey string) (string, bool) {
	name, ok := f.valid[rawKey]
	return name, ok
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
