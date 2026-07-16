package imageproxy

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// newTestProxy builds a Proxy whose test-only private-host exemption and
// cache are caller-supplied — used only by this package's tests to point
// Fetch at an httptest upstream (the production guardrail rejects
// 127.0.0.1). Kept unexported so no test-only bypass leaks into the
// production API surface.
func newTestProxy(client *http.Client, c *cache, allowPrivateHosts map[string]bool) *Proxy {
	return &Proxy{client: newGuardedClient(client, allowPrivateHosts), cache: c, allowPrivateHosts: allowPrivateHosts}
}

// TestIsBlockedIP covers the address-classification core of the SSRF guard —
// all literal IPs so no DNS is touched, mirroring internal/netscan's own
// TestValidatePrivateHost convention exactly (see that test's doc comment).
func TestIsBlockedIP(t *testing.T) {
	cases := []struct {
		ip      string
		blocked bool
	}{
		{"192.168.1.5", true},
		{"10.0.0.5", true},
		{"172.16.4.4", true},
		{"127.0.0.1", true},
		{"169.254.10.10", true},   // link-local unicast — cloud metadata range
		{"169.254.169.254", true}, // the cloud metadata endpoint itself
		{"0.0.0.0", true},         // unspecified
		{"::1", true},
		{"fc00::1", true}, // ULA
		{"::", true},      // unspecified, IPv6
		{"8.8.8.8", false},
		{"1.1.1.1", false},
		{"93.184.216.34", false}, // example.com's public IP
	}
	for _, c := range cases {
		ip := net.ParseIP(c.ip)
		if ip == nil {
			t.Fatalf("net.ParseIP(%q) failed", c.ip)
		}
		if got := isBlockedIP(ip); got != c.blocked {
			t.Errorf("isBlockedIP(%s) = %v, want %v", c.ip, got, c.blocked)
		}
	}
}

// TestValidateHostNotPrivate_LiteralIPs proves the resolve-then-check wrapper
// correctly short-circuits for an IP literal (no DNS lookup needed) — the
// dominant case for the cases TestIsBlockedIP already covers exhaustively.
func TestValidateHostNotPrivate_LiteralIPs(t *testing.T) {
	if err := validateHostNotPrivate(context.Background(), "8.8.8.8"); err != nil {
		t.Errorf("public IP literal rejected: %v", err)
	}
	if err := validateHostNotPrivate(context.Background(), "127.0.0.1"); err == nil {
		t.Error("loopback IP literal accepted, want rejected")
	}
	if err := validateHostNotPrivate(context.Background(), "10.1.10.3"); !errors.Is(err, ErrHostNotAllowed) {
		t.Errorf("private IP literal's error = %v, want wrapping ErrHostNotAllowed", err)
	}
}

// TestValidate_SchemeAndSyntax covers Validate's local, DNS-free checks
// (scheme/parse/empty-host) — the parts that stay pure regardless of the
// resolve-then-check host guard. Uses IP-literal hosts throughout so no test
// in this file touches real DNS (matching internal/netscan's convention);
// TestIsBlockedIP/TestValidateHostNotPrivate_LiteralIPs cover the resolve
// step directly, and the Fetch tests below cover it end-to-end via
// newTestProxy's allowPrivateHosts escape hatch.
func TestValidate_SchemeAndSyntax(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		wantErr error // nil = accept; otherwise the sentinel it must wrap
	}{
		{"public ip https", "https://93.184.216.34/x.jpg", nil},
		{"public ip with query", "https://93.184.216.34/x.jpg?v=2", nil},

		{"loopback ip", "https://127.0.0.1/x.jpg", ErrHostNotAllowed},
		{"private ip", "https://10.1.10.3/secret", ErrHostNotAllowed},
		{"cloud metadata endpoint", "https://169.254.169.254/latest/meta-data/", ErrHostNotAllowed},

		{"http not https", "http://93.184.216.34/x.jpg", ErrInvalidURL},
		{"file scheme", "file:///etc/passwd", ErrInvalidURL},
		{"gopher scheme", "gopher://93.184.216.34/x", ErrInvalidURL},
		{"data uri", "data:image/png;base64,AAAA", ErrInvalidURL},
		{"scheme-relative", "//93.184.216.34/x.jpg", ErrInvalidURL},
		{"empty", "", ErrInvalidURL},
		{"whitespace only", "   ", ErrInvalidURL},
		{"garbage", "://::::", ErrInvalidURL},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			u, err := Validate(context.Background(), tc.raw)
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

// allowPrivateHostFor builds the test-only private-host exemption for the
// httptest server's own host, so Fetch tests can point at a fake upstream
// (the production guardrail rejects 127.0.0.1, which is exactly what
// httptest.NewTLSServer listens on). Uses the unexported test constructor by
// design.
func allowPrivateHostFor(t *testing.T, serverURL string) map[string]bool {
	t.Helper()
	u, err := url.Parse(serverURL)
	if err != nil {
		t.Fatalf("parsing test server url: %v", err)
	}
	return map[string]bool{strings.ToLower(u.Hostname()): true}
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

	p := newTestProxy(srv.Client(), newCache(defaultCacheCap, defaultCacheTTL), allowPrivateHostFor(t, srv.URL))
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

	p := newTestProxy(srv.Client(), newCache(defaultCacheCap, defaultCacheTTL), allowPrivateHostFor(t, srv.URL))
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

// TestFetch_RejectsBeforeUpstream proves a private-address URL never triggers
// an outbound request — the SSRF gate runs before any I/O. Uses an IP literal
// (no DNS) so this test never touches the network for its own rejected URL —
// only the exempted httptest server (an unrelated host) is ever contacted,
// and hits stays 0 either way.
func TestFetch_RejectsBeforeUpstream(t *testing.T) {
	var hits int
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
	}))
	defer srv.Close()

	// Exempt only the test server's own host; ask for a different, private one.
	p := newTestProxy(srv.Client(), newCache(defaultCacheCap, defaultCacheTTL), allowPrivateHostFor(t, srv.URL))
	_, err := p.Fetch(context.Background(), "https://10.1.10.3/x.jpg")
	if !errors.Is(err, ErrHostNotAllowed) {
		t.Fatalf("Fetch of a private address = %v, want ErrHostNotAllowed", err)
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

	p := newTestProxy(srv.Client(), newCache(defaultCacheCap, defaultCacheTTL), allowPrivateHostFor(t, srv.URL))
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

	p := newTestProxy(srv.Client(), newCache(defaultCacheCap, defaultCacheTTL), allowPrivateHostFor(t, srv.URL))
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

// TestFetch_RefusesOffAllowlistRedirect proves the SSRF gate is enforced on
// redirects, not just the initial URL: an exempted upstream that 3xx-es to a
// private-address target must not be followed server-side. This is the
// redirect-SSRF fix — net/http's default client would blindly follow it.
func TestFetch_RefusesOffAllowlistRedirect(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Redirect to a private address. The guard must refuse this next hop
		// before any request is made to it.
		http.Redirect(w, r, "https://10.1.10.3/internal/x.jpg", http.StatusFound)
	}))
	defer srv.Close()

	// Exempt only the test server's own host; the redirect target is private.
	p := newTestProxy(srv.Client(), newCache(defaultCacheCap, defaultCacheTTL), allowPrivateHostFor(t, srv.URL))
	_, err := p.Fetch(context.Background(), srv.URL+"/poster.jpg")
	if err == nil {
		t.Fatal("Fetch followed a redirect to a private address, want error")
	}
	// The refusal must surface as the same sentinel the initial-URL check
	// uses, not a generic transport error.
	if !errors.Is(err, ErrHostNotAllowed) {
		t.Fatalf("Fetch redirect refusal = %v, want wrapping ErrHostNotAllowed", err)
	}
}

// TestFetch_FollowsAllowlistedRedirect proves the guard is not over-broad:
// a redirect whose target is itself allowed is still followed, so legitimate
// CDN redirects (e.g. between two hosts a studio/CDN uses) keep working.
func TestFetch_FollowsAllowlistedRedirect(t *testing.T) {
	const wantBody = "\x89PNGREDIRECTEDIMAGE"
	// Final destination: serves the actual image bytes.
	dest := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Write([]byte(wantBody))
	}))
	defer dest.Close()

	// First hop: 302s to the destination.
	origin := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, dest.URL+"/final.png", http.StatusFound)
	}))
	defer origin.Close()

	// Both servers listen on 127.0.0.1 (different ports); allowPrivateHostFor
	// keys on Hostname() with the port stripped, so a single 127.0.0.1 entry
	// exempts both hops. Use a client that trusts both self-signed certs.
	client := &http.Client{Transport: &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}}
	p := newTestProxy(client, newCache(defaultCacheCap, defaultCacheTTL), allowPrivateHostFor(t, origin.URL))

	img, err := p.Fetch(context.Background(), origin.URL+"/poster.png")
	if err != nil {
		t.Fatalf("Fetch of allowlisted redirect chain: %v", err)
	}
	if string(img.Body) != wantBody {
		t.Fatalf("body = %q, want %q (should have followed to destination)", img.Body, wantBody)
	}
	if img.ContentType != "image/png" {
		t.Fatalf("content type = %q, want image/png", img.ContentType)
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

	p := newTestProxy(srv.Client(), newCache(defaultCacheCap, time.Minute), allowPrivateHostFor(t, srv.URL))
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

	p := newTestProxy(srv.Client(), newCache(defaultCacheCap, defaultCacheTTL), allowPrivateHostFor(t, srv.URL))
	_, err := p.Fetch(context.Background(), srv.URL+"/huge.png")
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("Fetch of over-cap image = %v, want size-cap error", err)
	}
}
