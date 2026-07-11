package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestProxyHeadersPresent covers AC1's table: all four recognized header
// names, plus negative/empty-value cases — presence-only, no authorization
// significance (see ProxyHeadersPresent's doc comment).
func TestProxyHeadersPresent(t *testing.T) {
	for _, name := range proxyIdentityHeaders {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.Header.Set(name, "someone")
			if !ProxyHeadersPresent(req) {
				t.Errorf("expected ProxyHeadersPresent to report true when %q is set", name)
			}
		})
	}

	t.Run("no headers set", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		if ProxyHeadersPresent(req) {
			t.Error("expected ProxyHeadersPresent to report false with no recognized headers present")
		}
	})

	t.Run("empty header value not treated as present", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		for _, name := range proxyIdentityHeaders {
			req.Header.Set(name, "")
		}
		if ProxyHeadersPresent(req) {
			t.Error("expected empty header values to not count as present")
		}
	})

	t.Run("unrecognized header ignored", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("X-Some-Other-Header", "value")
		if ProxyHeadersPresent(req) {
			t.Error("expected an unrecognized header to never trigger detection")
		}
	})
}
