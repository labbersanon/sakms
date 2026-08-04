package tmdb

import (
	"container/list"
	"log"
	"os"
	"sync"
	"time"
)

// defaultCacheCap / defaultCacheTTL / maxCacheEntryBytes size the production
// TMDB response cache.
//
// TTL is 10 minutes, deliberately much shorter than internal/imageproxy's
// hour: this cache sits behind do(), so it holds volatile list endpoints
// (/trending, /discover) alongside near-immutable ones (/tv/{id}/season/{n}).
// A per-endpoint TTL table would be the "right" answer and is rejected as a
// policy surface with no proven need (this repo's no-premature-abstraction
// convention). 10 minutes is the shortest TTL that still makes the dominant
// access pattern free — reopening or comparing titles within one browsing
// session — while keeping Discover rows visibly fresh. If it proves wrong the
// symptom is "Discover rows don't refresh" and the knob is these constants.
//
// maxCacheEntryBytes bounds a single entry. Without it the only bound on an
// entry is httpx.MaxResponseBodySize (10 MB), and do() is the chokepoint for
// ~35 methods — including TVAggregateFullCredits, whose /tv/{id}/
// aggregate_credits body for a long-running show is hundreds of KB. put skips
// anything larger, so an oversized body simply re-fetches uncached rather than
// being pinned. Worst case is therefore 512 x 256 KB = 128 MB, typically far
// less; that is the honest bound, not a smaller guess.
const (
	defaultCacheCap    = 512
	defaultCacheTTL    = 10 * time.Minute
	maxCacheEntryBytes = 256 * 1024
)

// defaultCache is the process-lifetime response cache every Client built by
// New shares. It MUST be package-level: mode.Build constructs a fresh Client
// per HTTP request (internal/mode/mode.go:434, 33 call sites), so a per-Client
// cache would never serve a single hit. Same lifetime shape as imageproxy's
// singleton cache (constructed once in main.go); a package var rather than a
// main.go construction because that avoids threading a parameter through
// mode.Build's 33 call sites for no behavioral gain.
//
// Complete consumer list, so nobody has to go looking: every Client from
// mode.Build (all of internal/api), plus internal/recheck/recheck.go:222,
// which builds its own Client outside mode.Build and inherits this cache
// harmlessly — it is a background availability re-check whose repeated TMDB
// reads are exactly the kind that benefit.
//
// Claude 2026-08-03: added the second non-mode.Build consumer.
// Reason: internal/discoverrefresh (plan discover-scheduled-refresh §3.2, §7.1)
// also builds its own Client outside mode.Build, but — UNLIKE recheck — with
// Config.BypassCache: true, so it deliberately does NOT inherit this cache.
// See Config.BypassCache's doc comment (client.go:78-97) for why: a manual
// "refresh now" fired within this cache's 10-minute TTL of any Discover read
// would otherwise return byte-identical cached bytes and report success
// without actually re-fetching.
// Troubleshooting: if this list gains a third consumer, update it here too —
// it exists precisely so nobody has to grep for tmdb.New call sites.
var defaultCache = newCache(defaultCacheCap, defaultCacheTTL)

// ResetDefaultCache empties the package-level cache. TEST-ONLY.
//
// It exists because internal/api tests reach TMDB through mode.Build (after
// swapping DefaultBaseURL at the package level), so they cannot reach
// Client.cache the way internal/tmdb's own test helpers do. Two sibling tests
// pointed at one fake server, differing only in a query param that is part of
// the cache key or not, would otherwise have the second one served entirely
// from the first one's entries.
//
// It clears in place rather than reassigning defaultCache: every Client copied
// the pointer at New() time, so a reassignment would leave already-constructed
// clients on the old cache, and writing a package var while another goroutine
// is inside New() is a data race that -race will find.
func ResetDefaultCache() { defaultCache.reset() }

// cache is a fixed-capacity LRU with per-entry TTL, safe for concurrent use,
// holding raw response bodies keyed by request. Ported from
// internal/imageproxy/cache.go, storing []byte instead of *Image — that is
// what makes it general-purpose rather than season/episode-specific, since
// do() is a single chokepoint every *Client method routes through.
//
// Deliberately minimal, same as the original: no background sweeper (expired
// entries are dropped lazily on lookup and evicted by LRU pressure anyway),
// no singleflight (two concurrent misses for the same key both fetch — a
// negligible, self-correcting waste).
type cache struct {
	mu  sync.Mutex
	ttl time.Duration
	cap int
	ll  *list.List               // front = most recently used
	m   map[string]*list.Element // key -> element holding *cacheEntry
}

type cacheEntry struct {
	key     string
	body    []byte
	expires time.Time
}

// cacheNow returns the current time — indirected so tests can drive TTL expiry
// deterministically, the same package-var-for-testability pattern
// DefaultBaseURL and nowFn already use in this package. Named cacheNow rather
// than reusing or shadowing nowFn (client.go), which serves an unrelated
// purpose (discoverFilterQuery's date bound); two similar names in one package
// is a maintenance trap.
//
// It is package-level and shared, and so is defaultCache: tests in this
// package must NOT call t.Parallel(). A parallel test would observe another
// test's cacheNow override (or its cache entries) mid-run.
var cacheNow = time.Now

func newCache(capacity int, ttl time.Duration) *cache {
	if capacity < 1 {
		capacity = 1
	}
	return &cache{
		ttl: ttl,
		cap: capacity,
		ll:  list.New(),
		m:   make(map[string]*list.Element, capacity),
	}
}

// get returns the cached body for key if present and unexpired, promoting it
// to most-recently-used. An expired entry is removed and reported as a miss.
func (c *cache) get(key string) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	el, ok := c.m[key]
	if !ok {
		return nil, false
	}
	ent := el.Value.(*cacheEntry)
	if cacheNow().After(ent.expires) {
		c.removeElement(el)
		return nil, false
	}
	c.ll.MoveToFront(el)
	return ent.body, true
}

// put stores body under key with a fresh TTL, promoting/replacing any existing
// entry and evicting the least-recently-used entry if over capacity. A body
// larger than maxCacheEntryBytes is not stored at all — see that constant.
func (c *cache) put(key string, body []byte) {
	if len(body) > maxCacheEntryBytes {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	exp := cacheNow().Add(c.ttl)
	if el, ok := c.m[key]; ok {
		ent := el.Value.(*cacheEntry)
		ent.body = body
		ent.expires = exp
		c.ll.MoveToFront(el)
		return
	}
	el := c.ll.PushFront(&cacheEntry{key: key, body: body, expires: exp})
	c.m[key] = el
	if c.ll.Len() > c.cap {
		if back := c.ll.Back(); back != nil {
			c.removeElement(back)
		}
	}
}

// reset drops every entry, leaving the cache usable. See ResetDefaultCache.
func (c *cache) reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ll.Init()
	c.m = make(map[string]*list.Element, c.cap)
}

// removeElement drops el from both the list and the map. Caller holds c.mu.
func (c *cache) removeElement(el *list.Element) {
	c.ll.Remove(el)
	delete(c.m, el.Value.(*cacheEntry).key)
}

// cacheDebug gates logCacheMiss, off unless SAKMS_TMDB_CACHE_DEBUG is set to a
// non-empty value.
//
// Claude 2026-08-02: added — logCacheMiss previously logged unconditionally.
// Reason: a miss is not a rare event. A cold Discover load fans out across
// every row, and one cold Series DetailPopup open fires the extended-details
// call plus one per season — dozens of lines per operator click, drowning the
// soft-failure lines that actually need reading. The counter the plan offered
// as the alternative would need a read-out surface (a route, or a periodic
// dump) that nothing asks for yet, so this takes the debug-flag branch.
// Read once at init, matching internal/config's plain os.Getenv convention
// (empty string IS the sentinel, so no cmp.Or default) — there is no existing
// debug-log flag in this codebase to reuse, which was checked before adding
// one. Deliberately not toggleable at runtime: flipping it needs a restart,
// which is the same cost as every other env knob here.
// Troubleshooting: Phase-4 review FIX 4 (architect F3, plan deviation).
// Review if: this codebase grows a real log-level/verbosity mechanism — fold
// this into it rather than keeping a private flag.
var cacheDebug = os.Getenv("SAKMS_TMDB_CACHE_DEBUG") != ""

// logCacheMiss records a cache miss for path, so this cache's effectiveness is
// observable from the logs rather than only from an operator reporting that
// Discover rows are not refreshing. Misses only: a hit-and-miss line per TMDB
// call would be pure noise across ~35 methods, and a miss is the interesting
// event (it is the one that costs a round trip). Gated by cacheDebug — see
// above for why this one soft-path line is not logged unconditionally the way
// the rest of this program's are.
//
// It logs the PATH, never the cache key: the key carries a fingerprint of the
// operator's TMDB API key, and this program deliberately keeps credential-
// bearing URLs out of log lines (see httpx.WrapTransportError).
func logCacheMiss(path string) {
	if !cacheDebug {
		return
	}
	log.Printf("tmdb cache: miss for %s", path)
}
