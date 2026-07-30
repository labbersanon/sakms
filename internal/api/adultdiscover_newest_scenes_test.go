package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
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

// newestScenesMuxWithReleaseStore is newestScenesMux's sibling for the tests
// that seed the pool / release-level cache: it also returns the real
// *adultnewest.ReleaseStore backing the mux, so a test can Insert a pool row (or
// read the scene-match cache) before/after firing the request.
func newestScenesMuxWithReleaseStore(t *testing.T) (*httptest.Server, *connectionsUpserter, *adultnewest.ReleaseStore) {
	t.Helper()
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, testFeedHealth(), rssFeedsStore, nil, nil, nil, nil, nil, nil))
	t.Cleanup(srv.Close)
	return srv, &connectionsUpserter{connStore: connStore, rssFeedsStore: rssFeedsStore}, adultNewestReleaseStore
}

// brokenReleaseStore returns an *adultnewest.ReleaseStore wrapping an already-
// CLOSED *sql.DB, so every query it runs fails — the pool-lookup-error test's
// way of injecting a failing store without a mock/interface (ReleaseStore has
// none; this codebase's house pattern is a concrete *Client/*Store over a real
// backend, see CLAUDE.md).
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
	connStore interface {
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

// getNewestScenes fires the drill-down for one page and decodes the
// {items, hasMore} envelope. page<1 means "omit the param" (exercise the
// default-to-1 path).
func getNewestScenes(t *testing.T, base, kind, name string, page int) (*http.Response, apidto.AdultNewestScenesPage) {
	t.Helper()
	u := base + "/api/modes/adult/discover/newest/entity-scenes?kind=" + url.QueryEscape(kind) + "&name=" + url.QueryEscape(name)
	if page >= 1 {
		u += "&page=" + strconv.Itoa(page)
	}
	resp, err := http.Get(u)
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		return resp, apidto.AdultNewestScenesPage{}
	}
	var out apidto.AdultNewestScenesPage
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		resp.Body.Close()
		t.Fatalf("decoding response: %v", err)
	}
	resp.Body.Close()
	return resp, out
}

// seedStudioPoolBox inserts a Studio pool row so EntityBox(name) resolves to
// entity_source — the "known box" the page>1 enrichment resolves against. Mirrors
// scan.go's Studio row shape (entity_id == entity_title == the name).
func seedStudioPoolBox(t *testing.T, store *adultnewest.ReleaseStore, name, source string) {
	t.Helper()
	if err := store.Insert(context.Background(), adultnewest.MatchedRelease{
		RowType:         adultnewest.RowStudio,
		EntityID:        name,
		EntitySource:    source,
		EntityTitle:     name,
		BrowseConfirmed: true,
	}); err != nil {
		t.Fatalf("seeding studio pool box: %v", err)
	}
}

// --- page=1: pool-only, zero Prowlarr ---------------------------------------

// TestAdultNewestEntityScenesHandler_Page1NoProwlarr is the load-bearing
// property of the reworked initial open: a plain page=1 request fires ZERO
// Prowlarr searches, proven against an httptest Prowlarr counting calls.
func TestAdultNewestEntityScenesHandler_Page1NoProwlarr(t *testing.T) {
	var calls int32
	prowlarr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[{"guid":"1","title":"Some.Scene-GRP","protocol":"torrent","size":1,"downloadUrl":"magnet:x"}]`))
	}))
	defer prowlarr.Close()

	srv, seed := newestScenesMux(t)
	seed.setProwlarr(t, prowlarr.URL)

	resp, page := getNewestScenes(t, srv.URL, "performer", "Some Performer", 0)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if got := atomic.LoadInt32(&calls); got != 0 {
		t.Fatalf("expected ZERO Prowlarr calls on page=1, got %d", got)
	}
	if !page.HasMore {
		t.Errorf("expected page=1 HasMore=true (Show More always offered), got false")
	}
	if len(page.Items) != 0 {
		t.Errorf("expected no items (no pool-matched RSS), got %d", len(page.Items))
	}
}

// TestAdultNewestEntityScenesHandler_Page1DropsUnmatchedRSS proves page=1 shows
// ONLY pool-matched RSS items — an item with no pool row is dropped entirely,
// never shown raw (resolveRssFeedHandler's drop-unmatched precedent).
func TestAdultNewestEntityScenesHandler_Page1DropsUnmatchedRSS(t *testing.T) {
	feed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		w.Write([]byte(`<?xml version="1.0"?><rss version="2.0"><channel>` +
			`<item><title>Nova Pool Hit Scene</title><enclosure url="http://feed.invalid/hit.torrent" length="500"/></item>` +
			`<item><title>Nova Pool Miss Scene</title><enclosure url="http://feed.invalid/miss.torrent" length="600"/></item>` +
			`</channel></rss>`))
	}))
	defer feed.Close()

	srv, seed, releaseStore := newestScenesMuxWithReleaseStore(t)
	seed.addAdultFeed(t, "adult feed", feed.URL)

	if err := releaseStore.Insert(context.Background(), adultnewest.MatchedRelease{
		RowType:         adultnewest.RowScene,
		EntityID:        "scene-nova-1",
		EntitySource:    "tpdb",
		EntityTitle:     "Resolved Nova Title",
		EntityStudio:    "Resolved Nova Studio",
		EntityImage:     "https://cdn/nova.jpg",
		BrowseConfirmed: true,
		FeedItemKey:     "http://feed.invalid/hit.torrent",
	}); err != nil {
		t.Fatalf("seeding pool: %v", err)
	}

	resp, page := getNewestScenes(t, srv.URL, "performer", "Nova", 1)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if len(page.Items) != 1 {
		t.Fatalf("expected only the 1 pool-matched item (miss dropped), got %d (%+v)", len(page.Items), page.Items)
	}
	it := page.Items[0]
	if it.DownloadURL != "http://feed.invalid/hit.torrent" {
		t.Errorf("expected the pool-hit item, got %+v", it)
	}
	if it.Title != "Resolved Nova Title" || it.Studio != "Resolved Nova Studio" || it.Image != "https://cdn/nova.jpg" {
		t.Errorf("expected pool-matched enrichment, got %+v", it)
	}
	// Grab-safety: enrichment must never touch the grab-bearing fields.
	if it.ReleaseTitle != "Nova Pool Hit Scene" || it.SizeBytes != 500 || it.Protocol != "torrent" {
		t.Errorf("expected grab-bearing fields unchanged (raw RSS), got %+v", it)
	}
}

// TestAdultNewestEntityScenesHandler_Page1RSSFailureNeverFails asserts a failing
// RSS feed is logged and dropped while page=1 still 200s (empty, HasMore true) —
// RSS failure never fails the handler.
func TestAdultNewestEntityScenesHandler_Page1RSSFailureNeverFails(t *testing.T) {
	badFeed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "feed exploded", http.StatusInternalServerError)
	}))
	defer badFeed.Close()

	srv, seed := newestScenesMux(t)
	seed.addAdultFeed(t, "broken adult feed", badFeed.URL)

	resp, page := getNewestScenes(t, srv.URL, "performer", "Working", 1)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 despite the failing feed, got %d", resp.StatusCode)
	}
	if len(page.Items) != 0 || !page.HasMore {
		t.Fatalf("expected empty items + HasMore true, got %+v hasMore=%v", page.Items, page.HasMore)
	}
}

// TestAdultNewestEntityScenesHandler_Page1PoolLookupErrorKeepsRaw asserts a
// failing pool lookup falls back to returning the raw RSS items unfiltered
// (best-effort, mirroring resolveRssFeedHandler) rather than blanking the view.
func TestAdultNewestEntityScenesHandler_Page1PoolLookupErrorKeepsRaw(t *testing.T) {
	feed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		w.Write([]byte(`<?xml version="1.0"?><rss version="2.0"><channel>` +
			`<item><title>Nova One</title><enclosure url="http://feed.invalid/1.torrent" length="1"/></item>` +
			`<item><title>Nova Two</title><enclosure url="http://feed.invalid/2.torrent" length="2"/></item>` +
			`</channel></rss>`))
	}))
	defer feed.Close()

	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, _, rssFeedsStore := testStores(t)
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, brokenReleaseStore(t), testFeedHealth(), rssFeedsStore, nil, nil, nil, nil, nil, nil))
	t.Cleanup(srv.Close)
	seed := &connectionsUpserter{connStore: connStore, rssFeedsStore: rssFeedsStore}
	seed.addAdultFeed(t, "adult feed", feed.URL)

	resp, page := getNewestScenes(t, srv.URL, "performer", "Nova", 1)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 despite the failing pool lookup, got %d", resp.StatusCode)
	}
	if len(page.Items) != 2 {
		t.Fatalf("expected both raw RSS items kept on a pool-lookup error, got %d (%+v)", len(page.Items), page.Items)
	}
	for _, it := range page.Items {
		if it.Studio != "" || it.Image != "" {
			t.Errorf("expected raw/unenriched item on lookup error, got %+v", it)
		}
	}
}

// --- generic validation -----------------------------------------------------

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

// TestAdultNewestEntityScenesHandler_Page2ProwlarrNotConfigured asserts the
// configured-service error shape (400) when a Show More (page>1) hits an
// unconfigured Prowlarr. page=1 never needs Prowlarr, so the check moved to
// page>1.
func TestAdultNewestEntityScenesHandler_Page2ProwlarrNotConfigured(t *testing.T) {
	srv, _ := newestScenesMux(t) // no prowlarr connection seeded
	resp, err := http.Get(srv.URL + "/api/modes/adult/discover/newest/entity-scenes?kind=studio&name=Brazzers&page=2")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 when prowlarr is unconfigured, got %d", resp.StatusCode)
	}
}

// TestAdultNewestEntityScenesHandler_Page2ProwlarrError502 asserts a Prowlarr
// error on page>1 surfaces as 502.
func TestAdultNewestEntityScenesHandler_Page2ProwlarrError502(t *testing.T) {
	prowlarr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer prowlarr.Close()

	srv, seed := newestScenesMux(t)
	seed.setProwlarr(t, prowlarr.URL)

	resp, err := http.Get(srv.URL + "/api/modes/adult/discover/newest/entity-scenes?kind=performer&name=Anyone&page=2")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("expected 502 on Prowlarr error, got %d", resp.StatusCode)
	}
}

// TestAdultNewestEntityScenesHandler_Page2NeverHangs proves that against a
// Prowlarr that never responds, page>1 still returns within the outboundTimeout
// bound (proven by shrinking outboundTimeout, not by waiting the production 15s).
func TestAdultNewestEntityScenesHandler_Page2NeverHangs(t *testing.T) {
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
	resp, err := http.Get(srv.URL + "/api/modes/adult/discover/newest/entity-scenes?kind=performer&name=Ghost&page=2")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	elapsed := time.Since(start)

	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("expected 502 once the outboundTimeout fires, got %d", resp.StatusCode)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("handler did not return within the outboundTimeout bound: took %s", elapsed)
	}
}

// --- page>1: Prowlarr + cache-checked enrichment ----------------------------

// tpdbStudioCatalogFake routes the TPDB REST endpoints the page>1 enrichment
// uses for a studio drill: SearchSites (resolve, exact-name) then ScenesBySite
// (catalog). sitesCount/scenesCount (nil-safe) tally the resolve + catalog
// fetches so a test can prove the cache short-circuit (a second page>1 makes
// zero box calls). sceneByID counts any GET /scenes/{id} (must stay 0).
func tpdbStudioCatalogFake(sitesCount, scenesCount, sceneByID *int32) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasPrefix(p, "/sites/") && strings.HasSuffix(p, "/scenes"):
			if scenesCount != nil {
				atomic.AddInt32(scenesCount, 1)
			}
			w.Write([]byte(`{"data":[{"_id":"tsc1","title":"Some Clean Scene Title","date":"2023-05-01","site":{"name":"Brazzers"},"image":"http://cdn/tsc1.jpg","duration":1500,"tags":[{"id":1,"name":"Anal"},{"id":2,"name":"Blonde"}],"performers":[{"name":"Riley Reid"},{"name":"Jane Doe"}]}]}`))
		case strings.HasPrefix(p, "/scenes/"):
			if sceneByID != nil {
				atomic.AddInt32(sceneByID, 1)
			}
			w.Write([]byte(`{"data":{}}`))
		case p == "/sites":
			if sitesCount != nil {
				atomic.AddInt32(sitesCount, 1)
			}
			w.Write([]byte(`{"data":[{"uuid":"site1","name":"Brazzers"}]}`))
		default:
			w.Write([]byte(`{"data":[]}`))
		}
	}
}

// TestAdultNewestEntityScenesHandler_Page2MappingAndGrabSafety is the core
// page>1 property: a Prowlarr-sourced raw release whose scene is in the entity's
// TPDB catalog gets its display Title OVERWRITTEN with the clean scene title and
// Image/Studio/Date/DurationSeconds/Genres/Performers populated, while Rating
// stays 0, Source stays "prowlarr", and the four grab-bearing fields are
// untouched. Exactly one Prowlarr call; HasMore=false. resolveTPDBDuration must
// never fire.
func TestAdultNewestEntityScenesHandler_Page2MappingAndGrabSafety(t *testing.T) {
	origTPDB := tpdbrest.DefaultBaseURL
	defer func() { tpdbrest.DefaultBaseURL = origTPDB }()

	var sceneByID, prowlarrCalls int32
	tpdb := httptest.NewServer(tpdbStudioCatalogFake(nil, nil, &sceneByID))
	defer tpdb.Close()
	tpdbrest.DefaultBaseURL = tpdb.URL

	const rawTitle = "Brazzers.Some.Clean.Scene.Title.XXX.1080p-GROUP"
	prowlarr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&prowlarrCalls, 1)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[{"guid":"1","title":"` + rawTitle + `","protocol":"torrent","size":900000000,"downloadUrl":"magnet:?xt=urn:btih:GRAB"}]`))
	}))
	defer prowlarr.Close()

	srv, seed, releaseStore := newestScenesMuxWithReleaseStore(t)
	seed.setProwlarr(t, prowlarr.URL)
	seed.setTPDB(t)
	seedStudioPoolBox(t, releaseStore, "Brazzers", "tpdb")

	resp, page := getNewestScenes(t, srv.URL, "studio", "Brazzers", 2)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if got := atomic.LoadInt32(&prowlarrCalls); got != 1 {
		t.Fatalf("expected EXACTLY ONE Prowlarr.Search, got %d", got)
	}
	if page.HasMore {
		t.Errorf("expected page=2 HasMore=false, got true")
	}
	if len(page.Items) != 1 {
		t.Fatalf("expected 1 enriched item, got %d (%+v)", len(page.Items), page.Items)
	}
	it := page.Items[0]
	if it.Title != "Some Clean Scene Title" {
		t.Errorf("expected display Title OVERWRITTEN with the clean catalog title, got %q", it.Title)
	}
	if it.Studio != "Brazzers" || it.Date != "2023-05-01" || it.Image != "http://cdn/tsc1.jpg" || it.DurationSeconds != 1500 {
		t.Errorf("expected Studio/Date/Image/DurationSeconds populated, got %+v", it)
	}
	if len(it.Genres) != 2 || it.Genres[0] != "Anal" || it.Genres[1] != "Blonde" {
		t.Errorf("expected Genres split + trimmed, got %#v", it.Genres)
	}
	if len(it.Performers) != 2 || it.Performers[0] != "Riley Reid" || it.Performers[1] != "Jane Doe" {
		t.Errorf("expected Performers split + trimmed, got %#v", it.Performers)
	}
	if it.Rating != 0 || it.Source != "prowlarr" {
		t.Errorf("expected Rating 0 + Source prowlarr, got rating=%v source=%q", it.Rating, it.Source)
	}
	if it.ReleaseTitle != rawTitle || it.DownloadURL != "magnet:?xt=urn:btih:GRAB" || it.Protocol != "torrent" || it.SizeBytes != 900000000 {
		t.Errorf("expected grab-bearing fields unchanged, got %+v", it)
	}
	if atomic.LoadInt32(&sceneByID) != 0 {
		t.Errorf("resolveTPDBDuration/GetSceneByID must never fire, got %d calls", sceneByID)
	}
}

// TestAdultNewestEntityScenesHandler_Page2CacheHitSkipsBoxFetch is US-2's cache
// property: a SECOND page>1 request for the same entity + same Prowlarr result
// hits the release-level cache for the already-matched release and does NOT
// re-run the box resolve/catalog fetch — proven with a call-counting TPDB fake
// across two sequential page>1 calls (catalog fetched once, not twice).
func TestAdultNewestEntityScenesHandler_Page2CacheHitSkipsBoxFetch(t *testing.T) {
	origTPDB := tpdbrest.DefaultBaseURL
	defer func() { tpdbrest.DefaultBaseURL = origTPDB }()

	var sitesCount, scenesCount int32
	tpdb := httptest.NewServer(tpdbStudioCatalogFake(&sitesCount, &scenesCount, nil))
	defer tpdb.Close()
	tpdbrest.DefaultBaseURL = tpdb.URL

	const rawTitle = "Brazzers.Some.Clean.Scene.Title.XXX-GRP"
	prowlarr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[{"guid":"1","title":"` + rawTitle + `","protocol":"torrent","size":100,"downloadUrl":"magnet:?xt=urn:btih:CACHE"}]`))
	}))
	defer prowlarr.Close()

	srv, seed, releaseStore := newestScenesMuxWithReleaseStore(t)
	seed.setProwlarr(t, prowlarr.URL)
	seed.setTPDB(t)
	seedStudioPoolBox(t, releaseStore, "Brazzers", "tpdb")

	// First page>1: cache miss → resolve + catalog fetch, match written to cache.
	resp1, page1 := getNewestScenes(t, srv.URL, "studio", "Brazzers", 2)
	if resp1.StatusCode != http.StatusOK {
		t.Fatalf("first page=2: expected 200, got %d", resp1.StatusCode)
	}
	if len(page1.Items) != 1 || page1.Items[0].Title != "Some Clean Scene Title" {
		t.Fatalf("first page=2: expected the enriched item, got %+v", page1.Items)
	}
	if got := atomic.LoadInt32(&scenesCount); got != 1 {
		t.Fatalf("first page=2: expected exactly 1 catalog fetch, got %d", got)
	}
	if got := atomic.LoadInt32(&sitesCount); got != 1 {
		t.Fatalf("first page=2: expected exactly 1 resolve, got %d", got)
	}

	// Second page>1, same entity + same Prowlarr result: the cache hit means the
	// box is NOT touched again.
	resp2, page2 := getNewestScenes(t, srv.URL, "studio", "Brazzers", 2)
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("second page=2: expected 200, got %d", resp2.StatusCode)
	}
	if len(page2.Items) != 1 || page2.Items[0].Title != "Some Clean Scene Title" {
		t.Fatalf("second page=2: expected the same enriched item from cache, got %+v", page2.Items)
	}
	// Grab-safety on the cache-hit path: the raw release title (the Grab query)
	// must survive a cache reconstruction untouched.
	if page2.Items[0].ReleaseTitle != rawTitle {
		t.Errorf("second page=2: expected ReleaseTitle unchanged on cache hit, got %q", page2.Items[0].ReleaseTitle)
	}
	if got := atomic.LoadInt32(&scenesCount); got != 1 {
		t.Errorf("second page=2: expected NO extra catalog fetch (cache hit), total got %d", got)
	}
	if got := atomic.LoadInt32(&sitesCount); got != 1 {
		t.Errorf("second page=2: expected NO extra resolve (cache hit), total got %d", got)
	}
}

// TestAdultNewestEntityScenesHandler_Page2NoPoolBoxDropsAll proves the graceful
// no-known-box path: with no pool row for the drilled entity, page>1 has no box
// to resolve against, so every Prowlarr item is dropped (empty, not an error).
func TestAdultNewestEntityScenesHandler_Page2NoPoolBoxDropsAll(t *testing.T) {
	prowlarr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[{"guid":"1","title":"Brazzers.Some.Scene.XXX-GRP","protocol":"torrent","size":100,"downloadUrl":"magnet:?xt=urn:btih:NOBOX"}]`))
	}))
	defer prowlarr.Close()

	// No pool row seeded → EntityBox returns "" → every miss dropped.
	srv, seed, _ := newestScenesMuxWithReleaseStore(t)
	seed.setProwlarr(t, prowlarr.URL)
	seed.setTPDB(t)

	resp, page := getNewestScenes(t, srv.URL, "studio", "Brazzers", 2)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if len(page.Items) != 0 {
		t.Fatalf("expected all items dropped with no known box, got %d (%+v)", len(page.Items), page.Items)
	}
	if page.HasMore {
		t.Errorf("expected HasMore=false on page=2, got true")
	}
}

// TestAdultNewestEntityScenesHandler_Page2UnmatchedDropped proves an unmatched
// Prowlarr item (its release title matches nothing in the box catalog) is
// dropped from the page>1 response — never shown raw.
func TestAdultNewestEntityScenesHandler_Page2UnmatchedDropped(t *testing.T) {
	origTPDB := tpdbrest.DefaultBaseURL
	defer func() { tpdbrest.DefaultBaseURL = origTPDB }()

	tpdb := httptest.NewServer(tpdbStudioCatalogFake(nil, nil, nil))
	defer tpdb.Close()
	tpdbrest.DefaultBaseURL = tpdb.URL

	prowlarr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Title shares nothing with the catalog's "Some Clean Scene Title".
		w.Write([]byte(`[{"guid":"1","title":"Totally.Unrelated.Nonsense.XXX-GRP","protocol":"torrent","size":100,"downloadUrl":"magnet:?xt=urn:btih:NOPE"}]`))
	}))
	defer prowlarr.Close()

	srv, seed, releaseStore := newestScenesMuxWithReleaseStore(t)
	seed.setProwlarr(t, prowlarr.URL)
	seed.setTPDB(t)
	seedStudioPoolBox(t, releaseStore, "Brazzers", "tpdb")

	resp, page := getNewestScenes(t, srv.URL, "studio", "Brazzers", 2)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if len(page.Items) != 0 {
		t.Fatalf("expected the unmatched item dropped, got %d (%+v)", len(page.Items), page.Items)
	}
}

// TestAdultNewestEntityScenesHandler_Page2BoxFailureDropsAll proves a box that
// errors on resolve never fails the handler: the item is simply dropped
// (unmatched), the response 200s empty.
func TestAdultNewestEntityScenesHandler_Page2BoxFailureDropsAll(t *testing.T) {
	origStash := stashbox.StashDBURL
	defer func() { stashbox.StashDBURL = origStash }()

	stash := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer stash.Close()
	stashbox.StashDBURL = stash.URL

	prowlarr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[{"guid":"1","title":"Brazzers.Some.Scene.XXX-GRP","protocol":"torrent","size":100,"downloadUrl":"magnet:?xt=urn:btih:RAW"}]`))
	}))
	defer prowlarr.Close()

	srv, seed, releaseStore := newestScenesMuxWithReleaseStore(t)
	seed.setProwlarr(t, prowlarr.URL)
	seed.setStashdb(t)
	seedStudioPoolBox(t, releaseStore, "Brazzers", "stashdb")

	resp, page := getNewestScenes(t, srv.URL, "studio", "Brazzers", 2)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 despite the failing box, got %d", resp.StatusCode)
	}
	if len(page.Items) != 0 {
		t.Fatalf("expected the unmatched item dropped on box failure, got %+v", page.Items)
	}
}

// TestAdultNewestEntityScenesHandler_Page2TimeoutNeverFails proves the
// enrichment sub-timeout never hangs or fails the handler: against a box that
// never responds, with newestScenesEnrichTimeout shrunk, the handler still 200s
// promptly (the unmatched item dropped).
func TestAdultNewestEntityScenesHandler_Page2TimeoutNeverFails(t *testing.T) {
	origStash := stashbox.StashDBURL
	origTimeout := newestScenesEnrichTimeout
	defer func() {
		stashbox.StashDBURL = origStash
		newestScenesEnrichTimeout = origTimeout
	}()
	newestScenesEnrichTimeout = 150 * time.Millisecond

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

	prowlarr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[{"guid":"1","title":"Brazzers.Some.Scene.XXX-GRP","protocol":"torrent","size":100,"downloadUrl":"magnet:?xt=urn:btih:RAW"}]`))
	}))
	defer prowlarr.Close()

	srv, seed, releaseStore := newestScenesMuxWithReleaseStore(t)
	seed.setProwlarr(t, prowlarr.URL)
	seed.setStashdb(t)
	seedStudioPoolBox(t, releaseStore, "Brazzers", "stashdb")

	start := time.Now()
	resp, page := getNewestScenes(t, srv.URL, "studio", "Brazzers", 2)
	elapsed := time.Since(start)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 despite the hanging box, got %d", resp.StatusCode)
	}
	if len(page.Items) != 0 {
		t.Fatalf("expected the item dropped on enrichment timeout, got %+v", page.Items)
	}
	if elapsed > 3*time.Second {
		t.Fatalf("handler did not return promptly under the enrichment sub-timeout: took %s", elapsed)
	}
}
