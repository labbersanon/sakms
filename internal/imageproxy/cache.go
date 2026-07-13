package imageproxy

import (
	"container/list"
	"sync"
	"time"
)

// defaultCacheCap / defaultCacheTTL size the production image cache. Posters
// and thumbnails are small (tens of KB each), so a few hundred entries is a
// modest memory footprint while covering a full Discover grid many times over;
// the TTL is long enough that repeated grid renders don't re-hit TMDB/TPDB but
// short enough that art updates are picked up within a session. Deliberately
// modest — the plan warns against over-engineering a persistent cache ahead of
// proven need; this is intentionally just an in-memory LRU.
const (
	defaultCacheCap = 256
	defaultCacheTTL = time.Hour
)

// cache is a fixed-capacity LRU with per-entry TTL, safe for concurrent use.
// It is deliberately minimal: no background sweeper (expired entries are
// dropped lazily on lookup, and eventually evicted by LRU pressure anyway),
// no singleflight (two concurrent misses for the same key both fetch — a
// negligible, self-correcting waste for Stage 1's read-only grid).
type cache struct {
	mu  sync.Mutex
	ttl time.Duration
	cap int
	ll  *list.List               // front = most recently used
	m   map[string]*list.Element // key -> element holding *cacheEntry
}

type cacheEntry struct {
	key     string
	img     *Image
	expires time.Time
}

// nowFunc is indirected so tests can drive TTL expiry deterministically.
var nowFunc = time.Now

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

// get returns the cached image for key if present and unexpired, promoting it
// to most-recently-used. An expired entry is removed and reported as a miss.
func (c *cache) get(key string) (*Image, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	el, ok := c.m[key]
	if !ok {
		return nil, false
	}
	ent := el.Value.(*cacheEntry)
	if nowFunc().After(ent.expires) {
		c.removeElement(el)
		return nil, false
	}
	c.ll.MoveToFront(el)
	return ent.img, true
}

// put stores img under key with a fresh TTL, promoting/replacing any existing
// entry and evicting the least-recently-used entry if over capacity.
func (c *cache) put(key string, img *Image) {
	c.mu.Lock()
	defer c.mu.Unlock()

	exp := nowFunc().Add(c.ttl)
	if el, ok := c.m[key]; ok {
		ent := el.Value.(*cacheEntry)
		ent.img = img
		ent.expires = exp
		c.ll.MoveToFront(el)
		return
	}
	el := c.ll.PushFront(&cacheEntry{key: key, img: img, expires: exp})
	c.m[key] = el
	if c.ll.Len() > c.cap {
		if back := c.ll.Back(); back != nil {
			c.removeElement(back)
		}
	}
}

// removeElement drops el from both the list and the map. Caller holds c.mu.
func (c *cache) removeElement(el *list.Element) {
	c.ll.Remove(el)
	delete(c.m, el.Value.(*cacheEntry).key)
}
