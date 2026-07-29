package adultmergecache

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"time"

	"github.com/labbersanon/sakms/internal/adultmerge"
	"github.com/labbersanon/sakms/internal/connections"
	"github.com/labbersanon/sakms/internal/stashbox"
	"github.com/labbersanon/sakms/internal/tpdbrest"
)

// precomputePerformers precomputes and caches the merged Performers row's first
// PrecomputePages pages. It builds its own tolerant TPDB (required) and StashDB
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
func precomputePerformers(ctx context.Context, httpClient *http.Client, connStore *connections.Store, store *Store) error {
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

	for page := 1; page <= PrecomputePages; page++ {
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
func precomputeStudios(ctx context.Context, httpClient *http.Client, connStore *connections.Store, store *Store) error {
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

	for page := 1; page <= PrecomputePages; page++ {
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
