package auth

import "net/http"

// proxyIdentityHeaders is the fixed, hardcoded set of reverse-proxy identity
// header names whose presence on a FIRST-RUN request hints a proxied
// deployment — used ONLY to pre-select a setup-wizard default (see
// internal/api's authStatusHandler). It is NEVER read by any authorization
// path (Middleware/ForwardAuth); presence here flips a dropdown, nothing more
// (Scope Risk #2). Not operator-configurable by design (Scope Risk #1) — a
// v1 convenience default matching how forward mode's own header names default
// rather than requiring config (defaultForwardUserHeader, forward.go).
var proxyIdentityHeaders = []string{
	"Remote-User",          // forward mode's own default identity header
	"X-Remote-User",
	"X-Forwarded-User",
	"X-authentik-username", // confirmed live in this deployment's actual env
}

// ProxyHeadersPresent reports whether r carries any recognized reverse-proxy
// identity header. net/http canonicalizes header keys on both send and
// receive, so wire-casing is irrelevant. This is a presence-only hint; it
// authorizes nothing.
func ProxyHeadersPresent(r *http.Request) bool {
	for _, name := range proxyIdentityHeaders {
		if r.Header.Get(name) != "" {
			return true
		}
	}
	return false
}
