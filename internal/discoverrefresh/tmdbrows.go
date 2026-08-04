package discoverrefresh

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

	"golang.org/x/sync/errgroup"

	"github.com/labbersanon/sakms/internal/tmdb"
)

// tmdbTarget is one of the SIX fixed Mainstream rows this package caches — the
// exact set frontend/src/screens/discover/Mainstream.tsx renders on mount,
// {movies,series} × {trending,popular,upcoming}.
//
// internal/api's discoverHandler dispatches SEVEN categories; the other four
// (genre/studio/network/filter) are deliberately NOT cached. Their key space is
// unbounded (filter: arbitrary genreIds/year/minRating/sortBy) or a large
// cross-product (one key per TMDB id), and they are operator-driven browse
// actions rather than mount-time rows — the same carve-out shape as the
// stash-box {id}/scenes drill-downs. They keep working exactly as they do
// today, live, via the read path's cold-miss fall-through.
type tmdbTarget struct {
	// mode is the cache key's first half. It is the STRING VALUE of mode.Movies
	// / mode.Series, spelled out rather than imported: this package must not
	// import internal/mode, whose mode.Build constructs a cache-ENABLED
	// tmdb.Client (see the package doc). The read path composes the same key as
	// string(m)+":"+category, so these two spellings must stay in step.
	mode string
	// category is the value discoverHandler's ?category= takes.
	category string
	// mediaType is what mediaTypeForMode(mode) yields: Series is TMDB's TV
	// catalog, Movies the movie one.
	mediaType tmdb.MediaType
}

// tmdbTargets is the complete cached TMDB key space, in refresh order.
//
// Review if: MAINSTREAM_ROWS (Mainstream.tsx) gains or loses a fixed row — the
// cached set is defined as "exactly the rows that render on mount", so a new
// one belongs here and a removed one leaves an orphan row behind.
var tmdbTargets = []tmdbTarget{
	{mode: "movies", category: "trending", mediaType: tmdb.Movie},
	{mode: "movies", category: "popular", mediaType: tmdb.Movie},
	{mode: "movies", category: "upcoming", mediaType: tmdb.Movie},
	{mode: "series", category: "trending", mediaType: tmdb.TV},
	{mode: "series", category: "popular", mediaType: tmdb.TV},
	{mode: "series", category: "upcoming", mediaType: tmdb.TV},
}

// cacheKey is the '{mode}:{category}' identity migration 0058 documents for
// source='tmdb'.
func (t tmdbTarget) cacheKey() string { return t.mode + ":" + t.category }

// fetch calls the same tmdb.Client method discoverHandler's switch would for
// this target, including Trending's "week" time window. Keeping the two in step
// matters more than it looks: a divergence here would cache a row whose content
// silently differs from what the live fall-through path serves for the very
// next page.
func (t tmdbTarget) fetch(ctx context.Context, client *tmdb.Client, page int) ([]tmdb.Item, error) {
	switch t.category {
	case "trending":
		return client.Trending(ctx, t.mediaType, "week", page)
	case "popular":
		return client.Popular(ctx, t.mediaType, page)
	default:
		// "upcoming". UpcomingTV is TMDB's /tv/on_the_air — the closest TV
		// analog to Upcoming Movies' "future release date"; TMDB has no direct
		// equivalent. Same branch discoverHandler takes.
		if t.mediaType == tmdb.TV {
			return client.UpcomingTV(ctx, page)
		}
		return client.UpcomingMovies(ctx, page)
	}
}

// filtersUnreleased mirrors discoverHandler's own condition exactly
// (internal/api/discover.go): Movies-only, Trending/Popular-only. Series is
// excluded because TMDB's TV catalog has no release_dates concept, and Upcoming
// is exempt because showing not-yet-released titles is that row's entire
// purpose — only Trending/Popular claim to be "watch it now".
func (t tmdbTarget) filtersUnreleased() bool {
	return t.mediaType == tmdb.Movie && (t.category == "trending" || t.category == "popular")
}

// refreshTMDBCategories rebuilds all six fixed TMDB rows, STRICTLY SEQUENTIALLY
// — no goroutine fan-out across targets or pages. CLAUDE.md's "Discover never
// queries Prowlarr" rule is about Prowlarr specifically, but the reasoning it
// encodes (one trigger must never explode into a concurrent burst of external
// queries) applies here, and internal/adultnewest's runCycle ties its own
// sequential shape to exactly that. The one bounded exception is
// FilterByUSRelease's pre-existing errgroup limit of 5, reused unchanged rather
// than widened.
//
// One key's failure abandons THAT KEY ONLY: MarkFailure records the attempt
// against the existing row (leaving its payload and refreshed_at untouched, so
// a previously-good row keeps serving) and the loop moves on. A partially
// accumulated list is never written — Put is reached only after a whole key
// succeeds.
//
// client must be built with tmdb.Config.BypassCache set; see the package doc.
func refreshTMDBCategories(ctx context.Context, d Deps, client *tmdb.Client) {
	for _, t := range tmdbTargets {
		refreshOneTMDBTarget(ctx, d, client, t)
	}
}

// refreshOneTMDBTarget accumulates and stores exactly one of the six fixed
// TMDB targets — refreshTMDBCategories' loop body, given a name (BE-8) so
// discoverrefresh.go's due-checked RefreshAll can refresh a single stale key
// without re-fetching the other five. Pure extraction: refreshTMDBCategories'
// behaviour is unchanged, it just calls this once per target now.
func refreshOneTMDBTarget(ctx context.Context, d Deps, client *tmdb.Client, t tmdbTarget) {
	key := t.cacheKey()
	items, rawPages, exhausted, err := accumulateCategory(ctx, client, t)
	if err != nil {
		log.Printf("discoverrefresh: refreshing tmdb/%s failed, keeping any previous payload: %v", key, err)
		if merr := d.Cache.MarkFailure(ctx, "tmdb", key, err); merr != nil {
			log.Printf("discoverrefresh: recording the tmdb/%s failure: %v", key, merr)
		}
		return
	}
	payload, err := marshalTMDBItems(items)
	if err != nil {
		log.Printf("discoverrefresh: encoding tmdb/%s failed: %v", key, err)
		if merr := d.Cache.MarkFailure(ctx, "tmdb", key, err); merr != nil {
			log.Printf("discoverrefresh: recording the tmdb/%s failure: %v", key, merr)
		}
		return
	}
	if err := d.Cache.Put(ctx, "tmdb", key, payload, tmdbPageSize, rawPages, exhausted); err != nil {
		log.Printf("discoverrefresh: storing tmdb/%s failed: %v", key, err)
	}
}

// accumulateCategory walks raw upstream pages for one target, filtering as it
// goes, and returns the flat survivor list plus the two values the read path
// needs to reason about the window's edge.
//
// THE LOOP ALWAYS STOPS ON A RAW PAGE BOUNDARY, and rawPages counts the pages
// actually consumed. That is the whole reason the counter exists. Because the
// US-release filter drops items, len(items)/tmdbPageSize and rawPages diverge —
// a run that consumed 5 raw pages may hold ~70 survivors. Serving
// ceil(70/20) = 4 logical pages and then asking the live path for raw page
// rawPages+1 = 6 is exact: every accumulated item is reachable, and nothing
// already rendered is re-fetched. Truncating to carouselCachedPages*tmdbPageSize
// would silently discard the tail; falling through at logical page K+1 would
// re-serve items already shown.
//
// exhausted is set ONLY when the upstream itself returned an empty page —
// genuine end of data. A run that merely hit the depth target is NOT exhausted,
// and Entry.Slice must not report it as such.
//
// filterReleasedMovies' retry-page logic is deliberately NOT reproduced here. It
// exists solely to satisfy the frontend's one-click-equals-one-page contract;
// accumulating flat makes it unnecessary, and reproducing it is what would
// re-create — in frozen, 24h form — the duplicate-item defect that function's
// own ACCEPTED LIMITATION note records.
//
// Any upstream error abandons the whole key (no partial payload is returned to
// the caller), because a gap in the middle of a flat accumulated list is not
// something the read path's page arithmetic can express.
func accumulateCategory(ctx context.Context, client *tmdb.Client, t tmdbTarget) (items []tmdb.Item, rawPages int, exhausted bool, err error) {
	for rawPages < maxRawPages && len(items) < carouselCachedPages*tmdbPageSize {
		batch, ferr := t.fetch(ctx, client, rawPages+1)
		if ferr != nil {
			return nil, 0, false, fmt.Errorf("fetching %s page %d: %w", t.cacheKey(), rawPages+1, ferr)
		}
		rawPages++
		if len(batch) == 0 {
			exhausted = true
			break
		}
		if t.filtersUnreleased() {
			batch = FilterByUSRelease(ctx, client, batch)
		}
		items = append(items, batch...)
	}
	return items, rawPages, exhausted, nil
}

// marshalTMDBItems encodes each item SEPARATELY, so the payload is a slice of N
// item bodies rather than one blob holding an array. Store.Put marshals the
// []json.RawMessage it is given, so collapsing the items into a single element
// here would store [[{…},{…}]] and break the read path's byte-transparency
// contract for every consumer.
func marshalTMDBItems(items []tmdb.Item) ([]json.RawMessage, error) {
	out := make([]json.RawMessage, 0, len(items))
	for _, item := range items {
		raw, err := json.Marshal(item)
		if err != nil {
			return nil, fmt.Errorf("encoding tmdb item %d: %w", item.ID, err)
		}
		out = append(out, raw)
	}
	return out, nil
}

// FilterByUSRelease checks every item's US release status concurrently (bounded
// to 5 in flight, to avoid firing dozens of simultaneous TMDB calls for one
// page) and returns the survivors in their original order. Fails OPEN on a
// per-item HasUSRelease error: logs it and keeps the item rather than failing
// the whole page. One transient TMDB hiccup among up to 20 per-item calls (more
// during a retry burst) must not blank the entire Trending/Popular Movies row
// for every viewer — the same never-an-error posture Discover's other per-item
// TMDB lookups already have (see fetchTitlePoster/posterHandler).
//
// MOVED here VERBATIM from internal/api/discover.go's unexported
// filterByUSRelease, with sess *mode.Session replaced by the one field it ever
// touched, *tmdb.Client. Two callers now share ONE implementation: this
// package's write path passes a BypassCache client, and internal/api's
// filterReleasedMovies passes sess.TMDB. Duplicating it instead would let the
// cached and live paths drift on what "released" means — precisely the class of
// bug this repo's convention notes keep flagging.
//
// The one deliberate non-verbatim change is the log prefix: it read "discover:"
// while this lived in internal/api and now reads "discoverrefresh:", because
// with two callers that prefix would otherwise misattribute a write-path
// failure to the live read path. No test asserts on the string.
func FilterByUSRelease(ctx context.Context, client *tmdb.Client, items []tmdb.Item) []tmdb.Item {
	keep := make([]bool, len(items))
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(5)
	for i, item := range items {
		i, item := i, item
		g.Go(func() error {
			ok, err := client.HasUSRelease(gctx, item.ID)
			if err != nil {
				log.Printf("discoverrefresh: HasUSRelease failed for tmdbId=%d, keeping the item rather than filtering the row to empty: %v", item.ID, err)
				keep[i] = true
				return nil
			}
			keep[i] = ok
			return nil
		})
	}
	_ = g.Wait() // every goroutine above always returns nil — see the fail-open note.
	out := make([]tmdb.Item, 0, len(items))
	for i, item := range items {
		if keep[i] {
			out = append(out, item)
		}
	}
	return out
}
