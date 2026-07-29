package imageproxy

import (
	"context"
	"crypto/tls"
	"database/sql"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// newTestProxy builds a Proxy whose test-only private-host exemption and
// cache are caller-supplied — used only by this package's tests to point
// Fetch at an httptest upstream (the production guardrail rejects
// 127.0.0.1). Kept unexported so no test-only bypass leaks into the
// production API surface.
func newTestProxy(client *http.Client, c *cache, allowPrivateHosts map[string]bool) *Proxy {
	return newTestProxyWithDurable(client, c, allowPrivateHosts, nil)
}

// newTestProxyWithDurable is newTestProxy plus an explicit durable store
// (nil = the same nil-durable behavior every existing Fetch test already
// exercises). Used by the US-3 durable-path tests below.
func newTestProxyWithDurable(client *http.Client, c *cache, allowPrivateHosts map[string]bool, durable *DurableStore) *Proxy {
	return &Proxy{
		client:            newGuardedClient(client, allowPrivateHosts),
		cache:             c,
		durable:           durable,
		allowPrivateHosts: allowPrivateHosts,
	}
}

// adultImageCacheDDL mirrors migration 0046 exactly (see
// internal/db/migrations/0046_adult_image_cache.sql) so these tests build a
// real DurableStore-backed DB without pulling in goose/the full migration
// chain — the DurableStore only ever touches this one table.
const adultImageCacheDDL = `
CREATE TABLE adult_image_cache (
    url_sha256   TEXT    NOT NULL PRIMARY KEY,
    source_url   TEXT    NOT NULL,
    content_type TEXT    NOT NULL,
    byte_size    INTEGER NOT NULL DEFAULT 0,
    row_type     TEXT    NOT NULL DEFAULT '',
    fetched_at   TEXT    NOT NULL
);
CREATE INDEX adult_image_cache_fetched_at ON adult_image_cache (fetched_at);`

// newTestDurable returns a DurableStore backed by a fresh temp-file SQLite DB
// (SetMaxOpenConns(1), matching production db.Open) and a temp bytes dir, plus
// the *sql.DB so a test can assert on the index directly.
func newTestDurable(t *testing.T) (*DurableStore, *sql.DB) {
	t.Helper()
	dir := t.TempDir()
	sqlDB, err := sql.Open("sqlite", filepath.Join(dir, "sakms.db"))
	if err != nil {
		t.Fatalf("opening test db: %v", err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	sqlDB.SetMaxOpenConns(1)
	if _, err := sqlDB.Exec(adultImageCacheDDL); err != nil {
		t.Fatalf("creating adult_image_cache: %v", err)
	}
	store, err := NewDurableStore(sqlDB, filepath.Join(dir, "imagecache"))
	if err != nil {
		t.Fatalf("NewDurableStore: %v", err)
	}
	return store, sqlDB
}

// durableRowCount returns the number of rows in the durable index — used to
// assert a rejected/skipped FetchAndPersist wrote nothing.
func durableRowCount(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM adult_image_cache`).Scan(&n); err != nil {
		t.Fatalf("counting adult_image_cache rows: %v", err)
	}
	return n
}

// keyOf resolves the exact durable key Fetch/FetchAndPersist would compute for
// raw (validate → keyFor → u.String()), so a test can pre-seed or assert the
// durable store under the identical key without duplicating the derivation.
func keyOf(t *testing.T, raw string, allowPrivateHosts map[string]bool) string {
	t.Helper()
	u, err := validate(context.Background(), raw, allowPrivateHosts)
	if err != nil {
		t.Fatalf("validate(%q): %v", raw, err)
	}
	return u.String()
}

// errRoundTripper fails every request — used to prove a code path made ZERO
// outbound calls (a durable/LRU hit that short-circuits before any Do). It
// counts calls so a test can assert exactly zero.
type errRoundTripper struct{ calls *int }

func (e errRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	*e.calls++
	return nil, errors.New("no network allowed in this test")
}

// noNetworkClient returns an *http.Client that errors on any request, plus a
// pointer to its call counter.
func noNetworkClient() (*http.Client, *int) {
	var calls int
	return &http.Client{Transport: errRoundTripper{calls: &calls}}, &calls
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

// --- US-3: durable-aware Fetch, FetchAndPersist, nil-durable regression ---

// TestFetch_DurableHitShortCircuitsBeforeHTTP proves the headline ordering
// invariant: with a durable != nil holding the key, Fetch returns the stored
// bytes WITHOUT any HTTP call. Proven with a client that errors on any Do — if
// Fetch reached the network it would error and the call counter would be > 0.
func TestFetch_DurableHitShortCircuitsBeforeHTTP(t *testing.T) {
	durable, _ := newTestDurable(t)
	const raw = "https://93.184.216.34/poster.png" // public IP literal: no DNS, passes validate
	const wantBody = "\x89PNGDURABLEBYTES"
	key := keyOf(t, raw, nil)
	if err := durable.Put(context.Background(), key, "image/png", []byte(wantBody)); err != nil {
		t.Fatalf("seeding durable: %v", err)
	}

	client, calls := noNetworkClient()
	p := newTestProxyWithDurable(client, newCache(defaultCacheCap, defaultCacheTTL), nil, durable)

	img, err := p.Fetch(context.Background(), raw)
	if err != nil {
		t.Fatalf("Fetch on durable hit = %v, want success (no network)", err)
	}
	if string(img.Body) != wantBody {
		t.Fatalf("body = %q, want %q", img.Body, wantBody)
	}
	if img.ContentType != "image/png" {
		t.Fatalf("content type = %q, want image/png", img.ContentType)
	}
	if !img.FromCache {
		t.Fatal("durable hit reported FromCache=false, want true")
	}
	if *calls != 0 {
		t.Fatalf("durable hit made %d HTTP calls, want 0 (must short-circuit before any Do)", *calls)
	}
}

// TestFetch_DurableMissLRUMiss_OneUpstreamCall_NoDurableWrite proves the miss
// path: durable miss + LRU miss performs EXACTLY one upstream call, populates
// the in-memory LRU (a second Fetch is a FromCache hit with no new upstream
// call), and NEVER writes the durable store (Fetch is read-only w.r.t. durable).
func TestFetch_DurableMissLRUMiss_OneUpstreamCall_NoDurableWrite(t *testing.T) {
	var hits int
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set("Content-Type", "image/png")
		w.Write([]byte("upstreambytes"))
	}))
	defer srv.Close()

	durable, db := newTestDurable(t)
	allow := allowPrivateHostFor(t, srv.URL)
	p := newTestProxyWithDurable(srv.Client(), newCache(defaultCacheCap, defaultCacheTTL), allow, durable)
	u := srv.URL + "/x.png"

	first, err := p.Fetch(context.Background(), u)
	if err != nil {
		t.Fatalf("first Fetch: %v", err)
	}
	if first.FromCache {
		t.Fatal("first fetch FromCache=true, want false (durable+LRU both miss)")
	}
	if hits != 1 {
		t.Fatalf("after first Fetch upstream hit %d times, want exactly 1", hits)
	}
	// Fetch must not have written the durable store.
	if n := durableRowCount(t, db); n != 0 {
		t.Fatalf("durable index has %d rows after Fetch, want 0 (Fetch must never write durable)", n)
	}
	if has, err := durable.Has(context.Background(), keyOf(t, u, allow)); err != nil || has {
		t.Fatalf("durable.Has after Fetch = (%v, %v), want (false, nil)", has, err)
	}

	// Second Fetch is served by the in-memory LRU: FromCache, no new upstream call.
	second, err := p.Fetch(context.Background(), u)
	if err != nil {
		t.Fatalf("second Fetch: %v", err)
	}
	if !second.FromCache {
		t.Fatal("second fetch FromCache=false, want true (LRU should be populated)")
	}
	if hits != 1 {
		t.Fatalf("after second Fetch upstream hit %d times, want still 1 (LRU serves it)", hits)
	}
}

// TestFetchAndPersist_RejectsPrivateURL_NoDurableWrite proves FetchAndPersist
// runs the SAME SSRF guard as Fetch: a private/loopback target is rejected with
// ErrHostNotAllowed and nothing is written to durable. Uses a no-network client
// to also confirm rejection happens before any outbound attempt.
func TestFetchAndPersist_RejectsPrivateURL_NoDurableWrite(t *testing.T) {
	durable, db := newTestDurable(t)
	client, calls := noNetworkClient()
	p := newTestProxyWithDurable(client, newCache(defaultCacheCap, defaultCacheTTL), nil, durable)

	err := p.FetchAndPersist(context.Background(), "https://10.1.10.3/x.jpg")
	if !errors.Is(err, ErrHostNotAllowed) {
		t.Fatalf("FetchAndPersist of a private address = %v, want ErrHostNotAllowed", err)
	}
	if n := durableRowCount(t, db); n != 0 {
		t.Fatalf("durable index has %d rows after a rejected URL, want 0", n)
	}
	if *calls != 0 {
		t.Fatalf("rejected URL made %d HTTP calls, want 0 (SSRF guard runs before any I/O)", *calls)
	}
}

// TestFetchAndPersist_UsesMethodInternalValidate proves FetchAndPersist
// validates through the METHOD-INTERNAL validate (honoring p.allowPrivateHosts),
// not the package-level Validate (which hardcodes nil and would reject
// 127.0.0.1). An httptest CDN listens on 127.0.0.1; it warms successfully only
// because the escape hatch applies — exactly the integration-test path §2.3
// requires. Also confirms the bytes land in durable and are readable back.
func TestFetchAndPersist_UsesMethodInternalValidate(t *testing.T) {
	const wantBody = "\x89PNGWARMEDBYTES"
	var hits int
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set("Content-Type", "image/png")
		w.Write([]byte(wantBody))
	}))
	defer srv.Close()

	// The package-level Validate would reject this 127.0.0.1 URL outright.
	if _, err := Validate(context.Background(), srv.URL+"/warm.png"); !errors.Is(err, ErrHostNotAllowed) {
		t.Fatalf("precondition: package-level Validate(127.0.0.1) = %v, want ErrHostNotAllowed", err)
	}

	durable, db := newTestDurable(t)
	allow := allowPrivateHostFor(t, srv.URL)
	p := newTestProxyWithDurable(srv.Client(), newCache(defaultCacheCap, defaultCacheTTL), allow, durable)
	raw := srv.URL + "/warm.png"

	if err := p.FetchAndPersist(context.Background(), raw); err != nil {
		t.Fatalf("FetchAndPersist = %v, want success (method-internal validate must accept the exempted 127.0.0.1 host)", err)
	}
	if hits != 1 {
		t.Fatalf("upstream hit %d times, want exactly 1", hits)
	}
	if n := durableRowCount(t, db); n != 1 {
		t.Fatalf("durable index has %d rows, want 1 (the warmed image)", n)
	}
	body, ct, found, err := durable.Get(context.Background(), keyOf(t, raw, allow))
	if err != nil || !found {
		t.Fatalf("durable.Get after warm = (found=%v, err=%v), want (true, nil)", found, err)
	}
	if string(body) != wantBody || ct != "image/png" {
		t.Fatalf("durable stored (%q, %q), want (%q, image/png)", body, ct, wantBody)
	}
}

// TestFetchAndPersist_HasHitSkipsWithZeroCDN proves the incremental-skip path:
// when the key is already present (durable.Has hit), FetchAndPersist returns nil
// with ZERO CDN fetches. Proven with a no-network client — a fetch would error.
func TestFetchAndPersist_HasHitSkipsWithZeroCDN(t *testing.T) {
	durable, db := newTestDurable(t)
	const raw = "https://93.184.216.34/already.png"
	key := keyOf(t, raw, nil)
	if err := durable.Put(context.Background(), key, "image/png", []byte("existing")); err != nil {
		t.Fatalf("seeding durable: %v", err)
	}

	client, calls := noNetworkClient()
	p := newTestProxyWithDurable(client, newCache(defaultCacheCap, defaultCacheTTL), nil, durable)

	if err := p.FetchAndPersist(context.Background(), raw); err != nil {
		t.Fatalf("FetchAndPersist on a Has-hit = %v, want nil (incremental skip)", err)
	}
	if *calls != 0 {
		t.Fatalf("Has-hit made %d CDN calls, want 0", *calls)
	}
	if n := durableRowCount(t, db); n != 1 {
		t.Fatalf("durable index has %d rows, want 1 (unchanged by the skip)", n)
	}
}

// TestFetchAndPersist_NilDurableIsNoOp proves durable warming is opt-in: with
// durable == nil FetchAndPersist is a no-op that returns nil and makes no
// outbound call — never breaking a caller that didn't wire a durable store,
// even for a URL that would otherwise be fetched.
func TestFetchAndPersist_NilDurableIsNoOp(t *testing.T) {
	client, calls := noNetworkClient()
	p := newTestProxyWithDurable(client, newCache(defaultCacheCap, defaultCacheTTL), nil, nil)
	if err := p.FetchAndPersist(context.Background(), "https://93.184.216.34/x.png"); err != nil {
		t.Fatalf("FetchAndPersist with nil durable = %v, want nil (no-op)", err)
	}
	if *calls != 0 {
		t.Fatalf("nil-durable FetchAndPersist made %d HTTP calls, want 0", *calls)
	}
}

// TestNewWithDurable_NilDurable_ByteIdenticalToNew is the nil-durable regression
// guard (US-3 AC): a Proxy built with a nil durable store must behave exactly
// like today's New(client) — LRU-only, durable never consulted. Asserts the
// field is nil and that Fetch's LRU path (miss→fetch→hit) is unchanged.
func TestNewWithDurable_NilDurable_ByteIdenticalToNew(t *testing.T) {
	// Structural: NewWithDurable(client, nil) leaves durable nil and is wired
	// like New.
	if p := NewWithDurable(&http.Client{}, nil); p.durable != nil {
		t.Fatal("NewWithDurable(_, nil).durable != nil, want nil")
	}
	if NewWithDurable(&http.Client{}, nil).cache == nil {
		t.Fatal("NewWithDurable(_, nil).cache == nil, want a live LRU (as New builds)")
	}

	// Behavioral: with durable nil, the request path is LRU-only exactly as
	// before this change.
	var hits int
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set("Content-Type", "image/png")
		w.Write([]byte("bytes"))
	}))
	defer srv.Close()

	p := newTestProxyWithDurable(srv.Client(), newCache(defaultCacheCap, defaultCacheTTL), allowPrivateHostFor(t, srv.URL), nil)
	if p.durable != nil {
		t.Fatal("test proxy durable != nil, want nil for this regression")
	}
	u := srv.URL + "/reg.png"

	first, err := p.Fetch(context.Background(), u)
	if err != nil {
		t.Fatalf("first Fetch: %v", err)
	}
	if first.FromCache || hits != 1 {
		t.Fatalf("first Fetch: FromCache=%v hits=%d, want false/1", first.FromCache, hits)
	}
	second, err := p.Fetch(context.Background(), u)
	if err != nil {
		t.Fatalf("second Fetch: %v", err)
	}
	if !second.FromCache || hits != 1 {
		t.Fatalf("second Fetch: FromCache=%v hits=%d, want true/1 (LRU hit, no new upstream)", second.FromCache, hits)
	}
}

// TestMaintainDurable is the US-5 accessor test: MaintainDurable folds the
// every-cycle Reconcile (drift cleanup) with the disk-cap PruneToCap and the
// walk-based over-cap decision the precompute cycle consumes to pause image
// warming. It also confirms the nil-durable no-op so the metadata-only precompute
// path is never affected.
func TestMaintainDurable(t *testing.T) {
	ctx := context.Background()

	// Nil-durable proxy: MaintainDurable is a zero-value no-op (never touches disk
	// or DB, never reports OverCap) — the metadata-only precompute path relies on this.
	if res, err := New(nil).MaintainDurable(ctx, 100); err != nil || res.OverCap || res.TotalBytes != 0 {
		t.Fatalf("nil-durable MaintainDurable must be a zero-value no-op, got %+v err=%v", res, err)
	}

	store, db := newTestDurable(t)

	// Seed three indexed 100-byte entries; the 2 ms spacing gives each a distinct
	// RFC3339Nano fetched_at so PruneToCap's oldest-first eviction is deterministic.
	body := make([]byte, 100)
	for _, u := range []string{"https://cdn.example/a.jpg", "https://cdn.example/b.jpg", "https://cdn.example/c.jpg"} {
		if err := store.Put(ctx, u, "image/jpeg", body); err != nil {
			t.Fatalf("seeding %s: %v", u, err)
		}
		time.Sleep(2 * time.Millisecond)
	}

	// Seed write-side drift the every-cycle Reconcile must fold in and clean:
	//   (a) a stray *.tmp-* file (interrupted-write orphan),
	//   (b) an orphan data file (64-hex name, no index row).
	strayShard, _ := store.pathFor(strings.Repeat("a", 64))
	if err := os.MkdirAll(strayShard, 0o755); err != nil {
		t.Fatalf("mkdir stray shard: %v", err)
	}
	if err := os.WriteFile(filepath.Join(strayShard, strings.Repeat("a", 64)+tempSuffix+"x"), []byte("partial"), 0o644); err != nil {
		t.Fatalf("seeding stray temp: %v", err)
	}
	orphanShard, orphanFile := store.pathFor(strings.Repeat("b", 64))
	if err := os.MkdirAll(orphanShard, 0o755); err != nil {
		t.Fatalf("mkdir orphan shard: %v", err)
	}
	if err := os.WriteFile(orphanFile, make([]byte, 50), 0o644); err != nil {
		t.Fatalf("seeding orphan file: %v", err)
	}

	// cap = 100: Reconcile clears the stray temp + orphan file (300 indexed bytes
	// remain), then PruneToCap evicts the two oldest indexed entries down to a
	// single 100-byte entry — exactly at the cap, so OverCap is true (>=).
	res, err := NewWithDurable(nil, store).MaintainDurable(ctx, 100)
	if err != nil {
		t.Fatalf("MaintainDurable: %v", err)
	}
	if res.StrayTemps != 1 || res.OrphanFiles != 1 || res.OrphanRows != 0 {
		t.Errorf("reconcile counts = {stray:%d rows:%d files:%d}, want {1,0,1}", res.StrayTemps, res.OrphanRows, res.OrphanFiles)
	}
	if res.TotalBytes != 100 {
		t.Errorf("post-prune TotalBytes = %d, want 100 (pruned back to a single entry at the cap)", res.TotalBytes)
	}
	if !res.OverCap {
		t.Errorf("OverCap = false, want true (usage sits exactly at the 100-byte cap after prune)")
	}
	if n := durableRowCount(t, db); n != 1 {
		t.Errorf("index rows after MaintainDurable = %d, want 1", n)
	}

	// capBytes <= 0 means "no cap": Reconcile still runs but PruneToCap is skipped
	// and OverCap is never asserted, so warming is never spuriously paused.
	if res, err := NewWithDurable(nil, store).MaintainDurable(ctx, 0); err != nil || res.OverCap {
		t.Fatalf("cap<=0 must skip pruning and never report OverCap, got %+v err=%v", res, err)
	}
}
