package auth

import (
	"context"
	"net/http"
	"strings"
)

// NodeKeyValidator is satisfied by *nodekeys.Store. The interface breaks the
// import cycle: auth does not need to know about the nodekeys package.
type NodeKeyValidator interface {
	Validate(ctx context.Context, rawKey string) (name string, ok bool)
}

// NodeKeyMiddleware validates Authorization: Bearer <rawKey> against the node
// key store. Any other form of authentication (X-Api-Key, session cookie,
// absent header) is rejected with 401 so operator routes and node routes cannot
// substitute for each other.
func NodeKeyMiddleware(store NodeKeyValidator, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "Bearer ") {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		rawKey := strings.TrimPrefix(auth, "Bearer ")
		if _, ok := store.Validate(r.Context(), rawKey); !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}
