package imageproxy

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// TestValidate_Allowlist mirrors internal/netscan's SSRF test rigor, inverted:
// real TMDB/TPDB image URLs must pass; arbitrary, internal, look-alike, and
// non-https URLs must be rejected — and rejected with the right error class
// (ErrInvalidURL for malformed/non-https, ErrHostNotAllowed for a valid https
// URL pointing at a disallowed host) so the handler can map them to the right
// status code.
func TestValidate_Allowlist(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		wantErr error // nil = accept; otherwise the sentinel it must wrap
	}{
		// --- accept: real allowlisted image URLs ---
		{"tmdb poster", "https://image.tmdb.org/t/p/w342/abc123.jpg", nil},
		{"tmdb original with query", "https://image.tmdb.org/t/p/original/x.jpg?v=2", nil},
		{"tpdb metadataapi cdn", "https://cdn.metadataapi.net/scenes/poster.jpg", nil},
		{"tpdb thumbs subdomain", "https://thumbs.metadataapi.net/s/1/2.jpg?w=300&h=200", nil},
		{"tpdb theporndb domain apex", "https://theporndb.net/img/a.jpg", nil},
		{"tpdb theporndb cdn subdomain", "https://cdn.theporndb.net/img/a.jpg", nil},

		// --- reject: wrong host (valid https) -> ErrHostNotAllowed ---
		{"arbitrary host", "https://evil.example.com/x.jpg", ErrHostNotAllowed},
		{"tmdb suffix bypass", "https://image.tmdb.org.evil.com/x.jpg", ErrHostNotAllowed},
		{"tpdb suffix bypass", "https://evilmetadataapi.net/x.jpg", ErrHostNotAllowed},
		{"tpdb domain as substring", "https://metadataapi.net.evil.com/x.jpg", ErrHostNotAllowed},
		{"cloud metadata endpoint", "https://169.254.169.254/latest/meta-data/", ErrHostNotAllowed},
		{"internal ip", "https://10.1.10.3/secret", ErrHostNotAllowed},
		{"bare localhost", "https://localhost/x", ErrHostNotAllowed},
		{"tmdb non-image host", "https://www.themoviedb.org/x.jpg", ErrHostNotAllowed},

		// --- reject: malformed / wrong scheme -> ErrInvalidURL ---
		{"http not https", "http://image.tmdb.org/t/p/w342/x.jpg", ErrInvalidURL},
		{"file scheme", "file:///etc/passwd", ErrInvalidURL},
		{"gopher scheme", "gopher://image.tmdb.org/x", ErrInvalidURL},
		{"data uri", "data:image/png;base64,AAAA", ErrInvalidURL},
		{"scheme-relative", "//image.tmdb.org/x.jpg", ErrInvalidURL},
		{"empty", "", ErrInvalidURL},
		{"whitespace only", "   ", ErrInvalidURL},
		{"garbage", "://::::", ErrInvalidURL},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			u, err := Validate(tc.raw)
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("Validate(%q) = error %v, want accept", tc.raw, err)
				}
				if u == nil {
					t.Fatalf("Validate(%q) returned nil URL on accept", tc.raw)
				}
				return
			}
			if err == nil {
				t.Fatalf("Validate(%q) accepted, want reject (%v)", tc.raw, tc.wantErr)
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("Validate(%q) = %v, want wrapping %v", tc.raw, err, tc.wantErr)
			}
		})
	}
}

// hostRuleFor builds an exact allowlist rule for the httptest server's host so
// Fetch tests can point at a fake upstream (the production allowlist rejects
// 127.0.0.1). Uses the unexported test constructor by design.
func hostRuleFor(t *testing.T, serverURL string) []hostRule {
	t.Helper()
	u, err := url.Parse(serverURL)
	if err != nil {
		t.Fatalf("parsing test server url: %v", err)
	}
	return []hostRule{exactHost(u.Hostname())}
}

// TestFetch_StreamsFromUpstream proves a validated URL is fetched server-side
// and its bytes + content type are returned faithfully.
func TestFetch_StreamsFromUpstream(t *testing.T) {
	const wantBody = "\x89PNG\r\n\x1a\nFAKEIMAGEBYTES"
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Write([]byte(wantBody))
	}))
	defer srv.Close()

	p := newTestProxy(srv.Client(), newCache(defaultCacheCap, defaultCacheTTL), hostRuleFor(t, srv.URL))
	img, err := p.Fetch(context.Background(), srv.URL+"/poster.png")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if string(img.Body) != wantBody {
		t.Fatalf("body = %q, want %q", img.Body, wantBody)
	}
	if img.ContentType != "image/png" {
		t.Fatalf("content type = %q, want image/png", img.ContentType)
	}
	if img.FromCache {
		t.Fatal("first fetch reported FromCache=true, want false")
	}
}

// TestFetch_CacheHit proves a second request for the same URL is served from
// cache without re-hitting the upstream (the core Stage 1 caching requirement).
func TestFetch_CacheHit(t *testing.T) {
	var hits int
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set("Content-Type", "image/jpeg")
		w.Write([]byte("jpegbytes"))
	}))
	defer srv.Close()

	p := newTestProxy(srv.Client(), newCache(defaultCacheCap, defaultCacheTTL), hostRuleFor(t, srv.URL))
	u := srv.URL + "/same.jpg?w=342"

	first, err := p.Fetch(context.Background(), u)
	if err != nil {
		t.Fatalf("first Fetch: %v", err)
	}
	if first.FromCache {
		t.Fatal("first fetch FromCache=true, want false")
	}

	second, err := p.Fetch(context.Background(), u)
	if err != nil {
		t.Fatalf("second Fetch: %v", err)
	}
	if !second.FromCache {
		t.Fatal("second fetch FromCache=false, want true (should be a cache hit)")
	}
	if hits != 1 {
		t.Fatalf("upstream hit %d times, want exactly 1 (second request must not re-hit)", hits)
	}
	if string(second.Body) != string(first.Body) {
		t.Fatalf("cached body %q != original %q", second.Body, first.Body)
	}
}

// TestFetch_RejectsBeforeUpstream proves an off-allowlist URL never triggers an
// outbound request — the SSRF gate runs before any I/O.
func TestFetch_RejectsBeforeUpstream(t *testing.T) {
	var hits int
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
	}))
	defer srv.Close()

	// Allowlist only the test server's host; ask for a different host.
	p := newTestProxy(srv.Client(), newCache(defaultCacheCap, defaultCacheTTL), hostRuleFor(t, srv.URL))
	_, err := p.Fetch(context.Background(), "https://evil.example.com/x.jpg")
	if !errors.Is(err, ErrHostNotAllowed) {
		t.Fatalf("Fetch off-allowlist = %v, want ErrHostNotAllowed", err)
	}
	if hits != 0 {
		t.Fatalf("upstream was contacted %d times for a rejected URL, want 0", hits)
	}
}

// TestFetch_RejectsNonImageContent proves defense-in-depth: even an allowlisted
// host cannot be used to proxy non-image content, and such a response is not
// cached.
func TestFetch_RejectsNonImageContent(t *testing.T) {
	var hits int
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte("<html>not an image</html>"))
	}))
	defer srv.Close()

	p := newTestProxy(srv.Client(), newCache(defaultCacheCap, defaultCacheTTL), hostRuleFor(t, srv.URL))
	u := srv.URL + "/x.jpg"
	if _, err := p.Fetch(context.Background(), u); err == nil {
		t.Fatal("Fetch of non-image content succeeded, want error")
	}
	// A rejected (non-image) response must not be cached.
	if _, err := p.Fetch(context.Background(), u); err == nil {
		t.Fatal("second Fetch of non-image content succeeded, want error (must not be cached)")
	}
	if hits != 2 {
		t.Fatalf("upstream hit %d times, want 2 (error responses must not be cached)", hits)
	}
}

// TestFetch_DoesNotCacheErrorStatus proves a transient upstream failure is not
// pinned for the whole TTL.
func TestFetch_DoesNotCacheErrorStatus(t *testing.T) {
	var hits int
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if hits == 1 {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		w.Write([]byte("ok"))
	}))
	defer srv.Close()

	p := newTestProxy(srv.Client(), newCache(defaultCacheCap, defaultCacheTTL), hostRuleFor(t, srv.URL))
	u := srv.URL + "/flaky.png"
	if _, err := p.Fetch(context.Background(), u); err == nil {
		t.Fatal("first Fetch (500) succeeded, want error")
	}
	img, err := p.Fetch(context.Background(), u)
	if err != nil {
		t.Fatalf("second Fetch (200) = %v, want success (500 must not have been cached)", err)
	}
	if img.FromCache {
		t.Fatal("second fetch FromCache=true — the failed first response was cached")
	}
	if hits != 2 {
		t.Fatalf("upstream hit %d times, want 2", hits)
	}
}

// TestCache_TTLExpiry proves an entry past its TTL is a miss and re-fetched.
func TestCache_TTLExpiry(t *testing.T) {
	orig := nowFunc
	defer func() { nowFunc = orig }()
	base := time.Unix(1_700_000_000, 0)
	nowFunc = func() time.Time { return base }

	var hits int
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set("Content-Type", "image/png")
		w.Write([]byte("bytes"))
	}))
	defer srv.Close()

	p := newTestProxy(srv.Client(), newCache(defaultCacheCap, time.Minute), hostRuleFor(t, srv.URL))
	u := srv.URL + "/ttl.png"

	if _, err := p.Fetch(context.Background(), u); err != nil {
		t.Fatalf("first Fetch: %v", err)
	}
	// Within TTL: cache hit.
	nowFunc = func() time.Time { return base.Add(30 * time.Second) }
	img, _ := p.Fetch(context.Background(), u)
	if !img.FromCache {
		t.Fatal("within-TTL fetch was not a cache hit")
	}
	// Past TTL: miss, re-fetch.
	nowFunc = func() time.Time { return base.Add(2 * time.Minute) }
	img, err := p.Fetch(context.Background(), u)
	if err != nil {
		t.Fatalf("post-TTL Fetch: %v", err)
	}
	if img.FromCache {
		t.Fatal("post-TTL fetch was a cache hit, want a fresh fetch")
	}
	if hits != 2 {
		t.Fatalf("upstream hit %d times, want 2 (one initial + one after expiry)", hits)
	}
}

// TestCache_LRUEviction proves the least-recently-used entry is evicted at
// capacity.
func TestCache_LRUEviction(t *testing.T) {
	c := newCache(2, time.Hour)
	c.put("a", &Image{Body: []byte("a")})
	c.put("b", &Image{Body: []byte("b")})
	// Touch "a" so "b" becomes LRU.
	if _, ok := c.get("a"); !ok {
		t.Fatal("get(a) miss")
	}
	c.put("c", &Image{Body: []byte("c")}) // evicts "b"
	if _, ok := c.get("b"); ok {
		t.Fatal("b should have been evicted as LRU")
	}
	if _, ok := c.get("a"); !ok {
		t.Fatal("a should still be present")
	}
	if _, ok := c.get("c"); !ok {
		t.Fatal("c should be present")
	}
}

// TestFetch_OverSizeCap proves an over-cap body is rejected, not truncated.
func TestFetch_OverSizeCap(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		big := make([]byte, maxImageBytes+10)
		w.Write(big)
	}))
	defer srv.Close()

	p := newTestProxy(srv.Client(), newCache(defaultCacheCap, defaultCacheTTL), hostRuleFor(t, srv.URL))
	_, err := p.Fetch(context.Background(), srv.URL+"/huge.png")
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("Fetch of over-cap image = %v, want size-cap error", err)
	}
}
