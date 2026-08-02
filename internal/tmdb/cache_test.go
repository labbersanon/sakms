package tmdb

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// NOTE: no test in this package may call t.Parallel() — cacheNow and
// defaultCache are package-level and shared. See cacheNow's doc in cache.go.

// countingClient serves body for every request, counting how many actually
// reach the server. Each client gets its own fresh cache, exactly as the other
// helpers in this package do, so these tests measure only their own traffic.
func countingClient(t *testing.T, body string) (*Client, *int) {
	t.Helper()
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	c := New(Config{BaseURL: srv.URL, APIKey: "test-key"}, srv.Client())
	c.cache = newCache(defaultCacheCap, defaultCacheTTL)
	return c, &calls
}

const cachedTVFixture = `{"id": 7, "name": "Cached Show", "seasons": [{"season_number": 1, "episode_count": 8}]}`

// TestCache_HitAvoidsUpstream is the AC3 proof: a second identical call inside
// the TTL is served from cache and never reaches TMDB.
func TestCache_HitAvoidsUpstream(t *testing.T) {
	c, calls := countingClient(t, cachedTVFixture)

	first, err := c.TVDetails(context.Background(), 7)
	if err != nil {
		t.Fatalf("first call: %v", err)
	}
	second, err := c.TVDetails(context.Background(), 7)
	if err != nil {
		t.Fatalf("second call: %v", err)
	}

	if *calls != 1 {
		t.Errorf("expected exactly 1 upstream request, got %d", *calls)
	}
	if first.Title != "Cached Show" || second.Title != first.Title {
		t.Errorf("cached result differs from the live one: %+v vs %+v", second, first)
	}
	if len(second.Seasons) != 1 || second.Seasons[0].EpisodeCount != 8 {
		t.Errorf("cached result decoded incompletely: %+v", second.Seasons)
	}
}

// TestCache_TTLExpiry drives cacheNow forward past the TTL and confirms the
// entry stops being served.
func TestCache_TTLExpiry(t *testing.T) {
	base := time.Now()
	now := base
	cacheNow = func() time.Time { return now }
	t.Cleanup(func() { cacheNow = time.Now })

	c, calls := countingClient(t, cachedTVFixture)

	if _, err := c.TVDetails(context.Background(), 7); err != nil {
		t.Fatalf("first call: %v", err)
	}
	now = base.Add(defaultCacheTTL - time.Second)
	if _, err := c.TVDetails(context.Background(), 7); err != nil {
		t.Fatalf("within-TTL call: %v", err)
	}
	if *calls != 1 {
		t.Fatalf("a call within the TTL should be cached; upstream saw %d requests", *calls)
	}

	now = base.Add(defaultCacheTTL + time.Second)
	if _, err := c.TVDetails(context.Background(), 7); err != nil {
		t.Fatalf("post-TTL call: %v", err)
	}
	if *calls != 2 {
		t.Errorf("a call after the TTL should re-fetch; upstream saw %d requests", *calls)
	}
}

// TestCache_LRUEviction confirms a capacity-2 cache drops its least recently
// used entry once a third key arrives.
func TestCache_LRUEviction(t *testing.T) {
	cc := newCache(2, time.Hour)
	cc.put("a", []byte("1"))
	cc.put("b", []byte("2"))
	cc.put("c", []byte("3"))

	if _, ok := cc.get("a"); ok {
		t.Error("the least recently used entry should have been evicted")
	}
	for _, k := range []string{"b", "c"} {
		if _, ok := cc.get(k); !ok {
			t.Errorf("entry %q should still be cached", k)
		}
	}
}

// TestCache_SkipsOversizedEntry covers the per-entry byte cap: an oversized
// body is simply not stored, so it re-fetches rather than pinning up to 10 MB
// (httpx.MaxResponseBodySize) per entry.
func TestCache_SkipsOversizedEntry(t *testing.T) {
	cc := newCache(4, time.Hour)
	cc.put("big", make([]byte, maxCacheEntryBytes+1))
	if _, ok := cc.get("big"); ok {
		t.Error("a body larger than maxCacheEntryBytes must not be cached")
	}
	cc.put("ok", make([]byte, maxCacheEntryBytes))
	if _, ok := cc.get("ok"); !ok {
		t.Error("a body exactly at maxCacheEntryBytes should still be cached")
	}
}

// TestCache_DoesNotCacheErrors confirms a failed fetch is never pinned: a
// transient TMDB 500 must not blank a Discover row for the whole TTL.
func TestCache_DoesNotCacheErrors(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(cachedTVFixture))
	}))
	t.Cleanup(srv.Close)
	c := New(Config{BaseURL: srv.URL, APIKey: "test-key"}, srv.Client())
	c.cache = newCache(defaultCacheCap, defaultCacheTTL)

	if _, err := c.TVDetails(context.Background(), 7); err == nil {
		t.Fatal("expected the 500 to surface as an error")
	}
	details, err := c.TVDetails(context.Background(), 7)
	if err != nil {
		t.Fatalf("the retry after a 500 must reach upstream, not a cached failure: %v", err)
	}
	if details.Title != "Cached Show" {
		t.Errorf("unexpected details after retry: %+v", details)
	}
	if calls != 2 {
		t.Errorf("expected 2 upstream requests (the 500 then the retry), got %d", calls)
	}
}

// TestCache_KeyExcludesRawAPIKey guards the security property in cacheKey's
// doc: the operator's TMDB key never lands in a process-lifetime map key, and
// two operators' keys never share an entry.
func TestCache_KeyExcludesRawAPIKey(t *testing.T) {
	const secret = "s3cr3t-tmdb-key"
	a := New(Config{BaseURL: "https://example.test/3", APIKey: secret}, http.DefaultClient)
	b := New(Config{BaseURL: "https://example.test/3", APIKey: "a-different-key"}, http.DefaultClient)

	keyA := a.cacheKey("/tv/7", url.Values{})
	if strings.Contains(keyA, secret) {
		t.Fatalf("the cache key embeds the raw API key: %s", keyA)
	}
	if keyA == b.cacheKey("/tv/7", url.Values{}) {
		t.Error("clients with different API keys must not share cache entries")
	}

	// And prove it end-to-end: the same fake server, same path, two keys.
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(cachedTVFixture))
	}))
	t.Cleanup(srv.Close)
	shared := newCache(defaultCacheCap, defaultCacheTTL)
	c1 := New(Config{BaseURL: srv.URL, APIKey: secret}, srv.Client())
	c1.cache = shared
	c2 := New(Config{BaseURL: srv.URL, APIKey: "a-different-key"}, srv.Client())
	c2.cache = shared

	if _, err := c1.TVDetails(context.Background(), 7); err != nil {
		t.Fatalf("client 1: %v", err)
	}
	if _, err := c2.TVDetails(context.Background(), 7); err != nil {
		t.Fatalf("client 2: %v", err)
	}
	if calls != 2 {
		t.Errorf("a second API key must re-fetch rather than reuse the first key's entry; upstream saw %d requests", calls)
	}
}

// TestCacheKey_QueryOrderIndependent confirms two callers building the same
// params in a different order produce one key, not two.
func TestCacheKey_QueryOrderIndependent(t *testing.T) {
	c := New(Config{BaseURL: "https://example.test/3", APIKey: "k"}, http.DefaultClient)

	first := url.Values{}
	first.Set("page", "2")
	first.Set("with_genres", "18")

	second := url.Values{}
	second.Set("with_genres", "18")
	second.Set("page", "2")

	if c.cacheKey("/discover/tv", first) != c.cacheKey("/discover/tv", second) {
		t.Errorf("query order changed the cache key:\n%s\n%s",
			c.cacheKey("/discover/tv", first), c.cacheKey("/discover/tv", second))
	}
}

// TestResetDefaultCache confirms the internal/api-facing test hook empties the
// package-level cache IN PLACE — a client constructed before the reset must
// see it, which a reassignment of defaultCache would not deliver.
func TestResetDefaultCache(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(cachedTVFixture))
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(ResetDefaultCache)

	// Deliberately on the package default, which is what internal/api reaches.
	c := New(Config{BaseURL: srv.URL, APIKey: "reset-hook-key"}, srv.Client())

	if _, err := c.TVDetails(context.Background(), 7); err != nil {
		t.Fatalf("first call: %v", err)
	}
	if _, err := c.TVDetails(context.Background(), 7); err != nil {
		t.Fatalf("second call: %v", err)
	}
	if calls != 1 {
		t.Fatalf("expected the second call to be cached, upstream saw %d requests", calls)
	}

	ResetDefaultCache()

	if _, err := c.TVDetails(context.Background(), 7); err != nil {
		t.Fatalf("post-reset call: %v", err)
	}
	if calls != 2 {
		t.Errorf("the pre-existing client must observe the reset; upstream saw %d requests", calls)
	}
}
