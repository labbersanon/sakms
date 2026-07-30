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

	"github.com/labbersanon/sakms/internal/apidto"
	"github.com/labbersanon/sakms/internal/connections"
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
// Results map to a flat []apidto.AdultDiscoverItem with Image/Studio/Date/
// Rating left empty: the honest, documented consequence of a live-only source,
// identical to how the manual Search screen already presents raw releases.
//
// settingsStore is threaded through because mode.Build requires it (KidsRoot/AI
// wiring) — it has no settingsStore-free variant. It (and rssFeedsStore) are
// already NewMux params, so this deviation from the plan's 3-param sketch adds
// no NewMux-signature churn.
func adultNewestEntityScenesHandler(httpClient *http.Client, connStore *connections.Store, settingsStore *settings.Store, rssFeedsStore *rssfeeds.Store) http.HandlerFunc {
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
		items = append(items, fetchAdultRSSItems(tctx, httpClient, rssFeedsStore, name)...)

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(dedupeAdultDiscoverItems(items))
	}
}

// fetchAdultRSSItems fetches every enabled Adult-target RSS feed concurrently
// (bounded to rssFeedConcurrency) with a tight per-feed timeout, keeping only
// items whose Title contains name case-insensitively, and maps them to
// apidto.AdultDiscoverItem (Source "rss"). It is best-effort by contract:
// listing or fetching a feed can fail and is logged + dropped, never returned
// as an error — the caller must always get the Prowlarr results regardless.
func fetchAdultRSSItems(ctx context.Context, httpClient *http.Client, rssFeedsStore *rssfeeds.Store, name string) []apidto.AdultDiscoverItem {
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
				matched = append(matched, apidto.AdultDiscoverItem{
					Title:        it.Title,
					ReleaseTitle: it.Title,
					DownloadURL:  it.EnclosureURL,
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
	return out
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
