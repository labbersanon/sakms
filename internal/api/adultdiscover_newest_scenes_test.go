package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/labbersanon/sakms/internal/adultnewest"
	"github.com/labbersanon/sakms/internal/apidto"
	"github.com/labbersanon/sakms/internal/db"
	"github.com/labbersanon/sakms/internal/rssfeeds"
	"github.com/labbersanon/sakms/internal/stashbox"
	"github.com/labbersanon/sakms/internal/tpdbrest"
)

// newestScenesMux is the shared NewMux boilerplate every test in this file
// needs — a real store stack plus the standard nil tails, mirroring
// discover_availability_test.go's construction exactly.
func newestScenesMux(t *testing.T) (*httptest.Server, *connectionsUpserter) {
	t.Helper()
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, testFeedHealth(), rssFeedsStore, nil, nil, nil, nil, nil, nil))
	t.Cleanup(srv.Close)
	return srv, &connectionsUpserter{connStore: connStore, rssFeedsStore: rssFeedsStore}
}

// newestScenesMuxWithReleaseStore is newestScenesMux's sibling for the Phase 1
// pool-join tests below: it also returns the real *adultnewest.ReleaseStore
// backing the mux, so a test can Insert a pool row into it before firing the
// request.
func newestScenesMuxWithReleaseStore(t *testing.T) (*httptest.Server, *connectionsUpserter, *adultnewest.ReleaseStore) {
	t.Helper()
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, testFeedHealth(), rssFeedsStore, nil, nil, nil, nil, nil, nil))
	t.Cleanup(srv.Close)
	return srv, &connectionsUpserter{connStore: connStore, rssFeedsStore: rssFeedsStore}, adultNewestReleaseStore
}

// brokenReleaseStore returns an *adultnewest.ReleaseStore wrapping an already-
// CLOSED *sql.DB, so every query it runs (ByFeedItemKeys included) fails —
// the pool-lookup-error test's way of injecting a failing store without a
// mock/interface (ReleaseStore has none; this codebase's house pattern is a
// concrete *Client/*Store over a real backend, see CLAUDE.md).
func brokenReleaseStore(t *testing.T) *adultnewest.ReleaseStore {
	t.Helper()
	sqlDB, err := db.Open(filepath.Join(t.TempDir(), "broken.db"))
	if err != nil {
		t.Fatalf("opening db: %v", err)
	}
	if err := sqlDB.Close(); err != nil {
		t.Fatalf("closing db: %v", err)
	}
	return adultnewest.NewReleaseStore(sqlDB)
}

// connectionsUpserter is a tiny test helper bundling the two stores the
// newest-scenes tests seed (a "prowlarr" connection and adult RSS feeds).
type connectionsUpserter struct {
	connStore     interface {
		Upsert(ctx context.Context, service, url, apiKey string) error
	}
	rssFeedsStore *rssfeeds.Store
}

func (u *connectionsUpserter) setProwlarr(t *testing.T, rawURL string) {
	t.Helper()
	if err := u.connStore.Upsert(context.Background(), "prowlarr", rawURL, "key"); err != nil {
		t.Fatalf("seeding prowlarr connection: %v", err)
	}
}

func (u *connectionsUpserter) setStashdb(t *testing.T) {
	t.Helper()
	// The endpoint is hardcoded (stashbox.URLForBox → StashDBURL, overridden to
	// a fake in-test); only presence + a key matter to buildIdentifier.
	if err := u.connStore.Upsert(context.Background(), "stashdb", "https://stashdb.invalid", "key"); err != nil {
		t.Fatalf("seeding stashdb connection: %v", err)
	}
}

func (u *connectionsUpserter) setTPDB(t *testing.T) {
	t.Helper()
	// The REST base is hardcoded (tpdbrest.DefaultBaseURL, overridden to a fake
	// in-test); only presence + a key matter to buildIdentifier.
	if err := u.connStore.Upsert(context.Background(), "tpdb", "https://tpdb.invalid", "key"); err != nil {
		t.Fatalf("seeding tpdb connection: %v", err)
	}
}

func (u *connectionsUpserter) addAdultFeed(t *testing.T, title, feedURL string) {
	t.Helper()
	if _, err := u.rssFeedsStore.Create(context.Background(), title, feedURL, rssfeeds.TargetAdult, rssfeeds.Torrent, true); err != nil {
		t.Fatalf("seeding adult RSS feed: %v", err)
	}
}

func getNewestScenes(t *testing.T, base, kind, name string) (*http.Response, []apidto.AdultDiscoverItem) {
	t.Helper()
	u := base + "/api/modes/adult/discover/newest/entity-scenes?kind=" + url.QueryEscape(kind) + "&name=" + url.QueryEscape(name)
	resp, err := http.Get(u)
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		return resp, nil
	}
	var out []apidto.AdultDiscoverItem
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		resp.Body.Close()
		t.Fatalf("decoding response: %v", err)
	}
	resp.Body.Close()
	return resp, out
}

// TestAdultNewestEntityScenesHandler_OneProwlarrCall is the load-bearing
// DELIBERATE-mode property: exactly ONE Prowlarr.Search per drill-down open,
// proven against an httptest Prowlarr counting calls via an atomic counter —
// not asserted by code reading. No RSS feeds are seeded, so the counter can
// only ever see the single Search.
func TestAdultNewestEntityScenesHandler_OneProwlarrCall(t *testing.T) {
	var calls int32
	prowlarr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[{"guid":"1","title":"Some.Performer.Scene.2023.1080p-GRP","indexer":"I","protocol":"torrent","size":900000000,"downloadUrl":"magnet:?xt=urn:btih:AAAA"}]`))
	}))
	defer prowlarr.Close()

	srv, seed := newestScenesMux(t)
	seed.setProwlarr(t, prowlarr.URL)

	resp, out := getNewestScenes(t, srv.URL, "performer", "Some Performer")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("expected EXACTLY ONE Prowlarr.Search call, got %d", got)
	}
	if len(out) != 1 {
		t.Fatalf("expected 1 mapped item, got %d (%+v)", len(out), out)
	}
	if out[0].Source != "prowlarr" {
		t.Errorf("expected Source=prowlarr, got %q", out[0].Source)
	}
	if out[0].Title != "Some.Performer.Scene.2023.1080p-GRP" || out[0].ReleaseTitle != out[0].Title {
		t.Errorf("expected Title and ReleaseTitle both = release title, got Title=%q ReleaseTitle=%q", out[0].Title, out[0].ReleaseTitle)
	}
	if out[0].DownloadURL == "" || out[0].Protocol != "torrent" || out[0].SizeBytes != 900000000 {
		t.Errorf("unexpected enclosure mapping: %+v", out[0])
	}
	// Live-only source consequence: metadata fields left at zero values.
	if out[0].Image != "" || out[0].Studio != "" || out[0].Date != "" || out[0].Rating != 0 {
		t.Errorf("expected Image/Studio/Date/Rating empty for a live-only source, got %+v", out[0])
	}
}

// TestAdultNewestEntityScenesHandler_MissingName asserts an empty name → 400.
func TestAdultNewestEntityScenesHandler_MissingName(t *testing.T) {
	srv, _ := newestScenesMux(t)
	resp, err := http.Get(srv.URL + "/api/modes/adult/discover/newest/entity-scenes?kind=performer&name=")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing name, got %d", resp.StatusCode)
	}
}

// TestAdultNewestEntityScenesHandler_ProwlarrNotConfigured asserts the
// standard configured-service error shape (400) when no prowlarr connection
// exists — same shape searchHandler/discoverAvailabilityHandler return.
func TestAdultNewestEntityScenesHandler_ProwlarrNotConfigured(t *testing.T) {
	srv, _ := newestScenesMux(t) // no prowlarr connection seeded
	resp, err := http.Get(srv.URL + "/api/modes/adult/discover/newest/entity-scenes?kind=studio&name=Brazzers")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 when prowlarr is unconfigured, got %d", resp.StatusCode)
	}
}

// TestAdultNewestEntityScenesHandler_ProwlarrError502 asserts a Prowlarr error
// surfaces as 502 (Search-screen error shape).
func TestAdultNewestEntityScenesHandler_ProwlarrError502(t *testing.T) {
	prowlarr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer prowlarr.Close()

	srv, seed := newestScenesMux(t)
	seed.setProwlarr(t, prowlarr.URL)

	resp, err := http.Get(srv.URL + "/api/modes/adult/discover/newest/entity-scenes?kind=performer&name=Anyone")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("expected 502 on Prowlarr error, got %d", resp.StatusCode)
	}
}

// TestAdultNewestEntityScenesHandler_NeverHangs is the second DELIBERATE-mode
// property: against a Prowlarr that never responds, the handler still returns
// within the outboundTimeout bound (proven by shrinking outboundTimeout, not by
// waiting the production 15s). The fake server blocks until its request context
// is cancelled, so httptest's Close() doesn't stall on cleanup.
func TestAdultNewestEntityScenesHandler_NeverHangs(t *testing.T) {
	orig := newestScenesOutboundTimeout
	newestScenesOutboundTimeout = 300 * time.Millisecond
	defer func() { newestScenesOutboundTimeout = orig }()

	prowlarr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done() // hang until the caller's (context) deadline cancels us
	}))
	defer prowlarr.Close()

	srv, seed := newestScenesMux(t)
	seed.setProwlarr(t, prowlarr.URL)

	start := time.Now()
	resp, err := http.Get(srv.URL + "/api/modes/adult/discover/newest/entity-scenes?kind=performer&name=Ghost")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	elapsed := time.Since(start)

	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("expected 502 once the outboundTimeout fires, got %d", resp.StatusCode)
	}
	// Below 2s isolates the 300ms context bound from testHTTPClient's own 5s
	// client-timeout fallback — so this passes ONLY when newestScenesOutboundTimeout
	// actually fired, not when a regressed ctx wiring falls back to the 5s cap.
	if elapsed > 2*time.Second {
		t.Fatalf("handler did not return within the outboundTimeout bound: took %s", elapsed)
	}
}

// TestAdultNewestEntityScenesHandler_RSSFailureDropped asserts a failing RSS
// feed is logged and dropped while the Prowlarr results still return — RSS
// failure never fails the handler.
func TestAdultNewestEntityScenesHandler_RSSFailureDropped(t *testing.T) {
	prowlarr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[{"guid":"1","title":"Working.Release-GRP","indexer":"I","protocol":"torrent","size":100,"downloadUrl":"magnet:?xt=urn:btih:OK"}]`))
	}))
	defer prowlarr.Close()

	badFeed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "feed exploded", http.StatusInternalServerError)
	}))
	defer badFeed.Close()

	srv, seed := newestScenesMux(t)
	seed.setProwlarr(t, prowlarr.URL)
	seed.addAdultFeed(t, "broken adult feed", badFeed.URL)

	resp, out := getNewestScenes(t, srv.URL, "performer", "Working")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 despite the failing feed, got %d", resp.StatusCode)
	}
	if len(out) != 1 || out[0].Source != "prowlarr" {
		t.Fatalf("expected the single Prowlarr result to survive the RSS failure, got %+v", out)
	}
}

// TestAdultNewestEntityScenesHandler_RSSMatchAndDedupe covers two properties at
// once: (1) a matching RSS item is included and mapped with Source=rss; (2) an
// RSS item sharing a Prowlarr release's DownloadURL is deduped away, Prowlarr
// winning the tie (listed first).
func TestAdultNewestEntityScenesHandler_RSSMatchAndDedupe(t *testing.T) {
	const sharedURL = "magnet:?xt=urn:btih:SHARED"
	prowlarr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[{"guid":"1","title":"Nova Scene A","indexer":"I","protocol":"torrent","size":100,"downloadUrl":"` + sharedURL + `"}]`))
	}))
	defer prowlarr.Close()

	feed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		w.Write([]byte(`<?xml version="1.0"?><rss version="2.0"><channel>` +
			// duplicate of the Prowlarr release (same enclosure URL) — must dedupe
			`<item><title>Nova duplicate</title><enclosure url="` + sharedURL + `" length="100"/></item>` +
			// a genuine RSS-only match on the name
			`<item><title>Nova Scene B RSS</title><enclosure url="http://feed.invalid/b.torrent" length="222"/></item>` +
			// a non-matching item that must be filtered out
			`<item><title>Unrelated Other</title><enclosure url="http://feed.invalid/x.torrent" length="333"/></item>` +
			`</channel></rss>`))
	}))
	defer feed.Close()

	srv, seed := newestScenesMux(t)
	seed.setProwlarr(t, prowlarr.URL)
	seed.addAdultFeed(t, "adult feed", feed.URL)

	resp, out := getNewestScenes(t, srv.URL, "performer", "Nova")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	// Expect exactly two items: the Prowlarr release (deduped against the RSS
	// duplicate) + the one RSS-only match. The non-matching item is filtered.
	if len(out) != 2 {
		t.Fatalf("expected 2 deduped items, got %d (%+v)", len(out), out)
	}
	if out[0].DownloadURL != sharedURL || out[0].Source != "prowlarr" {
		t.Errorf("expected the shared-URL item to keep Prowlarr provenance, got %+v", out[0])
	}
	if out[1].Source != "rss" || out[1].SizeBytes != 222 || out[1].Protocol != "torrent" {
		t.Errorf("expected the RSS-only match mapped with Source=rss, feed protocol, and its enclosure length, got %+v", out[1])
	}
}

// poolHitFeedXML is a two-item RSS body used by the Phase 1 pool-join tests:
// the first item's enclosure URL is the join key seeded into the pool; the
// second has no pool row and must keep today's raw fallback.
const poolHitFeedXML = `<?xml version="1.0"?>
<rss version="2.0"><channel>
<item>
  <title>Nova Pool Hit Scene</title>
  <link>https://example.com/details/hit</link>
  <enclosure url="http://feed.invalid/hit.torrent" length="500" type="application/x-bittorrent"/>
</item>
<item>
  <title>Nova Pool Miss Scene</title>
  <link>https://example.com/details/miss</link>
  <enclosure url="http://feed.invalid/miss.torrent" length="600" type="application/x-bittorrent"/>
</item>
</channel></rss>`

// TestAdultNewestEntityScenesHandler_RSSPoolHit is the Phase 1 pool-join's
// core property: an RSS item whose DownloadURL matches a row already in
// adult_newest_releases (the background scan cycle's matched-entity pool)
// gets Title/Studio/Image populated from that row, unconditionally — and the
// grab-bearing fields (ReleaseTitle/DownloadURL/Protocol/SizeBytes) are left
// exactly as the raw RSS item computed them.
func TestAdultNewestEntityScenesHandler_RSSPoolHit(t *testing.T) {
	prowlarr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[]`))
	}))
	defer prowlarr.Close()

	feed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		w.Write([]byte(poolHitFeedXML))
	}))
	defer feed.Close()

	srv, seed, releaseStore := newestScenesMuxWithReleaseStore(t)
	seed.setProwlarr(t, prowlarr.URL)
	seed.addAdultFeed(t, "adult feed", feed.URL)

	if err := releaseStore.Insert(context.Background(), adultnewest.MatchedRelease{
		RowType:         adultnewest.RowScene,
		EntityID:        "scene-nova-1",
		EntitySource:    "tpdb",
		EntityTitle:     "Resolved Nova Title",
		EntityStudio:    "Resolved Nova Studio",
		EntityImage:     "https://cdn.theporndb.net/nova.jpg",
		BrowseConfirmed: true,
		FeedItemKey:     "http://feed.invalid/hit.torrent",
	}); err != nil {
		t.Fatalf("seeding pool: %v", err)
	}

	resp, out := getNewestScenes(t, srv.URL, "performer", "Nova")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if len(out) != 2 {
		t.Fatalf("expected 2 items (hit + miss, neither dropped), got %d (%+v)", len(out), out)
	}

	var hit, miss *apidto.AdultDiscoverItem
	for i := range out {
		switch out[i].DownloadURL {
		case "http://feed.invalid/hit.torrent":
			hit = &out[i]
		case "http://feed.invalid/miss.torrent":
			miss = &out[i]
		}
	}
	if hit == nil || miss == nil {
		t.Fatalf("expected both the hit and miss items present, got %+v", out)
	}

	if hit.Title != "Resolved Nova Title" || hit.Studio != "Resolved Nova Studio" || hit.Image != "https://cdn.theporndb.net/nova.jpg" {
		t.Errorf("expected the pool-matched item enriched, got %+v", hit)
	}
	// Grab-safety: the pool join must never touch these fields — they must
	// still carry exactly the raw RSS item's values.
	if hit.ReleaseTitle != "Nova Pool Hit Scene" {
		t.Errorf("expected ReleaseTitle unchanged (raw RSS title), got %q", hit.ReleaseTitle)
	}
	if hit.DownloadURL != "http://feed.invalid/hit.torrent" {
		t.Errorf("expected DownloadURL unchanged, got %q", hit.DownloadURL)
	}
	if hit.Protocol != "torrent" {
		t.Errorf("expected Protocol unchanged (feed protocol), got %q", hit.Protocol)
	}
	if hit.SizeBytes != 500 {
		t.Errorf("expected SizeBytes unchanged (raw enclosure length), got %d", hit.SizeBytes)
	}
}

// TestAdultNewestEntityScenesHandler_RSSPoolMiss asserts an RSS item with no
// pool row keeps today's raw title/empty-image fallback AND is still present
// in the response — unlike resolveRssFeedHandler's equivalent join
// (rss_feeds.go), this drill-down must never drop an unmatched item.
func TestAdultNewestEntityScenesHandler_RSSPoolMiss(t *testing.T) {
	prowlarr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[]`))
	}))
	defer prowlarr.Close()

	feed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		w.Write([]byte(poolHitFeedXML))
	}))
	defer feed.Close()

	// No pool row seeded at all — both feed items must stay raw.
	srv, seed, _ := newestScenesMuxWithReleaseStore(t)
	seed.setProwlarr(t, prowlarr.URL)
	seed.addAdultFeed(t, "adult feed", feed.URL)

	resp, out := getNewestScenes(t, srv.URL, "performer", "Nova")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if len(out) != 2 {
		t.Fatalf("expected 2 items, neither dropped for lack of a pool match, got %d (%+v)", len(out), out)
	}
	for _, it := range out {
		if it.Studio != "" || it.Image != "" {
			t.Errorf("expected no pool row to leave Studio/Image empty, got %+v", it)
		}
		if it.DownloadURL == "http://feed.invalid/miss.torrent" && it.Title != "Nova Pool Miss Scene" {
			t.Errorf("expected the raw title preserved for the unmatched item, got %+v", it)
		}
	}
}

// TestAdultNewestEntityScenesHandler_RSSPoolLookupError asserts a failing
// pool lookup is logged and every RSS item passes through raw/unenriched —
// the handler still 200s with the Prowlarr results intact, matching this
// handler's existing non-blocking contract.
func TestAdultNewestEntityScenesHandler_RSSPoolLookupError(t *testing.T) {
	prowlarr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[{"guid":"1","title":"Nova Prowlarr Release","indexer":"I","protocol":"torrent","size":900,"downloadUrl":"magnet:?xt=urn:btih:PROWL"}]`))
	}))
	defer prowlarr.Close()

	feed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		w.Write([]byte(poolHitFeedXML))
	}))
	defer feed.Close()

	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, _, rssFeedsStore := testStores(t)
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, brokenReleaseStore(t), testFeedHealth(), rssFeedsStore, nil, nil, nil, nil, nil, nil))
	t.Cleanup(srv.Close)
	seed := &connectionsUpserter{connStore: connStore, rssFeedsStore: rssFeedsStore}
	seed.setProwlarr(t, prowlarr.URL)
	seed.addAdultFeed(t, "adult feed", feed.URL)

	resp, out := getNewestScenes(t, srv.URL, "performer", "Nova")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 despite the failing pool lookup, got %d", resp.StatusCode)
	}
	if len(out) != 3 {
		t.Fatalf("expected all 3 items (1 prowlarr + 2 rss) present and unenriched, got %d (%+v)", len(out), out)
	}
	for _, it := range out {
		if it.Source == "prowlarr" {
			if it.Title != "Nova Prowlarr Release" || it.DownloadURL != "magnet:?xt=urn:btih:PROWL" {
				t.Errorf("expected the Prowlarr result intact, got %+v", it)
			}
			continue
		}
		if it.Studio != "" || it.Image != "" {
			t.Errorf("expected every RSS item to pass through unenriched on a pool-lookup error, got %+v", it)
		}
		if it.DownloadURL == "http://feed.invalid/hit.torrent" && it.Title != "Nova Pool Hit Scene" {
			t.Errorf("expected the raw title preserved (pool lookup failed, no enrichment applied), got %+v", it)
		}
	}
}

// tpdbStudioCatalogFake routes the TPDB REST endpoints the Phase-2 enrichment
// uses for a studio drill: SearchSites (resolve) then ScenesBySite (catalog).
// It also counts any GET /scenes/{id} (resolveTPDBDuration's re-fetch) so a
// test can assert it never fires.
func tpdbStudioCatalogFake(sceneByID *int32) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasPrefix(p, "/sites/") && strings.HasSuffix(p, "/scenes"):
			w.Write([]byte(`{"data":[{"_id":"tsc1","title":"Some Clean Scene Title","date":"2023-05-01","site":{"name":"Brazzers"},"image":"http://cdn/tsc1.jpg","duration":1500,"tags":[{"id":1,"name":"Anal"},{"id":2,"name":"Blonde"}],"performers":[{"name":"Riley Reid"},{"name":"Jane Doe"}]}]}`))
		case strings.HasPrefix(p, "/scenes/"):
			atomic.AddInt32(sceneByID, 1)
			w.Write([]byte(`{"data":{}}`))
		case p == "/sites":
			w.Write([]byte(`{"data":[{"uuid":"site1","name":"Brazzers"}]}`))
		default:
			w.Write([]byte(`{"data":[]}`))
		}
	}
}

// TestAdultNewestEntityScenesHandler_Phase2MappingAndGrabSafety is the core
// Phase-2 property at the handler layer: a Prowlarr-sourced raw release whose
// scene is in the entity's TPDB catalog gets its display Title OVERWRITTEN with
// the clean scene title and Image/Studio/Date/DurationSeconds/Genres/Performers
// populated (Genres/Performers split on a literal comma, trimmed), while Rating
// stays 0, Source stays "prowlarr", and the four grab-bearing fields
// (ReleaseTitle/DownloadURL/Protocol/SizeBytes) are untouched — ReleaseTitle
// still the raw, non-empty release string. resolveTPDBDuration must never fire.
func TestAdultNewestEntityScenesHandler_Phase2MappingAndGrabSafety(t *testing.T) {
	origTPDB := tpdbrest.DefaultBaseURL
	defer func() { tpdbrest.DefaultBaseURL = origTPDB }()

	var sceneByID int32
	tpdb := httptest.NewServer(tpdbStudioCatalogFake(&sceneByID))
	defer tpdb.Close()
	tpdbrest.DefaultBaseURL = tpdb.URL

	const rawTitle = "Brazzers.Some.Clean.Scene.Title.XXX.1080p-GROUP"
	prowlarr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[{"guid":"1","title":"` + rawTitle + `","indexer":"I","protocol":"torrent","size":900000000,"downloadUrl":"magnet:?xt=urn:btih:GRAB"}]`))
	}))
	defer prowlarr.Close()

	srv, seed := newestScenesMux(t)
	seed.setProwlarr(t, prowlarr.URL)
	seed.setTPDB(t)

	resp, out := getNewestScenes(t, srv.URL, "studio", "Brazzers")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if len(out) != 1 {
		t.Fatalf("expected 1 item, got %d (%+v)", len(out), out)
	}
	it := out[0]

	if it.Title != "Some Clean Scene Title" {
		t.Errorf("expected display Title OVERWRITTEN with the clean catalog title, got %q", it.Title)
	}
	if it.Studio != "Brazzers" || it.Date != "2023-05-01" || it.Image != "http://cdn/tsc1.jpg" || it.DurationSeconds != 1500 {
		t.Errorf("expected Studio/Date/Image/DurationSeconds populated, got %+v", it)
	}
	if len(it.Genres) != 2 || it.Genres[0] != "Anal" || it.Genres[1] != "Blonde" {
		t.Errorf("expected Genres split on bare comma + trimmed, got %#v", it.Genres)
	}
	if len(it.Performers) != 2 || it.Performers[0] != "Riley Reid" || it.Performers[1] != "Jane Doe" {
		t.Errorf("expected Performers split on bare comma + trimmed, got %#v", it.Performers)
	}
	if it.Rating != 0 {
		t.Errorf("MatchResult has no Rating field — expected Rating stays 0, got %v", it.Rating)
	}
	if it.Source != "prowlarr" {
		t.Errorf("expected Source provenance unchanged (prowlarr), got %q", it.Source)
	}
	// Grab-safety: the four grab-bearing fields must be exactly the raw release's.
	if it.ReleaseTitle != rawTitle {
		t.Errorf("expected ReleaseTitle unchanged (raw, non-empty), got %q", it.ReleaseTitle)
	}
	if it.DownloadURL != "magnet:?xt=urn:btih:GRAB" || it.Protocol != "torrent" || it.SizeBytes != 900000000 {
		t.Errorf("expected grab-bearing enclosure fields unchanged, got %+v", it)
	}
	if atomic.LoadInt32(&sceneByID) != 0 {
		t.Errorf("resolveTPDBDuration/GetSceneByID must never fire, got %d /scenes/{id} calls", sceneByID)
	}
}

// TestAdultNewestEntityScenesHandler_Phase2StillOneProwlarrCall proves the
// carve-out holds with enrichment active: exactly one Prowlarr.Search even
// though the enrichment fires its own TPDB catalog calls.
func TestAdultNewestEntityScenesHandler_Phase2StillOneProwlarrCall(t *testing.T) {
	origTPDB := tpdbrest.DefaultBaseURL
	defer func() { tpdbrest.DefaultBaseURL = origTPDB }()

	var sceneByID int32
	tpdb := httptest.NewServer(tpdbStudioCatalogFake(&sceneByID))
	defer tpdb.Close()
	tpdbrest.DefaultBaseURL = tpdb.URL

	var prowlarrCalls int32
	prowlarr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&prowlarrCalls, 1)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[{"guid":"1","title":"Brazzers.Some.Clean.Scene.Title.XXX-GRP","indexer":"I","protocol":"torrent","size":100,"downloadUrl":"magnet:?xt=urn:btih:ONE"}]`))
	}))
	defer prowlarr.Close()

	srv, seed := newestScenesMux(t)
	seed.setProwlarr(t, prowlarr.URL)
	seed.setTPDB(t)

	resp, _ := getNewestScenes(t, srv.URL, "studio", "Brazzers")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if got := atomic.LoadInt32(&prowlarrCalls); got != 1 {
		t.Fatalf("expected EXACTLY ONE Prowlarr.Search with enrichment active, got %d", got)
	}
}

// TestAdultNewestEntityScenesHandler_Phase2FailureNeverFails proves a box that
// errors on enrichment never fails the handler: the Prowlarr result still
// returns, unmatched item at its raw fallback.
func TestAdultNewestEntityScenesHandler_Phase2FailureNeverFails(t *testing.T) {
	origStash := stashbox.StashDBURL
	defer func() { stashbox.StashDBURL = origStash }()

	stash := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer stash.Close()
	stashbox.StashDBURL = stash.URL

	const rawTitle = "Brazzers.Some.Scene.XXX-GRP"
	prowlarr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[{"guid":"1","title":"` + rawTitle + `","indexer":"I","protocol":"torrent","size":100,"downloadUrl":"magnet:?xt=urn:btih:RAW"}]`))
	}))
	defer prowlarr.Close()

	srv, seed := newestScenesMux(t)
	seed.setProwlarr(t, prowlarr.URL)
	seed.setStashdb(t)

	resp, out := getNewestScenes(t, srv.URL, "studio", "Brazzers")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 despite the failing enrichment box, got %d", resp.StatusCode)
	}
	if len(out) != 1 || out[0].Title != rawTitle || out[0].Image != "" {
		t.Fatalf("expected the raw item to survive unenriched, got %+v", out)
	}
}

// TestAdultNewestEntityScenesHandler_Phase2TimeoutNeverFails proves the
// enrichment sub-timeout never hangs or fails the handler: against a box that
// never responds, with newestScenesEnrichTimeout shrunk, the handler still 200s
// promptly with the raw fallback.
func TestAdultNewestEntityScenesHandler_Phase2TimeoutNeverFails(t *testing.T) {
	origStash := stashbox.StashDBURL
	origTimeout := newestScenesEnrichTimeout
	defer func() {
		stashbox.StashDBURL = origStash
		newestScenesEnrichTimeout = origTimeout
	}()
	newestScenesEnrichTimeout = 150 * time.Millisecond

	// The handler blocks until either its request context is cancelled (the
	// production path: the enrichment sub-timeout aborts the outbound stash
	// call at ~150ms) OR the test's cleanup channel closes. The channel is the
	// teardown safety net: a client-side context cancel aborts the outbound
	// request immediately, but the httptest server may not observe the closed
	// connection promptly, which would otherwise hang Close() on a leaked
	// handler goroutine. defer-LIFO closes blockCh before stash.Close() runs.
	blockCh := make(chan struct{})
	stash := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-blockCh:
		}
	}))
	defer stash.Close()
	defer close(blockCh)
	stashbox.StashDBURL = stash.URL

	const rawTitle = "Brazzers.Some.Scene.XXX-GRP"
	prowlarr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[{"guid":"1","title":"` + rawTitle + `","indexer":"I","protocol":"torrent","size":100,"downloadUrl":"magnet:?xt=urn:btih:RAW"}]`))
	}))
	defer prowlarr.Close()

	srv, seed := newestScenesMux(t)
	seed.setProwlarr(t, prowlarr.URL)
	seed.setStashdb(t)

	start := time.Now()
	resp, out := getNewestScenes(t, srv.URL, "studio", "Brazzers")
	elapsed := time.Since(start)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 despite the hanging enrichment box, got %d", resp.StatusCode)
	}
	if len(out) != 1 || out[0].Title != rawTitle {
		t.Fatalf("expected the raw item returned on enrichment timeout, got %+v", out)
	}
	if elapsed > 3*time.Second {
		t.Fatalf("handler did not return promptly under the enrichment sub-timeout: took %s", elapsed)
	}
}

// TestAdultNewestEntityScenesHandler_Phase2SkipsPhase1Enriched is US-2's
// scope invariant: Phase 2 runs ONLY on items Phase 1 left raw. A pool-hit
// item (Phase 1 set its Title = EntityTitle) must NOT be re-matched and
// clobbered by Phase 2 even when the entity's catalog contains a scene whose
// title matches the pool item's RAW release string — while a sibling Prowlarr
// item in the same response IS enriched (proving Phase 2 actually ran, so the
// exclusion is real, not vacuous).
func TestAdultNewestEntityScenesHandler_Phase2SkipsPhase1Enriched(t *testing.T) {
	origTPDB := tpdbrest.DefaultBaseURL
	defer func() { tpdbrest.DefaultBaseURL = origTPDB }()

	// TPDB catalog carries TWO scenes: one matching the Prowlarr sibling's raw
	// title, one matching the pool item's raw ReleaseTitle (the clobber bait).
	tpdb := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasPrefix(p, "/sites/") && strings.HasSuffix(p, "/scenes"):
			w.Write([]byte(`{"data":[` +
				`{"_id":"c1","title":"Some Clean Scene Title","site":{"name":"Brazzers"}},` +
				`{"_id":"c2","title":"Pool Raw Scene","site":{"name":"Brazzers"}}` +
				`]}`))
		case p == "/sites":
			w.Write([]byte(`{"data":[{"uuid":"site1","name":"Brazzers"}]}`))
		default:
			w.Write([]byte(`{"data":[]}`))
		}
	}))
	defer tpdb.Close()
	tpdbrest.DefaultBaseURL = tpdb.URL

	prowlarr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[{"guid":"1","title":"Brazzers.Some.Clean.Scene.Title.XXX-GRP","indexer":"I","protocol":"torrent","size":100,"downloadUrl":"magnet:?xt=urn:btih:PROWL"}]`))
	}))
	defer prowlarr.Close()

	// RSS item whose enclosure URL is the pool key; its raw title matches the
	// "Pool Raw Scene" catalog entry, so Phase 2 WOULD clobber it if it wrongly
	// ran on a Phase-1-enriched item.
	feed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		w.Write([]byte(`<?xml version="1.0"?><rss version="2.0"><channel>` +
			`<item><title>Brazzers Pool Raw Scene XXX GRP</title><enclosure url="http://feed.invalid/pool.torrent" length="222"/></item>` +
			`</channel></rss>`))
	}))
	defer feed.Close()

	srv, seed, releaseStore := newestScenesMuxWithReleaseStore(t)
	seed.setProwlarr(t, prowlarr.URL)
	seed.setTPDB(t)
	seed.addAdultFeed(t, "adult feed", feed.URL)

	if err := releaseStore.Insert(context.Background(), adultnewest.MatchedRelease{
		RowType:         adultnewest.RowScene,
		EntityID:        "pool-scene-1",
		EntitySource:    "tpdb",
		EntityTitle:     "Resolved Pool Title",
		EntityStudio:    "Resolved Pool Studio",
		EntityImage:     "https://cdn/pool.jpg",
		BrowseConfirmed: true,
		FeedItemKey:     "http://feed.invalid/pool.torrent",
	}); err != nil {
		t.Fatalf("seeding pool: %v", err)
	}

	resp, out := getNewestScenes(t, srv.URL, "studio", "Brazzers")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if len(out) != 2 {
		t.Fatalf("expected 2 items (1 prowlarr + 1 pool-hit rss), got %d (%+v)", len(out), out)
	}

	var poolItem, prowlItem *apidto.AdultDiscoverItem
	for i := range out {
		switch out[i].DownloadURL {
		case "http://feed.invalid/pool.torrent":
			poolItem = &out[i]
		case "magnet:?xt=urn:btih:PROWL":
			prowlItem = &out[i]
		}
	}
	if poolItem == nil || prowlItem == nil {
		t.Fatalf("expected both the pool-hit and Prowlarr items present, got %+v", out)
	}

	// The pool-hit item keeps its Phase-1 (exact-key, trustworthy) title — Phase 2
	// must NOT clobber it with the lower-confidence "Pool Raw Scene" catalog match.
	if poolItem.Title != "Resolved Pool Title" {
		t.Errorf("Phase 2 clobbered a Phase-1-enriched item: got Title=%q, want %q", poolItem.Title, "Resolved Pool Title")
	}
	// The Prowlarr sibling WAS enriched — proves Phase 2 genuinely ran, so the
	// pool item's exclusion is a real skip, not a vacuous no-op.
	if prowlItem.Title != "Some Clean Scene Title" {
		t.Errorf("expected the still-raw Prowlarr item enriched by Phase 2, got Title=%q", prowlItem.Title)
	}
}
