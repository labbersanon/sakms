package adultmergecache

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/labbersanon/sakms/internal/adultmerge"
	"github.com/labbersanon/sakms/internal/connections"
	"github.com/labbersanon/sakms/internal/imageproxy"
	"github.com/labbersanon/sakms/internal/stashbox"
	"github.com/labbersanon/sakms/internal/tpdbrest"
)

// imageWarmConcurrency bounds concurrent CDN image-byte fetches during a
// precompute cycle, mirroring tpdbrest.maxImageBackfillConcurrency's ≤5 bound.
// These fetches hit the image CDN host (cdn.theporndb.net / StashDB / studio
// CDNs), NOT api.theporndb.net, so they do NOT pass through tpdbrest's shared
// REST rate limiter — this semaphore is the only concurrency bound on them.
const imageWarmConcurrency = 5

// imageWarmFailureThreshold is the per-cycle circuit-breaker limit: after this
// many CONSECUTIVE FetchAndPersist failures within one entity type's precompute
// call (counted across all its pages, not per-page), image warming aborts for
// the remainder of that cycle and logs once. Metadata warming is unaffected. A
// single success resets the consecutive-failure counter to 0. This stops a
// CDN-wide outage or a disk-full ENOSPC from spinning the cycle uselessly.
const imageWarmFailureThreshold = 20

// imageWarmer carries the image-warming state shared across a single entity
// type's precompute call: the ≤5 bounded-concurrency semaphore and the
// consecutive-failure circuit breaker. It is built once per precompute function
// (so the breaker is scoped per entity type per cycle) and its warmPage runs
// once per page, strictly AFTER that page's metadata store.Put — and blocks
// until that page's fan-out drains, so page N's image fetches never overlap
// page N+1's (nor stack on adultmerge's own per-page ≤5 scene fan-out). A nil
// *imageproxy.Proxy yields a nil *imageWarmer, so warming is skipped entirely
// (nothing built, no goroutines) — the nil-tolerant metadata-only path.
type imageWarmer struct {
	proxy   *imageproxy.Proxy
	sem     chan struct{}
	mu      sync.Mutex
	fails   int  // consecutive FetchAndPersist failures (reset on any success)
	aborted bool // circuit breaker tripped for the rest of this cycle
}

// newImageWarmer returns nil when proxy is nil, so callers cheaply skip warming
// without building a semaphore or spawning goroutines (metadata-only behavior).
func newImageWarmer(proxy *imageproxy.Proxy) *imageWarmer {
	if proxy == nil {
		return nil
	}
	return &imageWarmer{proxy: proxy, sem: make(chan struct{}, imageWarmConcurrency)}
}

// warmPage warms every non-empty URL for one page, bounded to ≤5 concurrent
// fetches, and returns only once the whole page's fan-out has drained. A
// FetchAndPersist error for one URL is logged and skipped — the metadata is
// already stored, so that card falls back to the request-path live fetch; one
// failure never fails the page, the entity type, or the cycle. Once the
// consecutive-failure circuit breaker has tripped this cycle, warmPage returns
// immediately (metadata warming continues in the caller).
func (w *imageWarmer) warmPage(ctx context.Context, urls []string) {
	w.mu.Lock()
	aborted := w.aborted
	w.mu.Unlock()
	if aborted {
		return
	}

	var wg sync.WaitGroup
	for _, url := range urls {
		if url == "" {
			continue
		}
		w.mu.Lock()
		aborted = w.aborted
		w.mu.Unlock()
		if aborted {
			break // stop spawning; in-flight fetches still drain below
		}
		wg.Add(1)
		go func(url string) {
			defer wg.Done()
			w.sem <- struct{}{}
			defer func() { <-w.sem }()
			err := w.proxy.FetchAndPersist(ctx, url)
			w.mu.Lock()
			defer w.mu.Unlock()
			if err != nil {
				log.Printf("adultmergecache: image warm %s: %v", url, err)
				w.fails++
				if w.fails >= imageWarmFailureThreshold && !w.aborted {
					w.aborted = true
					log.Printf("adultmergecache: image warming aborted for the rest of this cycle after %d consecutive failures", w.fails)
				}
				return
			}
			w.fails = 0
		}(url)
	}
	wg.Wait()
}

// precomputePerformers precomputes and caches the merged Performers row's first
// PrecomputePerformerPages pages. It builds its own tolerant TPDB (required) and StashDB
// (optional) clients straight from connStore — the same construction the live
// handler uses, replicated here because this package can't import package api.
// If TPDB isn't configured there is nothing to precompute (returns nil, leaving
// the cache empty; the read handler then falls through to live, which 400s as
// today when TPDB is absent).
//
// Pages are precomputed SEQUENTIALLY (one page's full pipeline completes before
// the next starts) — never concurrently. That is deliberate and, for Studios,
// load-bearing (see precomputeStudios). A per-page fetch/marshal/store error is
// logged and skipped so the other pages still get cached; the cycle never aborts
// wholesale.
//
// proxy is nil-tolerant: a nil proxy reproduces exactly today's metadata-only
// behavior (no image warming, no goroutines). When non-nil, after each page's
// metadata store.Put succeeds, that page's non-empty card image URLs are warmed
// into the durable store with ≤5 bounded concurrency (see imageWarmer) — per
// page, sequentially, so image fan-out never overlaps the next page's.
func precomputePerformers(ctx context.Context, httpClient *http.Client, connStore *connections.Store, store *Store, proxy *imageproxy.Proxy) error {
	tpdb, err := buildTPDB(ctx, connStore, httpClient)
	if err != nil {
		return err
	}
	if tpdb == nil {
		return nil // tpdb not configured — nothing to precompute
	}
	stash, hasStash, err := buildStashBox(ctx, connStore, httpClient, "stashdb")
	if err != nil {
		return err
	}
	warmer := newImageWarmer(proxy)

	for page := 1; page <= PrecomputePerformerPages; page++ {
		cards, hasMore, err := adultmerge.FetchAndMergePerformers(ctx, tpdb, stash, hasStash, page, cachePerPage)
		if err != nil {
			log.Printf("adultmergecache: performers precompute fetch page %d: %v", page, err)
			continue
		}
		payload, err := json.Marshal(cards)
		if err != nil {
			log.Printf("adultmergecache: performers precompute marshal page %d: %v", page, err)
			continue
		}
		if err := store.Put(ctx, "performers", page, cachePerPage, payload, hasMore, time.Now().UTC().Format(time.RFC3339)); err != nil {
			log.Printf("adultmergecache: performers precompute store page %d: %v", page, err)
			continue
		}
		if warmer != nil {
			urls := make([]string, 0, len(cards))
			for _, c := range cards {
				if c.Image != "" {
					urls = append(urls, c.Image)
				}
			}
			warmer.warmPage(ctx, urls)
		}
	}
	return nil
}

// precomputeStudios is precomputePerformers' studio sibling — same tolerant
// client construction, sequential-pages iteration, and per-page fault isolation,
// over the Studios pipeline (BrowseSites + QueryStudios + FilterZeroSceneSites +
// MergeStudios). Kept a SEPARATE sibling, never unified with the Performers one:
// the pipelines genuinely differ (see internal/adultmerge's package doc).
//
// The sequential-pages iteration is load-bearing here: FetchAndMergeStudios'
// FilterZeroSceneSites fans out ≤5 concurrent ScenesBySite calls PER PAGE, so
// running pages sequentially keeps the peak concurrent ScenesBySite fan-out
// across the whole K-page cycle at ≤5 (running K pages at once would peak at
// K×5). Keeping that ceiling at ≤5 is the invariant TPDB rate-limiting depends
// on — it's what bounds the worst-case wait a concurrently-issued interactive
// TPDB call can queue behind — so this ordering is satisfied by construction, not
// by luck.
//
// proxy is nil-tolerant, identical to precomputePerformers: nil = metadata-only,
// non-nil warms each page's card images (≤5 concurrent, per page, after that
// page's metadata store) via the same imageWarmer.
func precomputeStudios(ctx context.Context, httpClient *http.Client, connStore *connections.Store, store *Store, proxy *imageproxy.Proxy) error {
	tpdb, err := buildTPDB(ctx, connStore, httpClient)
	if err != nil {
		return err
	}
	if tpdb == nil {
		return nil // tpdb not configured — nothing to precompute
	}
	stash, hasStash, err := buildStashBox(ctx, connStore, httpClient, "stashdb")
	if err != nil {
		return err
	}
	warmer := newImageWarmer(proxy)

	for page := 1; page <= PrecomputeStudioPages; page++ {
		cards, hasMore, err := adultmerge.FetchAndMergeStudios(ctx, tpdb, stash, hasStash, page, cachePerPage)
		if err != nil {
			log.Printf("adultmergecache: studios precompute fetch page %d: %v", page, err)
			continue
		}
		payload, err := json.Marshal(cards)
		if err != nil {
			log.Printf("adultmergecache: studios precompute marshal page %d: %v", page, err)
			continue
		}
		if err := store.Put(ctx, "studios", page, cachePerPage, payload, hasMore, time.Now().UTC().Format(time.RFC3339)); err != nil {
			log.Printf("adultmergecache: studios precompute store page %d: %v", page, err)
			continue
		}
		if warmer != nil {
			urls := make([]string, 0, len(cards))
			for _, c := range cards {
				if c.Image != "" {
					urls = append(urls, c.Image)
				}
			}
			warmer.warmPage(ctx, urls)
		}
	}
	return nil
}

// buildTPDB constructs a TPDB REST client straight from connStore, or (nil, nil)
// if none is configured — the same tolerant, standalone construction the live
// handler's adultTPDBClient does (minus the HTTP-error writing), and the same
// (nil, nil)-on-ErrNotFound shape recheck.buildProwlarr uses. TPDB's REST base
// is the fixed public endpoint, never conn.URL. A real store error propagates.
func buildTPDB(ctx context.Context, connStore *connections.Store, httpClient *http.Client) (*tpdbrest.Client, error) {
	conn, err := connStore.Get(ctx, "tpdb")
	if errors.Is(err, connections.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return tpdbrest.New(tpdbrest.DefaultBaseURL, conn.APIKey, httpClient), nil
}

// buildStashBox constructs a stash-box client for the OPTIONAL StashDB source,
// or (nil, false, nil) if it isn't configured — mirroring the live handler's
// adultStashBoxClient (IsBearer:false, HasVoteField:true; endpoint is the fixed
// per-box constant, never conn.URL). A real store error propagates.
func buildStashBox(ctx context.Context, connStore *connections.Store, httpClient *http.Client, service string) (*stashbox.Client, bool, error) {
	conn, err := connStore.Get(ctx, service)
	if errors.Is(err, connections.ErrNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	endpoint, _ := stashbox.URLForBox(service)
	return stashbox.New(stashbox.Config{
		Endpoint: endpoint, APIKey: conn.APIKey, IsBearer: false, HasVoteField: true,
	}, httpClient), true, nil
}
