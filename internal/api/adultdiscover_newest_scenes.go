package api

import (
	"context"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/labbersanon/sakms/internal/adultnewest"
	"github.com/labbersanon/sakms/internal/apidto"
	"github.com/labbersanon/sakms/internal/connections"
	"github.com/labbersanon/sakms/internal/identify"
	"github.com/labbersanon/sakms/internal/mode"
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
//
// 30s (not 15s): the page>1 Show More search fans out to every configured
// indexer at once (confirmed live, 2026-07-30: a single search queried 6
// indexers for category 6000), and a real search against that many indexers
// can exceed 15s even when every indexer is healthy — a live "Cory Chase"
// search hit the 15s budget and surfaced as a raw context-deadline-exceeded
// error to the operator. Owner-approved bump, not a silent tolerance change.
var newestScenesOutboundTimeout = 30 * time.Second

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

// adultNewestEntityScenesHandler backs the Performers/Studios drill-down —
// GET /api/modes/adult/discover/newest/entity-scenes?kind={performer|studio}&name={name}&page={N}.
//
// It is the ONE deliberate, owner-approved exception to CLAUDE.md's "Discover
// never queries Prowlarr, full stop" rule (see that file's dated carve-out for
// this handler). The trigger shape is on-demand, not automatic:
//
//   - page<=1 (the initial drill-down open): returns ONLY pooled scene/movie
//     rows currently LINKED to the drilled entity and fires ZERO external calls
//     (no Prowlarr, no live RSS re-fetch). The matched-entity pool
//     (adult_newest_releases) is queried directly for scene/movie rows linked to
//     this performer/studio (US-2): a performer via an exact json_each match on a
//     scene's performers array, a studio via an exact entity_studio match — the
//     same linkage definition the browse-strip filter and the 0051 cleanup use.
//     HasMore is always true, so Show More is always offered at least once (even
//     for an entity the pool has nothing for yet).
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
// (adult_newest_scene_matches). feedHealth applies the D5 direct-grab exposure
// to page-1's pooled rows (the enclosure is carried only while a row's feed is
// fresh), exactly as the other pooled Adult Discover read paths do — all live on
// stores already wired into NewMux.
func adultNewestEntityScenesHandler(httpClient *http.Client, connStore *connections.Store, settingsStore *settings.Store, releaseStore *adultnewest.ReleaseStore, feedHealth *adultnewest.FeedHealth) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		q := r.URL.Query()

		name := q.Get("name")
		if name == "" {
			http.Error(w, "name is required", http.StatusBadRequest)
			return
		}
		// kind (performer|studio) selects which pool row_type this drill-down
		// queries (page 1) / resolves the known box against (page>1), and labels
		// which drill-down was opened for logging.
		kind := q.Get("kind")
		page := parseNewestPage(q.Get("page"))

		if page <= 1 {
			// Initial open: query the pool directly for scene/movie rows linked to
			// this entity — zero external calls (US-2). A lookup error degrades to
			// an empty page (still 200, HasMore true) rather than failing the
			// drill-down, mirroring the old RSS path's best-effort contract.
			scenes, err := releaseStore.ScenesLinkedToEntity(ctx, adultnewest.RowType(kind), name)
			if err != nil {
				log.Printf("adult newest entity-scenes: querying linked pool scenes for kind=%q name=%q (returning empty): %v", kind, name, err)
				scenes = nil
			}
			now := time.Now()
			items := make([]apidto.AdultDiscoverItem, 0, len(scenes))
			for _, m := range scenes {
				items = append(items, poolReleaseToDiscoverItem(m, feedHealth, now))
			}
			writeJSON(w, apidto.AdultNewestScenesPage{Items: dedupeAdultDiscoverItems(items), HasMore: true})
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

// poolReleaseToDiscoverItem maps a pooled scene/movie MatchedRelease (page 1's
// data source, US-2) into the drill-down's wire item. It applies the same D5
// direct-grab exposure as adultdiscover.go's poolReleaseToAdultScene: the
// enclosure (downloadUrl/protocol/sizeBytes) is carried ONLY while the row's feed
// is currently fresh (feedHealth.DirectGrabURL), never straight from the raw
// column — so a scene whose feed is stale falls back to the ReleaseTitle-driven
// Prowlarr grab. It deliberately does NOT apply writePooledScenes' Available
// visibility gate: US-2/US-4 define a linked scene by EXISTENCE, and availability-
// gating the drill-down while the browse strip only requires linkage would create
// a strip-vs-drilldown asymmetry.
func poolReleaseToDiscoverItem(m adultnewest.MatchedRelease, feedHealth *adultnewest.FeedHealth, now time.Time) apidto.AdultDiscoverItem {
	it := apidto.AdultDiscoverItem{
		ID:              m.EntityID,
		Title:           m.EntityTitle,
		Studio:          m.EntityStudio,
		Date:            m.EntityDate,
		Image:           m.EntityImage,
		DurationSeconds: m.EntityDurationSeconds,
		Source:          m.EntitySource,
		Genres:          m.Genres,
		Performers:      m.Performers,
		ReleaseTitle:    m.FirstSeenReleaseTitle,
	}
	if url := feedHealth.DirectGrabURL(m.DownloadURL, m.FeedID, time.Unix(m.LastConfirmedSeen, 0), now); url != "" {
		it.DownloadURL = url
		it.Protocol = m.DownloadProtocol
		it.SizeBytes = m.SizeBytes
	}
	return it
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
