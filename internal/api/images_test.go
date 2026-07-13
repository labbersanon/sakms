package api

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

// TestImageProxyHandler_StatusMapping verifies the handler's request-vs-gateway
// error split at the HTTP boundary: a missing or off-allowlist url is a 400
// (operator request error, no upstream contacted), not a 502. The happy path
// (a real image streamed) is covered at the imageproxy package layer, which can
// point at a fake upstream; here the production allowlist is in force, so only
// the reject paths are exercisable without a real TMDB/TPDB host.
func TestImageProxyHandler_StatusMapping(t *testing.T) {
	srv := httptest.NewServer(imageProxyHandler(&http.Client{}))
	defer srv.Close()

	cases := []struct {
		name       string
		query      string
		wantStatus int
	}{
		{"missing url param", "", http.StatusBadRequest},
		{"empty url param", "url=", http.StatusBadRequest},
		{"off-allowlist host", "url=" + url.QueryEscape("https://evil.example.com/x.jpg"), http.StatusBadRequest},
		{"non-https scheme", "url=" + url.QueryEscape("http://image.tmdb.org/t/p/w342/x.jpg"), http.StatusBadRequest},
		{"suffix bypass attempt", "url=" + url.QueryEscape("https://image.tmdb.org.evil.com/x.jpg"), http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := http.Get(srv.URL + "/api/images/proxy?" + tc.query)
			if err != nil {
				t.Fatalf("GET failed: %v", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != tc.wantStatus {
				t.Fatalf("status = %d, want %d", resp.StatusCode, tc.wantStatus)
			}
		})
	}
}
