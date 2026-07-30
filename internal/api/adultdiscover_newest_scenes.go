package api

import (
	"context"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/labbersanon/sakms/internal/adultnewest"
	"github.com/labbersanon/sakms/internal/apidto"
	"github.com/labbersanon/sakms/internal/connections"
	"github.com/labbersanon/sakms/internal/identify"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/rssfeed"
	"github.com/labbersanon/sakms/internal/rssfeeds"
	"github.com/labbersanon/sakms/internal/settings"
)

// newestScenesOutboundTimeout bounds the whole newest-entity-scenes drill-down
// — this package's own copy of cmd/sakms/main.go's outboundTimeout (package
// main, so not importable here), matching internal/recheck's and
// internal/adultnewest's same local-copy pattern. Scoped name (not a bare
// outboundTimeout) to avoid colliding with any sibling package-level identifier
// in internal/api. A var (not const) purely so tests can tighten it to prove
// the handler returns within the bound against a Prowlarr that never responds,
// exactly as mode.TPDBGraphQLURL is a var so tests can point it at a fake
// server — nothing mutates it in production.
var newestScenesOutboundTimeout = 15 * time.Second

// newestScenesEnrichTimeout bounds the page>1 catalog enrichment pass
// (identify.EnrichNewestScenes) — a child of the outboundTimeout-bounded tctx,
// so it's capped by whichever deadline is nearer (min of the two), never
// additive. ~2 throttled calls at ~1s spacing + network ≈ 2-4s wall clock, so
// 6s is generous headroom while still leaving margin to encode/return within the
// 15s ceiling. A var (not const) purely so tests can tighten it to prove the
// handler returns within the bound against a box that never responds — same
// rationale as newestScenesOutboundTimeout above; nothing mutates it in
// production.
var newestScenesEnrichTimeout = 6 * time.Second

// rssFeedTimeout is the tight per-feed ceiling for the supplementary RSS fetch
// — deliberately shorter than outboundTimeout so one slow feed can't consume
// the whole handler's budget. Each feed's context is still a child of the
// outboundTimeout-bounded context, so this is capped by whichever deadline is
// nearer (min of the two), never additive.
const rssFeedTimeout = 5 * time.Second

// rssFeedConcurrency bounds how many adult RSS feeds are fetched at once —
// matches the "bounded concurrency" convention this codebase already uses for
// fan-out (see discover_detail.go's errgroup soft-fail), never an unbounded
// goroutine-per-feed fan-out.
const rssFeedConcurrency = 5

// adultNewestEntityScenesHandler backs the Performers/Studios drill-down —
// GET /api/modes/adult/discover/newest/entity-scenes?kind={performer|studio}&name={name}&page={N}.
//
// It is the ONE deliberate, owner-approved exception to CLAUDE.md's "Discover
// never queries Prowlarr, full stop" rule (see that file's dated carve-out for
// this handler). The trigger shape is on-demand, not automatic:
//
//   - page<=1 (the initial drill-down open): returns ONLY pool-joined items and
//     fires ZERO Prowlarr calls. The enabled Adult RSS feeds are fetched and
//     joined against the identify pipeline's matched-entity pool
//     (adult_newest_releases); an item with NO pool match is DROPPED, never
//     shown raw — the same drop-unmatched precedent resolveRssFeedHandler
//     (rss_feeds.go) already established, so the drill-down never displays an
//     unverified title. HasMore is always true, so Show More is always offered
//     at least once (even for an entity the pool has nothing for yet).
//   - page>1 (an explicit operator Show More click): fires EXACTLY ONE
//     Prowlarr.Search, for ONE entity. Each raw Prowlarr result is first checked
//     against the release-level cache (adult_newest_scene_matches) by download
//     URL; only cache MISSES go through identify.EnrichNewestScenes (the clicked
//     entity's single known box — from the pool's entity_source — resolved by
//     exact name, its newest catalog fetched once and matched locally). Newly
//     matched results are written through to the cache; any item still unmatched
//     is DROPPED (same drop-unmatched rule). HasMore is always false — Prowlarr
//     doesn't paginate further.
//
// The one-call-per-Show-More constraint is load-bearing, not incidental: never
// add a path that could re-query Prowlarr more than once per page>1 invocation,
// and never re-introduce an automatic per-open (or per-scroll/per-card) Prowlarr
// query. Enrichment adds ONLY StashDB/FansDB/TPDB catalog calls plus the local
// cache — the Prowlarr carve-out stays intact.
//
// settingsStore is threaded through because mode.Build requires it (KidsRoot/AI
// wiring). releaseStore is the Adult identify pipeline's matched-entity pool
// (adult_newest_releases) plus the release-level match cache
// (adult_newest_scene_matches) — both live on the same *adultnewest.ReleaseStore
// already wired into NewMux, so this adds no NewMux-signature churn.
func adultNewestEntityScenesHandler(httpClient *http.Client, connStore *connections.Store, settingsStore *settings.Store, rssFeedsStore *rssfeeds.Store, releaseStore *adultnewest.ReleaseStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		q := r.URL.Query()

		name := q.Get("name")
		if name == "" {
			http.Error(w, "name is required", http.StatusBadRequest)
			return
		}
		// kind (performer|studio) selects which pool row_type's entity_source
		// (known box) the page>1 enrichment resolves against, and labels which
		// drill-down was opened for logging.
		kind := q.Get("kind")
		page := parseNewestPage(q.Get("page"))

		if page <= 1 {
			// Initial open: pool-only, zero Prowlarr calls. Bound the RSS fetch
			// so a hung feed can't stall the endpoint.
			tctx, cancel := context.WithTimeout(ctx, newestScenesOutboundTimeout)
			defer cancel()
			items := fetchAdultRSSItems(tctx, httpClient, rssFeedsStore, releaseStore, name)
			deduped := dedupeAdultDiscoverItems(items)
			writeJSON(w, apidto.AdultNewestScenesPage{Items: deduped, HasMore: true})
			return
		}

		// Show More (page>1): exactly ONE Prowlarr search + cache-checked
		// enrichment.
		sess, err := mode.Build(ctx, connStore, settingsStore, httpClient, nil, mode.Adult)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if sess.Prowlarr == nil {
			http.Error(w, "prowlarr isn't configured yet — add it in Settings first", http.StatusBadRequest)
			return
		}

		query := normalizeAdultQuery(name)

		tctx, cancel := context.WithTimeout(ctx, newestScenesOutboundTimeout)
		defer cancel()

		releases, err := sess.Prowlarr.Search(tctx, query, []int{adultAutoGrabCategory})
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		log.Printf("adult newest entity-scenes: kind=%q name=%q page=%d — Prowlarr returned %d releases", kind, name, page, len(releases))

		items := make([]apidto.AdultDiscoverItem, 0, len(releases))
		for _, rel := range releases {
			items = append(items, apidto.AdultDiscoverItem{
				Title:        rel.Title,
				ReleaseTitle: rel.Title,
				DownloadURL:  rel.DownloadURL,
				Protocol:     string(rel.Protocol),
				SizeBytes:    rel.Size,
				Source:       "prowlarr",
			})
		}

		matched := enrichNewestScenesShowMore(tctx, sess.Identify, releaseStore, kind, name, items)
		deduped := dedupeAdultDiscoverItems(matched)
		writeJSON(w, apidto.AdultNewestScenesPage{Items: deduped, HasMore: false})
	}
}

// parseNewestPage parses PaginatedStrip's 1-based page query param, defaulting
// to 1 for an absent, non-numeric, or below-1 value.
func parseNewestPage(s string) int {
	if s == "" {
		return 1
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 {
		return 1
	}
	return n
}

// enrichNewestScenesShowMore is the page>1 enrichment pass over the raw Prowlarr
// items: it checks the release-level cache (adult_newest_scene_matches) by
// download URL first, calls identify.EnrichNewestScenes ONLY for cache misses
// (and only when the entity has a known box in the pool), writes newly-matched
// results through to the cache, and DROPS any item still unmatched. Returns the
// surviving (confidently-matched) items in their original order. Best-effort: a
// cache-lookup error, a missing pool box, a box error/timeout, or no title match
// simply drops the affected items — it never fails the handler.
//
// The cache short-circuit is load-bearing for the "second Show More doesn't
// re-fetch the box catalog" property: EnrichNewestScenes is invoked ONLY when
// there is at least one miss, so an all-hits request makes zero box calls (zero
// resolve AND zero catalog).
func enrichNewestScenesShowMore(ctx context.Context, id *identify.Identifier, releaseStore *adultnewest.ReleaseStore, kind, name string, items []apidto.AdultDiscoverItem) []apidto.AdultDiscoverItem {
	if len(items) == 0 {
		return items
	}

	// 1. Batched cache lookup by download URL.
	urls := make([]string, 0, len(items))
	for i := range items {
		if items[i].DownloadURL != "" {
			urls = append(urls, items[i].DownloadURL)
		}
	}
	cache := map[string]adultnewest.SceneMatch{}
	if releaseStore != nil {
		if got, err := releaseStore.SceneMatchesByDownloadURLs(ctx, urls); err != nil {
			// Best-effort: a cache hiccup just means everything is treated as a
			// miss and (budget permitting) re-matched live.
			log.Printf("adult newest entity-scenes: cache lookup failed (treating all as misses): %v", err)
		} else {
			cache = got
		}
	}

	// keep[i] marks an item that survives (cache hit or fresh match); the rest
	// are dropped. Misses are deferred to EnrichNewestScenes.
	keep := make([]bool, len(items))
	var missIdx []int
	var missTitles []string
	for i := range items {
		if m, ok := cache[items[i].DownloadURL]; ok && items[i].DownloadURL != "" {
			applyCachedSceneMatch(&items[i], m)
			keep[i] = true
			continue
		}
		missIdx = append(missIdx, i)
		missTitles = append(missTitles, items[i].ReleaseTitle)
	}

	// 2. Cache misses: resolve against the entity's ONE known box (from the pool
	// row's entity_source). No pool row → no known box → every miss is dropped.
	if len(missTitles) > 0 && id != nil && releaseStore != nil {
		box, err := releaseStore.EntityBox(ctx, adultnewest.RowType(kind), name)
		if err != nil {
			log.Printf("adult newest entity-scenes: looking up entity box (dropping page>1 misses): %v", err)
			box = ""
		}
		if box != "" {
			ectx, cancel := context.WithTimeout(ctx, newestScenesEnrichTimeout)
			defer cancel()
			matches, err := id.EnrichNewestScenes(ectx, kind, name, box, missTitles)
			if err != nil {
				log.Printf("adult newest entity-scenes: catalog enrichment failed (dropping misses): %v", err)
			} else {
				for mi, mr := range matches {
					if mr == nil {
						continue
					}
					pos := missIdx[mi]
					applyCatalogEnrichment(&items[pos], mr)
					keep[pos] = true
					// Write-through cache under the outer ctx (not the enrich
					// sub-timeout) so a slow box that still returned a match
					// doesn't lose the cache write to cancellation.
					if items[pos].DownloadURL != "" {
						if err := releaseStore.UpsertSceneMatch(ctx, sceneMatchFromItem(items[pos], mr.Box)); err != nil {
							log.Printf("adult newest entity-scenes: caching scene match failed (non-fatal): %v", err)
						}
					}
				}
			}
		}
	}

	// 3. Emit survivors in original order; drop the rest.
	out := make([]apidto.AdultDiscoverItem, 0, len(items))
	for i := range items {
		if keep[i] {
			out = append(out, items[i])
		}
	}
	return out
}

// sceneMatchFromItem snapshots an item's ENRICHED display fields (after
// applyCatalogEnrichment ran) into a release-level cache row. The grab-bearing
// fields are deliberately not stored — they're re-supplied from the live raw
// Prowlarr result on the next request.
func sceneMatchFromItem(it apidto.AdultDiscoverItem, box string) adultnewest.SceneMatch {
	return adultnewest.SceneMatch{
		DownloadURL:     it.DownloadURL,
		Title:           it.Title,
		Studio:          it.Studio,
		Image:           it.Image,
		Date:            it.Date,
		Genres:          it.Genres,
		Performers:      it.Performers,
		DurationSeconds: it.DurationSeconds,
		Box:             box,
	}
}

// applyEnrichedFields applies the shared overwrite-Title / populate-if-empty
// field mapping both a fresh box match and a cached release-level match use —
// so a cache hit and a fresh match produce identical display output. The four
// grab-bearing fields (ReleaseTitle/DownloadURL/Protocol/SizeBytes) and Source
// are never touched here; callers normalize their own source type (a fresh
// identify.MatchResult's comma-joined strings, or a cached SceneMatch's
// already-decoded slices) into these plain fields first.
func applyEnrichedFields(it *apidto.AdultDiscoverItem, title, image, studio, date string, genres, performers []string, durationSeconds int) {
	if title != "" {
		it.Title = title
	}
	if it.Image == "" && image != "" {
		it.Image = image
	}
	if it.Studio == "" && studio != "" {
		it.Studio = studio
	}
	if it.Date == "" && date != "" {
		it.Date = date
	}
	if len(it.Genres) == 0 && len(genres) > 0 {
		it.Genres = genres
	}
	if len(it.Performers) == 0 && len(performers) > 0 {
		it.Performers = performers
	}
	if it.DurationSeconds == 0 && durationSeconds > 0 {
		it.DurationSeconds = durationSeconds
	}
}

// applyCachedSceneMatch reconstructs an enriched item's presentation from a
// cached release-level match — see applyEnrichedFields for the shared mapping.
func applyCachedSceneMatch(it *apidto.AdultDiscoverItem, m adultnewest.SceneMatch) {
	applyEnrichedFields(it, m.Title, m.Image, m.Studio, m.Date, m.Genres, m.Performers, m.DurationSeconds)
}

// applyCatalogEnrichment applies a fresh MatchResult to an item — see
// applyEnrichedFields for the shared mapping. Genres/Performers are split on a
// LITERAL comma (not comma-space, the no-space join boxlookup.go uses) and
// trimmed before going through the same populate-if-empty rule as a cache hit.
func applyCatalogEnrichment(it *apidto.AdultDiscoverItem, m *identify.MatchResult) {
	applyEnrichedFields(it, m.Title, m.Image, m.Studio, m.Date,
		splitCommaTrim(m.Tags), splitCommaTrim(m.Performers), m.RuntimeSeconds)
}

// splitCommaTrim splits a MatchResult's comma-joined Tags/Performers string on
// a LITERAL "," (the no-space join boxlookup.go uses), trims each piece, and
// drops empties. Returns nil for an empty input.
func splitCommaTrim(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// fetchAdultRSSItems fetches every enabled Adult-target RSS feed concurrently
// (bounded to rssFeedConcurrency) with a tight per-feed timeout, keeping only
// items whose Title contains name case-insensitively, maps them to
// apidto.AdultDiscoverItem (Source "rss"), then joins them against the Adult
// identify pipeline's matched-entity pool (adult_newest_releases) via
// poolMatchedAdultRSSItems — keeping ONLY pool-matched items. It is best-effort
// by contract: listing or fetching a feed can fail and is logged + dropped,
// never returned as an error.
func fetchAdultRSSItems(ctx context.Context, httpClient *http.Client, rssFeedsStore *rssfeeds.Store, releaseStore *adultnewest.ReleaseStore, name string) []apidto.AdultDiscoverItem {
	feeds, err := rssFeedsStore.List(ctx)
	if err != nil {
		log.Printf("adult newest entity-scenes: listing RSS feeds failed (dropping RSS supplement): %v", err)
		return nil
	}

	needle := strings.ToLower(name)

	// Plain errgroup (not WithContext): every goroutine returns nil so a single
	// feed failure never cancels its siblings — the soft-fail fan-out pattern
	// discover_detail.go uses. SetLimit caps concurrent feed fetches.
	var (
		g   errgroup.Group
		mu  sync.Mutex
		out []apidto.AdultDiscoverItem
	)
	g.SetLimit(rssFeedConcurrency)

	for _, feed := range feeds {
		if feed.Target != rssfeeds.TargetAdult || !feed.Enabled {
			continue
		}
		feed := feed
		g.Go(func() error {
			fctx, cancel := context.WithTimeout(ctx, rssFeedTimeout)
			defer cancel()

			fetched, err := rssfeed.FetchItems(fctx, httpClient, feed.FeedURL)
			if err != nil {
				log.Printf("adult newest entity-scenes: RSS feed %q (id=%d) failed, dropping its results: %v", feed.Title, feed.ID, err)
				return nil
			}

			var matched []apidto.AdultDiscoverItem
			for _, it := range fetched {
				if !strings.Contains(strings.ToLower(it.Title), needle) {
					continue
				}
				// EnclosureURL, falling back to Link — matches
				// resolveRssFeedHandler's downloadURL computation
				// (rss_feeds.go) exactly, so a degenerate no-enclosure item
				// still gets a real, joinable key instead of always being "".
				downloadURL := it.EnclosureURL
				if downloadURL == "" {
					downloadURL = it.Link
				}
				matched = append(matched, apidto.AdultDiscoverItem{
					Title:        it.Title,
					ReleaseTitle: it.Title,
					DownloadURL:  downloadURL,
					Protocol:     string(feed.Protocol),
					SizeBytes:    it.EnclosureLength,
					Source:       "rss",
				})
			}
			if len(matched) > 0 {
				mu.Lock()
				out = append(out, matched...)
				mu.Unlock()
			}
			return nil
		})
	}
	_ = g.Wait() // every goroutine returns nil — see the soft-fail note above.

	return poolMatchedAdultRSSItems(ctx, releaseStore, out)
}

// poolMatchedAdultRSSItems joins items against the Adult identify pipeline's
// matched-entity pool (adult_newest_releases) by DownloadURL — the same
// feed_item_key the background RSS scan cycle stores for a matched release
// (scan.go's feedItemKey, EnclosureURL-then-Link). A matched item gets Title
// (overwritten), Studio, and Image populated from the pool row's
// EntityTitle/EntityStudio/EntityImage — unconditionally, since this is an
// exact-key match, not a fuzzy one.
//
// Unmatched items are DROPPED — this reverses the prior "keep unmatched RSS
// items raw" carve-out and adopts resolveRssFeedHandler's established
// drop-unmatched precedent (rss_feeds.go), so the drill-down never shows an
// unverified title. Best-effort on a hard lookup error, however: rather than
// blanking the whole view on a transient DB hiccup, the raw items are returned
// unfiltered (the same fall-back resolveRssFeedHandler makes). ReleaseTitle/
// DownloadURL/Protocol/SizeBytes are never touched — only Title/Studio/Image.
func poolMatchedAdultRSSItems(ctx context.Context, releaseStore *adultnewest.ReleaseStore, items []apidto.AdultDiscoverItem) []apidto.AdultDiscoverItem {
	if releaseStore == nil || len(items) == 0 {
		return items
	}
	keys := make([]string, 0, len(items))
	for _, it := range items {
		if it.DownloadURL != "" {
			keys = append(keys, it.DownloadURL)
		}
	}
	if len(keys) == 0 {
		// Nothing joinable → nothing verifiable → drop all (never show raw).
		return nil
	}
	matches, err := releaseStore.ByFeedItemKeys(ctx, keys)
	if err != nil {
		// Best-effort: a transient pool-join hiccup shouldn't blank the whole
		// drill-down, so log and return the raw items unfiltered rather than
		// dropping everything — mirrors resolveRssFeedHandler's error path.
		log.Printf("adult newest entity-scenes: enriching RSS items against adult pool: %v", err)
		return items
	}
	// Standard in-place filter — filtered[j] is only ever written from items[i]
	// with j <= i, so reusing items' backing array is safe.
	filtered := items[:0]
	for i := range items {
		m, ok := matches[items[i].DownloadURL]
		if !ok {
			continue
		}
		// Guard against a malformed pool row blanking a field that already had a
		// raw fallback value (e.g. an empty EntityTitle would otherwise wipe the
		// display Title).
		if m.EntityTitle != "" {
			items[i].Title = m.EntityTitle
		}
		if m.EntityStudio != "" {
			items[i].Studio = m.EntityStudio
		}
		if m.EntityImage != "" {
			items[i].Image = m.EntityImage
		}
		filtered = append(filtered, items[i])
	}
	return filtered
}

// dedupeAdultDiscoverItems removes duplicates keyed by DownloadURL, falling
// back to the normalized Title when a DownloadURL is somehow empty. First
// occurrence wins. Always returns a non-nil slice so an empty result encodes as
// a flat [] JSON array, never null — the shape the scene-list drill-down UI
// shell consumes.
func dedupeAdultDiscoverItems(items []apidto.AdultDiscoverItem) []apidto.AdultDiscoverItem {
	out := make([]apidto.AdultDiscoverItem, 0, len(items))
	seen := make(map[string]bool, len(items))
	for _, it := range items {
		key := it.DownloadURL
		if key == "" {
			key = normalizeAdultQuery(it.Title)
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, it)
	}
	return out
}
