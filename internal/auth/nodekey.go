package auth

import (
	"context"
	"net/http"
	"strings"
)

// NodeKeyValidator is satisfied by *nodekeys.Store. The interface breaks the
// import cycle: auth does not need to know about the nodekeys package.
type NodeKeyValidator interface {
	Validate(ctx context.Context, rawKey string) (id, name string, ok bool)
}

type nodeIdentityContextKey struct{}

// nodeIdentity is what NodeKeyMiddleware injects into the request context on
// a successful validation.
type nodeIdentity struct {
	id   string
	name string
}

// NodeIdentityFromContext returns the durable node id and name that
// NodeKeyMiddleware validated for this request. ok is false if called outside
// a request that passed through NodeKeyMiddleware.
func NodeIdentityFromContext(ctx context.Context) (id, name string, ok bool) {
	v, ok := ctx.Value(nodeIdentityContextKey{}).(nodeIdentity)
	if !ok {
		return "", "", false
	}
	return v.id, v.name, true
}

// NodeKeyMiddleware validates Authorization: Bearer <rawKey> against the node
// key store. Any other form of authentication (X-Api-Key, session cookie,
// absent header) is rejected with 401 so operator routes and node routes cannot
// substitute for each other. On success, the validated node's durable id and
// name are injected into the request context via NodeIdentityFromContext —
// this is new plumbing; prior to this, a successful validation discarded both
// values and injected nothing.
func NodeKeyMiddleware(store NodeKeyValidator, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "Bearer ") {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		rawKey := strings.TrimPrefix(auth, "Bearer ")
		id, name, ok := store.Validate(r.Context(), rawKey)
		if !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		ctx := context.WithValue(r.Context(), nodeIdentityContextKey{}, nodeIdentity{id: id, name: name})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
