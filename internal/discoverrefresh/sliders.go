package discoverrefresh

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"

	"github.com/labbersanon/sakms/internal/connections"
	"github.com/labbersanon/sakms/internal/discoversliders"
	"github.com/labbersanon/sakms/internal/tmdb"
)

// sliderSource is this source's discriminator in discover_row_cache. The
// cache_key is the slider's integer id as a decimal string — the same
// strconv.Itoa conversion Store.DeleteOrphanSliders uses, so the write path and
// the orphan sweep agree on the key.
const sliderSource = "slider"

// ErrSliderMisconfigured marks a ResolveSlider error as a permanent, per-slider
// configuration problem (bad filter_type/target pairing, a non-numeric
// filter_value) rather than a transient TMDB call failure — internal/api's
// resolveSliderHandler maps it to 400 instead of 502, so the frontend can tell
// "edit this slider" from "TMDB is down, retry."
//
// The scheduler treats it the same way: a misconfigured slider is marked failed
// and skipped, never cached and never retried into a payload, so the read path
// keeps falling through live and keeps returning the 400 that tells the
// operator to fix it.
var ErrSliderMisconfigured = errors.New("slider misconfigured")

// ResolveSlider fetches sl's items for the given 1-based page. Target == mixed
// concatenates the movie-catalog and TV-catalog results (movies first, then TV)
// where both exist — the simplest well-defined combination; Seerr-style
// interleaving-by-popularity is not attempted here (no premature sorting logic
// ahead of a real need). Studio/network have no cross-media equivalent (a movie
// production company isn't a TV concept and vice versa — see tmdb.Studio/
// tmdb.Network's doc comments), so a "mixed" studio/network slider degrades to
// its one applicable catalog rather than erroring; a slider whose Target names
// ONLY the inapplicable catalog (studio+tv, network+movie) is a genuine
// misconfiguration and errors.
//
// That degrade is why sliderPageSize is filter-type aware rather than
// target-only, and it is the single most load-bearing property in this file.
//
// It lives here rather than in internal/api because both the live read path and
// the scheduler resolve sliders, and two copies would let "what this slider
// returns" drift between the cached and live answers.
func ResolveSlider(ctx context.Context, client *tmdb.Client, sl discoversliders.Slider, page int) ([]tmdb.Item, error) {
	wantMovies := sl.Target == discoversliders.TargetMovie || sl.Target == discoversliders.TargetMixed
	wantTV := sl.Target == discoversliders.TargetTV || sl.Target == discoversliders.TargetMixed

	switch sl.FilterType {
	case discoversliders.FilterUpcoming:
		return fetchFixedFeed(ctx, wantMovies, wantTV, page, client.UpcomingMovies, client.UpcomingTV)
	case discoversliders.FilterTrending:
		return fetchFixedFeed(ctx, wantMovies, wantTV, page,
			func(ctx context.Context, page int) ([]tmdb.Item, error) {
				return client.Trending(ctx, tmdb.Movie, "week", page)
			},
			func(ctx context.Context, page int) ([]tmdb.Item, error) {
				return client.Trending(ctx, tmdb.TV, "week", page)
			})
	case discoversliders.FilterPopular:
		return fetchFixedFeed(ctx, wantMovies, wantTV, page,
			func(ctx context.Context, page int) ([]tmdb.Item, error) { return client.Popular(ctx, tmdb.Movie, page) },
			func(ctx context.Context, page int) ([]tmdb.Item, error) { return client.Popular(ctx, tmdb.TV, page) })
	case discoversliders.FilterGenre:
		id, err := sliderFilterValueInt(sl)
		if err != nil {
			return nil, err
		}
		return fetchFixedFeed(ctx, wantMovies, wantTV, page,
			func(ctx context.Context, page int) ([]tmdb.Item, error) {
				return client.DiscoverMoviesByGenre(ctx, id, page)
			},
			func(ctx context.Context, page int) ([]tmdb.Item, error) {
				return client.DiscoverTVByGenre(ctx, id, page)
			})
	case discoversliders.FilterKeyword:
		id, err := sliderFilterValueInt(sl)
		if err != nil {
			return nil, err
		}
		return fetchFixedFeed(ctx, wantMovies, wantTV, page,
			func(ctx context.Context, page int) ([]tmdb.Item, error) {
				return client.DiscoverMoviesByKeyword(ctx, id, page)
			},
			func(ctx context.Context, page int) ([]tmdb.Item, error) {
				return client.DiscoverTVByKeyword(ctx, id, page)
			})
	case discoversliders.FilterStudio:
		if sl.Target == discoversliders.TargetTV {
			return nil, fmt.Errorf("%w: slider %d: studio filter is movie-only, not valid for a tv-target slider", ErrSliderMisconfigured, sl.ID)
		}
		id, err := sliderFilterValueInt(sl)
		if err != nil {
			return nil, err
		}
		return client.DiscoverMoviesByStudio(ctx, id, page)
	case discoversliders.FilterNetwork:
		if sl.Target == discoversliders.TargetMovie {
			return nil, fmt.Errorf("%w: slider %d: network filter is series-only, not valid for a movie-target slider", ErrSliderMisconfigured, sl.ID)
		}
		id, err := sliderFilterValueInt(sl)
		if err != nil {
			return nil, err
		}
		return client.DiscoverTVByNetwork(ctx, id, page)
	default:
		return nil, fmt.Errorf("%w: slider %d: unrecognized filter type %q", ErrSliderMisconfigured, sl.ID, sl.FilterType)
	}
}

// sliderFilterValueInt parses sl.FilterValue (a stringified TMDB id) as an int —
// every non-fixed-feed FilterType stores one (see discoversliders.Store's
// validate). A parse failure means the stored value predates some future
// non-numeric FilterValue convention or was corrupted; either way it's a config
// problem, not a transient TMDB error (see ErrSliderMisconfigured).
func sliderFilterValueInt(sl discoversliders.Slider) (int, error) {
	id, err := strconv.Atoi(sl.FilterValue)
	if err != nil {
		return 0, fmt.Errorf("%w: slider %d: filter_value %q is not a valid TMDB id", ErrSliderMisconfigured, sl.ID, sl.FilterValue)
	}
	return id, nil
}

// fetchFixedFeed calls movieFn/tvFn depending on wantMovies/wantTV and
// concatenates the results — the shared dispatch shape behind every FilterType
// case in ResolveSlider that has both a movie and a TV sibling method
// (upcoming/trending/popular/genre/keyword all do; studio/network don't, and are
// handled separately there).
//
// One call to this with BOTH flags set produces 40 items for one logical page,
// which is the whole reason sliderPageSize exists.
func fetchFixedFeed(ctx context.Context, wantMovies, wantTV bool, page int, movieFn, tvFn func(context.Context, int) ([]tmdb.Item, error)) ([]tmdb.Item, error) {
	var items []tmdb.Item
	if wantMovies {
		mv, err := movieFn(ctx, page)
		if err != nil {
			return nil, err
		}
		items = append(items, mv...)
	}
	if wantTV {
		tv, err := tvFn(ctx, page)
		if err != nil {
			return nil, err
		}
		items = append(items, tv...)
	}
	return items, nil
}

// sliderPageSize is the LOGICAL page size of sl's live response — how many items
// one ResolveSlider call returns — and it is written to the row's page_size
// column so the read path never has to re-derive it.
//
// 40 ONLY when a mixed-target slider actually reaches fetchFixedFeed, which
// calls both movieFn and tvFn and concatenates them. studio and network do NOT
// reach it: ResolveSlider's FilterStudio/FilterNetwork cases deliberately
// degrade a mixed slider to its one applicable catalog rather than erroring, so
// those return a single 20-item page even at target=mixed.
//
// Keying this on Target alone — as an earlier revision of this feature's plan
// did — makes every mixed studio/network slider serve 40 items where the live
// path serves 20: cached page 1 would be live pages 1+2, and every page after
// the first would be half the items and the wrong ones. That is silent content
// corruption, not a performance regression, which is why the predicate is
// filter-type aware and why it is tested for BOTH mixed cases.
func sliderPageSize(sl discoversliders.Slider) int {
	if sl.Target != discoversliders.TargetMixed {
		return tmdbPageSize
	}
	switch sl.FilterType {
	case discoversliders.FilterStudio, discoversliders.FilterNetwork:
		return tmdbPageSize // single-catalog degrade, not a concatenation
	default:
		return 2 * tmdbPageSize // upcoming/trending/popular/genre/keyword all have both siblings
	}
}

// refreshSliders refreshes every ENABLED slider in turn, then sweeps rows left
// behind by sliders that were deleted or disabled.
//
// Strictly sequential, like every other refresh path here: one trigger must
// never explode into a concurrent burst of external queries. A slider that fails
// is logged and skipped so one bad config cannot kill the sweep.
func refreshSliders(ctx context.Context, d Deps, client *tmdb.Client) error {
	sliders, err := d.SlidersStore.List(ctx)
	if err != nil {
		return fmt.Errorf("listing discover sliders: %w", err)
	}
	keep := make([]int, 0, len(sliders))
	for _, sl := range sliders {
		if !sl.Enabled {
			continue
		}
		keep = append(keep, sl.ID)
		if err := refreshOneSlider(ctx, d, client, sl); err != nil {
			log.Printf("discoverrefresh: refreshing slider %d (%s): %v", sl.ID, sl.Title, err)
		}
	}
	if err := d.Cache.DeleteOrphanSliders(ctx, keep); err != nil {
		return err
	}
	return nil
}

// RefreshSlider refreshes exactly one slider by id, building its own
// cache-bypassing TMDB client.
//
// It is the entry point the create/update lifecycle hooks use, so a slider an
// operator just saved is populated immediately instead of waiting up to a whole
// interval. A disabled or unknown id is a silent no-op — there is nothing to
// cache, and the orphan sweep removes any row it left behind.
//
// Claude 2026-08-03: added the d.Cache nil guard and the inFlightKeys
// consultation (BE-16, plan §5.1, T-12b).
// Reason: this is now called fire-and-forget from createSliderHandler/
// updateSliderHandler (internal/api/discover_sliders.go), which build their
// Deps from NewMux's discoverCache param — nil at every call site that
// doesn't explicitly seed a cache (every pre-BE-17 test, and any future
// caller that passes a zero-value Deps). Without the guard, refreshOneSlider
// would dereference a nil *Store inside a background goroutine, taking the
// whole process down instead of degrading like RefreshAll's identical guard
// already does. The KeyInFlight check is §5.1's second single-flight guard:
// if a RefreshAll cycle is already refreshing this exact slider (it marks
// the key before calling refreshOneSlider — see refreshSlidersDue), this
// populate returns immediately rather than racing that cycle's own Put for
// the same key.
// Troubleshooting: a slider created during a running cycle whose row never
// appears until the cycle's own pass writes it is this branch working as
// designed, not a bug — see inFlightKeys' doc comment.
// Review if: RefreshSlider gains a caller that needs to know the skip
// happened (it currently returns nil either way, same as "nothing to do").
func RefreshSlider(ctx context.Context, d Deps, id int) error {
	if d.Cache == nil {
		return nil
	}
	sliders, err := d.SlidersStore.List(ctx)
	if err != nil {
		return fmt.Errorf("listing discover sliders: %w", err)
	}
	for _, sl := range sliders {
		if sl.ID != id {
			continue
		}
		if !sl.Enabled {
			return nil
		}
		key := strconv.Itoa(sl.ID)
		if KeyInFlight(sliderSource, key) {
			return nil // an in-flight cycle is about to write this key anyway
		}
		client, err := buildBypassedTMDBClient(ctx, d)
		if err != nil {
			return err
		}
		if client == nil {
			return nil // TMDB isn't configured — not a failure, nothing to cache
		}
		return refreshOneSlider(ctx, d, client, sl)
	}
	return nil
}

// refreshOneSlider accumulates sl's items page by page and writes ONE row.
//
// Three outcomes, deliberately distinct:
//
//   - a fetch error (including ErrSliderMisconfigured) — MarkFailure, NO Put,
//     abandon this key. Never writes a partial list: a refresh either replaces
//     the whole payload or leaves the previous one untouched, which is what
//     makes a failed refresh stale-but-available rather than blank.
//   - a zero-item accumulation — writes NOTHING AT ALL: no row, no MarkFailure.
//     See the source-specific rule below.
//   - otherwise — one Put with the row's own page_size/raw_pages/exhausted.
//
// # Why a zero-item slider writes no row (source-specific, do not generalize)
//
// fetchFixedFeed declares `var items []tmdb.Item` and only appends, so a slider
// whose branches all come back empty resolves to a NIL slice, which the live
// handler encodes as `null` — not `[]`. Caching an empty list here would make
// the cached response `[]` where the live one is `null`, and would freeze a
// misconfigured or dried-up slider into serving that forever instead of
// retrying live. Writing nothing reproduces today's behaviour exactly: the read
// path finds no row and falls through.
//
// This rule belongs to the `slider` source ONLY and is enforced here rather than
// in Store.Put, which is deliberately source-agnostic — tmdb, trakt and stashbox
// all encode an empty result as `[]` and MUST be allowed to cache it, or an
// operator with an empty Trakt watchlist would get a live Trakt call on every
// single Discover load, forever.
func refreshOneSlider(ctx context.Context, d Deps, client *tmdb.Client, sl discoversliders.Slider) error {
	key := strconv.Itoa(sl.ID)
	perPage := sliderPageSize(sl)

	var items []tmdb.Item
	rawPages := 0
	exhausted := false
	for rawPages < maxRawPages && len(items) < carouselCachedPages*perPage {
		batch, err := ResolveSlider(ctx, client, sl, rawPages+1)
		if err != nil {
			if markErr := d.Cache.MarkFailure(ctx, sliderSource, key, err); markErr != nil {
				log.Printf("discoverrefresh: marking slider %d failed: %v", sl.ID, markErr)
			}
			return err
		}
		rawPages++
		items = append(items, batch...)
		if len(batch) < perPage {
			// Stop accumulating on ANY short page: a partly filled page
			// followed by a full one would make cached page N straddle two
			// upstream pages, so cached page N would no longer equal live page
			// N and Entry.Slice's RawPages rewrite would be wrong. Stopping
			// here keeps accumulation aligned to a raw upstream page boundary
			// by construction.
			//
			// But EXHAUSTED only when the upstream returned nothing at all. A
			// short-but-nonzero page can still be followed by more: a mixed
			// fixed-feed slider whose TV catalog ran dry while its movie
			// catalog continues returns 20 of an expected 40, and reporting
			// that row exhausted would serve [] for every later page where the
			// live path still has real items.
			exhausted = len(batch) == 0
			break
		}
	}

	if len(items) == 0 {
		log.Printf("discoverrefresh: slider %d (%s) resolved to zero items — leaving it uncached so reads keep falling through live", sl.ID, sl.Title)
		return nil
	}

	payload, err := marshalSliderItems(items)
	if err != nil {
		return err
	}
	return d.Cache.Put(ctx, sliderSource, key, payload, perPage, rawPages, exhausted)
}

// marshalSliderItems encodes each item on its own, so the stored payload is a
// list of item BODIES the read path can re-emit verbatim — that per-item
// encoding is what makes a cached response byte-identical to the live one.
func marshalSliderItems(items []tmdb.Item) ([]json.RawMessage, error) {
	out := make([]json.RawMessage, 0, len(items))
	for _, it := range items {
		body, err := json.Marshal(it)
		if err != nil {
			return nil, fmt.Errorf("encoding slider item %d: %w", it.ID, err)
		}
		out = append(out, body)
	}
	return out, nil
}

// buildBypassedTMDBClient constructs a TMDB client straight from the connection
// store, or (nil, nil) when TMDB isn't configured — the same tolerant,
// standalone construction recheck.buildTMDB does, minus the Session.
//
// BypassCache is always set: this client's traffic must neither read nor write
// internal/tmdb's shared response LRU. See tmdb.Config.BypassCache for why both
// halves matter.
func buildBypassedTMDBClient(ctx context.Context, d Deps) (*tmdb.Client, error) {
	conn, err := d.ConnStore.Get(ctx, "tmdb")
	if errors.Is(err, connections.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	httpClient := d.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: outboundTimeout}
	}
	// TMDB is a fixed public service — its base URL is hardcoded, not conn.URL.
	return tmdb.New(tmdb.Config{BaseURL: tmdb.DefaultBaseURL, APIKey: conn.APIKey, BypassCache: true}, httpClient), nil
}
