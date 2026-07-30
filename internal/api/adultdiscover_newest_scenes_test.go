package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/labbersanon/sakms/internal/apidto"
	"github.com/labbersanon/sakms/internal/rssfeeds"
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
