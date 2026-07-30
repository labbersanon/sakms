package api

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
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

// newestScenesEnrichTimeout bounds the Phase-2 catalog enrichment pass
// (identify.EnrichNewestScenes) — a child of the outboundTimeout-bounded tctx,
// so it's capped by whichever deadline is nearer (min of the two), never
// additive. ~4 throttled calls across independent host queues at ~1s spacing +
// network ≈ 2-4s wall clock, so 6s is generous headroom while still leaving
// margin to encode/return within the 15s ceiling. A var (not const) purely so
// tests can tighten it to prove the handler returns within the bound against a
// box that never responds — same rationale as newestScenesOutboundTimeout
// above; nothing mutates it in production.
var newestScenesEnrichTimeout = 6 * time.Second

// rssFeedTimeout is the tight per-feed ceiling for the supplementary RSS fetch
// (step 5) — deliberately shorter than outboundTimeout so one slow feed can't
// consume the whole handler's budget. Each feed's context is still a child of
// the outboundTimeout-bounded context, so this is capped by whichever deadline
// is nearer (min of the two), never additive.
const rssFeedTimeout = 5 * time.Second

// rssFeedConcurrency bounds how many adult RSS feeds are fetched at once —
// matches the "bounded concurrency" convention this codebase already uses for
// fan-out (see discover_detail.go's errgroup soft-fail), never an unbounded
// goroutine-per-feed fan-out.
const rssFeedConcurrency = 5

// adultNewestEntityScenesHandler backs the Performers/Studios drill-down —
// GET /api/modes/adult/discover/newest/entity-scenes?kind={performer|studio}&name={name}.
//
// It is the ONE deliberate, owner-approved exception to CLAUDE.md's "Discover
// never queries Prowlarr, full stop" rule (see that file's second dated
// carve-out): it fires EXACTLY ONE Prowlarr.Search, for ONE entity, only on an
// explicit operator drill-down open — never automatically, never per-card,
// never per-scroll (pagination stays inside the returned result set). The
// ONE-call-per-open constraint is load-bearing, not incidental — do not add
// any path that could re-query Prowlarr more than once per invocation.
//
// Prowlarr is the primary source; enabled Adult-target RSS feeds are a
// best-effort supplement whose ANY failure (timeout, network, parse) is logged
// and dropped — RSS never fails the handler, Prowlarr carries the weight.
// Results map to a flat []apidto.AdultDiscoverItem. RSS-sourced items are
// additionally, best-effort joined against the Adult identify pipeline's
// matched-entity pool (see fetchAdultRSSItems/enrichAdultRSSItemsFromPool): a
// release the background scan cycle already identified gets its real
// Title/Studio/Image from that pool instead of the raw RSS item title.
//
// Items still raw after that pool join (Prowlarr-sourced items, which have no
// pool entry, plus any RSS item too new for the pool yet) then get a Phase-2
// best-effort catalog enrichment (see enrichNewestScenes /
// identify.EnrichNewestScenes): the clicked studio/performer is resolved to
// each configured StashDB/FansDB/TPDB catalog id and that entity's newest scene
// catalog is fetched once per box and matched locally, populating
// Image/Studio/Date/Genres/Performers/DurationSeconds and overwriting the raw
// display Title on a confident match. Enrichment is best-effort: a failed
// resolve, box error, or timeout leaves the item at its raw fallback and never
// fails the handler. It adds ONLY StashDB/FansDB/TPDB catalog calls — still
// exactly one Prowlarr.Search per invocation, the carve-out intact. An item
// with no confident catalog match keeps Image/Studio/Date/Rating empty — the
// honest, documented consequence of a live-only source.
//
// settingsStore is threaded through because mode.Build requires it (KidsRoot/AI
// wiring) — it has no settingsStore-free variant. It (and rssFeedsStore) are
// already NewMux params, so this deviation from the plan's 3-param sketch adds
// no NewMux-signature churn.
//
// releaseStore is the Adult identify pipeline's matched-entity pool
// (adult_newest_releases, internal/adultnewest) — threaded through so the RSS
// supplement can be joined against it (see fetchAdultRSSItems), the same store
// already wired into adultNewestPerformerGendersHandler above and
// resolveRssFeedHandler (internal/api/rss_feeds.go), not a new store.
func adultNewestEntityScenesHandler(httpClient *http.Client, connStore *connections.Store, settingsStore *settings.Store, rssFeedsStore *rssfeeds.Store, releaseStore *adultnewest.ReleaseStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		q := r.URL.Query()

		name := q.Get("name")
		if name == "" {
			http.Error(w, "name is required", http.StatusBadRequest)
			return
		}
		// kind (performer|studio) is behavior-neutral: the Prowlarr query and the
		// RSS title-contains filter both key on name alone, so kind only labels
		// which row was opened. Logged (not branched/validated) so the drill-down
		// open is traceable, matching discover_availability.go's in-handler logging.
		kind := q.Get("kind")

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

		// Bound the whole rest of the handler: a Prowlarr (or RSS feed) that never
		// responds is cancelled at outboundTimeout, so this endpoint can never hang.
		tctx, cancel := context.WithTimeout(ctx, newestScenesOutboundTimeout)
		defer cancel()

		// Prowlarr — primary, EXACTLY ONE call. On error the whole handler 502s
		// (Search-screen error shape); RSS below never gets a chance to mask it.
		releases, err := sess.Prowlarr.Search(tctx, query, []int{adultAutoGrabCategory})
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		log.Printf("adult newest entity-scenes: kind=%q name=%q — Prowlarr returned %d releases", kind, name, len(releases))

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

		// RSS — supplementary, best-effort. Appended after Prowlarr so Prowlarr
		// wins the dedupe on any shared release; any feed failure is logged and
		// that feed's items dropped, never propagated.
		items = append(items, fetchAdultRSSItems(tctx, httpClient, rssFeedsStore, releaseStore, name)...)

		deduped := dedupeAdultDiscoverItems(items)

		// Phase 2: best-effort catalog enrichment of items still raw after the
		// Phase-1 pool join (in place, by index). Never fails the handler.
		enrichNewestScenes(tctx, sess.Identify, kind, name, deduped)

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(deduped)
	}
}

// enrichNewestScenes runs the Phase-2 catalog enrichment (§5-6 of the
// drill-down plan) in place over the deduped item slice. It collects only the
// items STILL raw after Phase 1 — those whose display Title still equals their
// ReleaseTitle, i.e. neither the pool join nor anything else has replaced it
// (Phase 1's defining mutation is Title = EntityTitle, so a pool hit is
// excluded; Prowlarr items and pool-miss RSS items are included) — resolves the
// entity's catalog id per box, and applies the §6 field mapping to each
// confident match. Best-effort: an enrichment error/timeout logs and returns,
// leaving every item at its current (raw or Phase-1-enriched) state. The four
// grab-bearing fields (ReleaseTitle/DownloadURL/Protocol/SizeBytes) and Source
// are never written here.
func enrichNewestScenes(ctx context.Context, id *identify.Identifier, kind, name string, items []apidto.AdultDiscoverItem) {
	if id == nil {
		return
	}
	// itemIdx maps each release-title slot handed to EnrichNewestScenes back to
	// its index in items, so a returned match can be applied to the right card.
	var itemIdx []int
	var releaseTitles []string
	for i := range items {
		if items[i].ReleaseTitle != "" && items[i].Title == items[i].ReleaseTitle {
			itemIdx = append(itemIdx, i)
			releaseTitles = append(releaseTitles, items[i].ReleaseTitle)
		}
	}
	if len(releaseTitles) == 0 {
		return
	}

	ectx, cancel := context.WithTimeout(ctx, newestScenesEnrichTimeout)
	defer cancel()
	matches, err := id.EnrichNewestScenes(ectx, kind, name, releaseTitles)
	if err != nil {
		log.Printf("adult newest entity-scenes: catalog enrichment failed (keeping raw items): %v", err)
		return
	}
	for ti, m := range matches {
		if m == nil {
			continue
		}
		applyCatalogEnrichment(&items[itemIdx[ti]], m)
	}
}

// applyCatalogEnrichment applies the §6 MatchResult → AdultDiscoverItem field
// mapping for one confident match: Title overwrite-on-match; Image/Studio/Date/
// Genres/Performers/DurationSeconds populate-if-empty; Genres/Performers split
// on a LITERAL comma (not comma-space) and trimmed; Rating and Source left
// untouched; the four grab-bearing fields never written.
func applyCatalogEnrichment(it *apidto.AdultDiscoverItem, m *identify.MatchResult) {
	if m.Title != "" {
		it.Title = m.Title
	}
	if it.Image == "" && m.Image != "" {
		it.Image = m.Image
	}
	if it.Studio == "" && m.Studio != "" {
		it.Studio = m.Studio
	}
	if it.Date == "" && m.Date != "" {
		it.Date = m.Date
	}
	if len(it.Genres) == 0 {
		if g := splitCommaTrim(m.Tags); len(g) > 0 {
			it.Genres = g
		}
	}
	if len(it.Performers) == 0 {
		if p := splitCommaTrim(m.Performers); len(p) > 0 {
			it.Performers = p
		}
	}
	if it.DurationSeconds == 0 && m.RuntimeSeconds > 0 {
		it.DurationSeconds = m.RuntimeSeconds
	}
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
// items whose Title contains name case-insensitively, and maps them to
// apidto.AdultDiscoverItem (Source "rss"). It is best-effort by contract:
// listing or fetching a feed can fail and is logged + dropped, never returned
// as an error — the caller must always get the Prowlarr results regardless.
//
// After the per-feed-matched slice is built, every item is additionally joined
// against the Adult identify pipeline's matched-entity pool
// (adult_newest_releases) via enrichAdultRSSItemsFromPool — see that function's
// doc comment for the join mechanics and the (deliberate) difference from
// resolveRssFeedHandler's equivalent join in rss_feeds.go.
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

	enrichAdultRSSItemsFromPool(ctx, releaseStore, out)

	return out
}

// enrichAdultRSSItemsFromPool joins items against the Adult identify
// pipeline's matched-entity pool (adult_newest_releases) by DownloadURL — the
// same feed_item_key the background RSS scan cycle stores for a matched
// release (scan.go's feedItemKey, EnclosureURL-then-Link, the same fallback
// fetchAdultRSSItems now computes above). A matched item gets Title
// (overwritten), Studio, and Image populated from the pool row's
// EntityTitle/EntityStudio/EntityImage — unconditionally, since this is an
// exact-key match, not a fuzzy one, so no confidence threshold applies.
//
// UNLIKE resolveRssFeedHandler's equivalent join (rss_feeds.go), an item with
// no pool match is never dropped: this drill-down's job is to show every
// currently-available release for the entity, including ones too new for the
// pool yet, so those simply keep today's raw-title/no-image fallback.
//
// Best-effort: a lookup error is logged and every item passes through
// raw/unenriched, matching this handler's existing non-blocking contract —
// it never fails the caller. ReleaseTitle/DownloadURL/Protocol/SizeBytes are
// never touched here — only Title/Studio/Image (grab-safety).
func enrichAdultRSSItemsFromPool(ctx context.Context, releaseStore *adultnewest.ReleaseStore, items []apidto.AdultDiscoverItem) {
	if releaseStore == nil || len(items) == 0 {
		return
	}
	keys := make([]string, 0, len(items))
	for _, it := range items {
		if it.DownloadURL != "" {
			keys = append(keys, it.DownloadURL)
		}
	}
	if len(keys) == 0 {
		return
	}
	matches, err := releaseStore.ByFeedItemKeys(ctx, keys)
	if err != nil {
		// Best-effort enrichment: a transient pool-join hiccup shouldn't fail
		// the drill-down, so log and leave every item raw/unenriched rather
		// than propagating the error.
		log.Printf("adult newest entity-scenes: enriching RSS items against adult pool: %v", err)
		return
	}
	for i := range items {
		m, ok := matches[items[i].DownloadURL]
		if !ok {
			continue
		}
		items[i].Title = m.EntityTitle
		items[i].Studio = m.EntityStudio
		items[i].Image = m.EntityImage
	}
}

// dedupeAdultDiscoverItems removes duplicates keyed by DownloadURL, falling
// back to the normalized Title when a DownloadURL is somehow empty. First
// occurrence wins, and the caller lists Prowlarr items before RSS ones, so a
// release seen in both sources keeps its Prowlarr provenance. Always returns a
// non-nil slice so an empty result encodes as a flat [] JSON array, never null
// — the shape the existing scene-list drill-down UI shell consumes (F7).
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
