package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/labbersanon/sakms/internal/adultnewest"
	"github.com/labbersanon/sakms/internal/apidto"
	"github.com/labbersanon/sakms/internal/rssfeed"
	"github.com/labbersanon/sakms/internal/rssfeeds"
)

// rssURLPtr wraps a plaintext feed URL as the three-state *string the update DTO
// takes (non-nil, non-empty = set/replace).
func rssURLPtr(s string) *string { return &s }

// rssProtoPtr wraps a protocol string as the optional *string the create DTO
// takes (non-nil = use as-is, nil = auto-detect server-side).
func rssProtoPtr(s string) *string { return &s }

// TestRssFeedCRUD_EndToEnd exercises create/list/update/delete against the
// real HTTP handlers backed by a real migrated SQLite file.
func TestRssFeedCRUD_EndToEnd(t *testing.T) {
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, testFeedHealth(), rssFeedsStore, nil, nil, nil, nil, nil))
	defer srv.Close()

	createBody, _ := json.Marshal(apidto.RssFeedCreateRequest{
		Title: "NZBGeek Saved Search", FeedURL: "https://nzbgeek.info/rss?t=1", Target: "movie", Protocol: rssProtoPtr("usenet"), Enabled: true,
	})
	createResp, err := http.Post(srv.URL+"/api/discover/rss-feeds", "application/json", bytes.NewReader(createBody))
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	defer createResp.Body.Close()
	if createResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 from create, got %d", createResp.StatusCode)
	}
	var created apidto.RssFeed
	if err := json.NewDecoder(createResp.Body).Decode(&created); err != nil {
		t.Fatalf("decoding create response: %v", err)
	}
	if created.ID == 0 || created.Title != "NZBGeek Saved Search" || created.Target != "movie" || created.Protocol != "usenet" {
		t.Fatalf("unexpected created feed: %+v", created)
	}

	// It shows up in the list.
	listResp, err := http.Get(srv.URL + "/api/discover/rss-feeds")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer listResp.Body.Close()
	var list []apidto.RssFeed
	if err := json.NewDecoder(listResp.Body).Decode(&list); err != nil {
		t.Fatalf("decoding list: %v", err)
	}
	if len(list) != 1 || list[0].ID != created.ID {
		t.Fatalf("unexpected list: %+v", list)
	}

	// Update it.
	updateBody, _ := json.Marshal(apidto.RssFeedUpdateRequest{
		Title: "NZBGeek Saved Search (renamed)", FeedURL: rssURLPtr("https://nzbgeek.info/rss?t=2"), Target: "tv", Protocol: "torrent", Enabled: false,
	})
	updateReq, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/discover/rss-feeds/"+strconv.Itoa(created.ID), bytes.NewReader(updateBody))
	updateResp, err := http.DefaultClient.Do(updateReq)
	if err != nil {
		t.Fatalf("PUT failed: %v", err)
	}
	defer updateResp.Body.Close()
	if updateResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 from update, got %d", updateResp.StatusCode)
	}
	var updated apidto.RssFeed
	if err := json.NewDecoder(updateResp.Body).Decode(&updated); err != nil {
		t.Fatalf("decoding update response: %v", err)
	}
	if updated.Title != "NZBGeek Saved Search (renamed)" || updated.Target != "tv" || updated.Protocol != "torrent" || updated.Enabled {
		t.Fatalf("unexpected updated feed: %+v", updated)
	}

	// Delete it.
	delReq, _ := http.NewRequest(http.MethodDelete, srv.URL+"/api/discover/rss-feeds/"+strconv.Itoa(created.ID), nil)
	delResp, err := http.DefaultClient.Do(delReq)
	if err != nil {
		t.Fatalf("DELETE failed: %v", err)
	}
	defer delResp.Body.Close()
	if delResp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204 from delete, got %d", delResp.StatusCode)
	}

	finalListResp, err := http.Get(srv.URL + "/api/discover/rss-feeds")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer finalListResp.Body.Close()
	var finalList []apidto.RssFeed
	if err := json.NewDecoder(finalListResp.Body).Decode(&finalList); err != nil {
		t.Fatalf("decoding final list: %v", err)
	}
	if len(finalList) != 0 {
		t.Errorf("expected empty list after delete, got %+v", finalList)
	}
}

// TestCreateRssFeedHandler_RejectsInvalidTarget proves rssfeeds.Store's
// validation errors surface as 400s, not 500s.
func TestCreateRssFeedHandler_RejectsInvalidTarget(t *testing.T) {
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, testFeedHealth(), rssFeedsStore, nil, nil, nil, nil, nil))
	defer srv.Close()

	body, _ := json.Marshal(apidto.RssFeedCreateRequest{Title: "Bad", FeedURL: "https://example.com/rss", Target: "not-a-real-target", Protocol: rssProtoPtr("usenet")})
	resp, err := http.Post(srv.URL+"/api/discover/rss-feeds", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for an invalid target, got %d", resp.StatusCode)
	}
}

// TestReorderRssFeedsHandler_Reorders proves POST .../reorder actually
// changes SortOrder in the persisted list.
func TestReorderRssFeedsHandler_Reorders(t *testing.T) {
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	ctx := context.Background()
	first, err := rssFeedsStore.Create(ctx, "First", "https://example.com/a", "movie", "usenet", true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	second, err := rssFeedsStore.Create(ctx, "Second", "https://example.com/b", "tv", "torrent", true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, testFeedHealth(), rssFeedsStore, nil, nil, nil, nil, nil))
	defer srv.Close()

	body, _ := json.Marshal(apidto.RssFeedReorderRequest{IDs: []int{second.ID, first.ID}})
	resp, err := http.Post(srv.URL+"/api/discover/rss-feeds/reorder", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", resp.StatusCode)
	}

	list, err := rssFeedsStore.List(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(list) != 2 || list[0].ID != second.ID || list[1].ID != first.ID {
		t.Fatalf("unexpected order after reorder: %+v", list)
	}
}

// TestResolveRssFeedHandler_MapsItemsToDTO proves the resolve route fetches
// the feed's live items and maps DownloadURL/SizeBytes/Protocol/Indexer
// correctly, including the enclosure-missing fallback to Link.
func TestResolveRssFeedHandler_MapsItemsToDTO(t *testing.T) {
	fakeFeed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		w.Write([]byte(`<?xml version="1.0"?>
<rss version="2.0"><channel>
<item>
  <title>Some.Release.2026</title>
  <link>https://example.com/details/1</link>
  <pubDate>Wed, 15 Jul 2026 12:00:00 +0000</pubDate>
  <enclosure url="https://example.com/fetch/1.nzb" length="1024" type="application/x-nzb"/>
</item>
<item>
  <title>No.Enclosure.2026</title>
  <link>https://example.com/details/2</link>
  <pubDate>Wed, 15 Jul 2026 11:00:00 +0000</pubDate>
</item>
</channel></rss>`))
	}))
	defer fakeFeed.Close()

	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	f, err := rssFeedsStore.Create(context.Background(), "My Feed", fakeFeed.URL, "movie", "usenet", true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, testFeedHealth(), rssFeedsStore, nil, nil, nil, nil, nil))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/discover/rss-feeds/" + strconv.Itoa(f.ID) + "/resolve")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var items []apidto.RssFeedItem
	if err := json.NewDecoder(resp.Body).Decode(&items); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %+v", items)
	}
	if items[0].DownloadURL != "https://example.com/fetch/1.nzb" || items[0].SizeBytes != 1024 {
		t.Errorf("unexpected first item: %+v", items[0])
	}
	if items[0].Protocol != "usenet" || items[0].Indexer != "My Feed" {
		t.Errorf("expected protocol/indexer from feed config, got %+v", items[0])
	}
	if items[1].DownloadURL != "https://example.com/details/2" {
		t.Errorf("expected item with no enclosure to fall back to Link, got %+v", items[1])
	}
}

// adultEnrichFeedXML is a two-item RSS body: the first item's enclosure URL is
// the join key seeded into the pool below; the second has no enclosure (falls
// back to its link, which is deliberately NOT in the pool).
const adultEnrichFeedXML = `<?xml version="1.0"?>
<rss version="2.0"><channel>
<item>
  <title>Raw.Scene.Release.2026.XXX</title>
  <link>https://example.com/details/1</link>
  <pubDate>Wed, 15 Jul 2026 12:00:00 +0000</pubDate>
  <enclosure url="https://example.com/fetch/1.torrent" length="2048" type="application/x-bittorrent"/>
</item>
<item>
  <title>Unmatched.Release.2026.XXX</title>
  <link>https://example.com/details/2</link>
  <pubDate>Wed, 15 Jul 2026 11:00:00 +0000</pubDate>
</item>
</channel></rss>`

// matchedPoolRow is the seed whose FeedItemKey equals the first feed item's
// enclosure URL — the join target both the Adult (positive) and Movies
// (must-not-enrich) tests use, so the Movies test is genuinely discriminating:
// a matching row exists, and only the target guard keeps it from being applied.
func matchedPoolRow() adultnewest.MatchedRelease {
	return adultnewest.MatchedRelease{
		RowType:         adultnewest.RowScene,
		EntityID:        "scene-123",
		EntitySource:    "tpdb",
		EntityTitle:     "Resolved Scene Title",
		EntityStudio:    "Resolved Studio",
		EntityImage:     "https://cdn.theporndb.net/scene-123.jpg",
		BrowseConfirmed: true,
		FeedItemKey:     "https://example.com/fetch/1.torrent",
	}
}

// TestResolveRssFeedHandler_AdultEnrichesFromPool proves an Adult feed item whose
// enclosure key matches a pooled matched-entity gets the resolved
// title/studio/image populated, AND that an item with no pool match is filtered
// out of the response entirely (not returned unenriched) — the resolved Adult
// feed row surfaces only identified releases.
func TestResolveRssFeedHandler_AdultEnrichesFromPool(t *testing.T) {
	fakeFeed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		w.Write([]byte(adultEnrichFeedXML))
	}))
	defer fakeFeed.Close()

	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	ctx := context.Background()
	if err := adultNewestReleaseStore.Insert(ctx, matchedPoolRow()); err != nil {
		t.Fatalf("seeding pool: %v", err)
	}
	f, err := rssFeedsStore.Create(ctx, "My Adult Feed", fakeFeed.URL, "adult", "torrent", true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, testFeedHealth(), rssFeedsStore, nil, nil, nil, nil, nil))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/discover/rss-feeds/" + strconv.Itoa(f.ID) + "/resolve")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var items []apidto.RssFeedItem
	if err := json.NewDecoder(resp.Body).Decode(&items); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	// Only the matched item survives: the unmatched item is dropped, not returned
	// unenriched.
	if len(items) != 1 {
		t.Fatalf("expected 1 item (unmatched dropped), got %+v", items)
	}

	// Matched item: resolved fields populated; the RAW release title (used by the
	// grab) is left untouched.
	if items[0].ResolvedTitle != "Resolved Scene Title" || items[0].ResolvedStudio != "Resolved Studio" ||
		items[0].ResolvedImage != "https://cdn.theporndb.net/scene-123.jpg" {
		t.Errorf("expected matched item enriched, got %+v", items[0])
	}
	if items[0].Title != "Raw.Scene.Release.2026.XXX" || items[0].DownloadURL != "https://example.com/fetch/1.torrent" {
		t.Errorf("enrichment must not alter the raw grab fields, got %+v", items[0])
	}

	// The unmatched release must be absent from the response entirely — asserted by
	// identity, not merely by the count above, so this pins the new filter behavior.
	for _, it := range items {
		if it.Title == "Unmatched.Release.2026.XXX" || it.DownloadURL == "https://example.com/details/2" {
			t.Errorf("unmatched Adult item must be filtered out of the response, but found %+v", it)
		}
	}
}

// TestResolveRssFeedHandler_MoviesFeedNeverEnriched proves a Movies/Series feed
// is completely unaffected: even with a pooled row whose key matches the feed
// item's enclosure, no lookup is applied and no resolved field is ever populated
// (the Target guard, not the absence of a matching row, is what's tested).
func TestResolveRssFeedHandler_MoviesFeedNeverEnriched(t *testing.T) {
	fakeFeed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		w.Write([]byte(adultEnrichFeedXML))
	}))
	defer fakeFeed.Close()

	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	ctx := context.Background()
	if err := adultNewestReleaseStore.Insert(ctx, matchedPoolRow()); err != nil {
		t.Fatalf("seeding pool: %v", err)
	}
	f, err := rssFeedsStore.Create(ctx, "My Movies Feed", fakeFeed.URL, "movie", "torrent", true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, testFeedHealth(), rssFeedsStore, nil, nil, nil, nil, nil))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/discover/rss-feeds/" + strconv.Itoa(f.ID) + "/resolve")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var items []apidto.RssFeedItem
	if err := json.NewDecoder(resp.Body).Decode(&items); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %+v", items)
	}
	for i, it := range items {
		if it.ResolvedTitle != "" || it.ResolvedStudio != "" || it.ResolvedImage != "" {
			t.Errorf("Movies feed item %d must never be enriched, got %+v", i, it)
		}
	}
}

// TestResolveRssFeedHandler_UnknownIDReturns404 proves resolving a
// nonexistent feed id 404s instead of panicking or 500ing.
func TestResolveRssFeedHandler_UnknownIDReturns404(t *testing.T) {
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, testFeedHealth(), rssFeedsStore, nil, nil, nil, nil, nil))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/discover/rss-feeds/999/resolve")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 for an unknown feed id, got %d", resp.StatusCode)
	}
}

// --- Protocol auto-detection (detectProtocol + create/rescan handlers) ------

// torrentTypeFeedXML: a single item with an explicit application/x-bittorrent
// enclosure type — the confident-torrent-via-type case.
const torrentTypeFeedXML = `<?xml version="1.0"?>
<rss version="2.0"><channel>
<item><title>T.2026</title><link>https://ex.com/1</link>
<enclosure url="https://ex.com/1.torrent" length="1" type="application/x-bittorrent"/></item>
</channel></rss>`

// usenetTypeFeedXML: a single item with an explicit application/x-nzb enclosure
// type — the confident-usenet-via-type case.
const usenetTypeFeedXML = `<?xml version="1.0"?>
<rss version="2.0"><channel>
<item><title>N.2026</title><link>https://ex.com/1</link>
<enclosure url="https://ex.com/1.nzb" length="1" type="application/x-nzb"/></item>
</channel></rss>`

// inconclusiveFeedXML: a bare .torrent-over-https link with NO type attribute
// and NO magnet scheme — the case Option B degrades gracefully on (must fall
// through to the pop-up, not guess wrong).
const inconclusiveFeedXML = `<?xml version="1.0"?>
<rss version="2.0"><channel>
<item><title>Bare.2026</title><link>https://ex.com/1</link>
<enclosure url="https://ex.com/1.torrent" length="1"/></item>
</channel></rss>`

// fakeRSSFeed serves body as an RSS 2.0 document from a throwaway httptest
// server, cleaned up when the test ends.
func fakeRSSFeed(t *testing.T, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// newRssFeedTestServer wires the full API mux over fresh migrated stores and
// returns the server plus the RSS feed store for direct read-back assertions.
func newRssFeedTestServer(t *testing.T) (*httptest.Server, *rssfeeds.Store) {
	t.Helper()
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, testFeedHealth(), rssFeedsStore, nil, nil, nil, nil, nil))
	t.Cleanup(srv.Close)
	return srv, rssFeedsStore
}

// TestDetectProtocol covers every branch of the enclosure-type/URL-scheme
// heuristic, including the load-bearing inconclusive cases (a bare
// .torrent-over-https with no type attribute must NOT be guessed) and the
// tolerate-a-malformed-first-item sampling behavior.
func TestDetectProtocol(t *testing.T) {
	tests := []struct {
		name      string
		items     []rssfeed.Item
		want      rssfeeds.Protocol
		confident bool
	}{
		{
			name:      "confident torrent via type attr",
			items:     []rssfeed.Item{{EnclosureURL: "https://ex.com/1.torrent", EnclosureType: "application/x-bittorrent"}},
			want:      rssfeeds.Torrent,
			confident: true,
		},
		{
			name:      "confident torrent via magnet scheme fallback (no type)",
			items:     []rssfeed.Item{{EnclosureURL: "magnet:?xt=urn:btih:abc123"}},
			want:      rssfeeds.Torrent,
			confident: true,
		},
		{
			name:      "confident usenet via type attr",
			items:     []rssfeed.Item{{EnclosureURL: "https://ex.com/1.nzb", EnclosureType: "application/x-nzb"}},
			want:      rssfeeds.Usenet,
			confident: true,
		},
		{
			name:      "inconclusive: bare .torrent over https, no type, no magnet",
			items:     []rssfeed.Item{{EnclosureURL: "https://ex.com/1.torrent"}},
			confident: false,
		},
		{
			name:      "inconclusive: empty/unreachable feed",
			items:     nil,
			confident: false,
		},
		{
			name: "tolerates malformed first item, detects from a later one",
			items: []rssfeed.Item{
				{}, // no enclosure, no scheme signal — must not abort the whole scan
				{EnclosureURL: "https://ex.com/2.nzb", EnclosureType: "application/x-nzb"},
			},
			want:      rssfeeds.Usenet,
			confident: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, confident := detectProtocol(tt.items)
			if confident != tt.confident {
				t.Fatalf("confident = %v, want %v", confident, tt.confident)
			}
			if confident && got != tt.want {
				t.Fatalf("protocol = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestCreateRssFeedHandler_ExplicitProtocolSkipsDetection is the regression
// guard for the unchanged path: when a protocol is supplied it is used as-is and
// the feed is never fetched (proven by asserting the feed server was not hit).
func TestCreateRssFeedHandler_ExplicitProtocolSkipsDetection(t *testing.T) {
	var hit bool
	feed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit = true
		w.Write([]byte(usenetTypeFeedXML))
	}))
	defer feed.Close()

	srv, store := newRssFeedTestServer(t)
	body, _ := json.Marshal(apidto.RssFeedCreateRequest{
		Title: "Explicit", FeedURL: feed.URL, Target: "adult", Protocol: rssProtoPtr("torrent"), Enabled: true,
	})
	resp, err := http.Post(srv.URL+"/api/discover/rss-feeds", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var created apidto.RssFeed
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if created.Protocol != "torrent" {
		t.Fatalf("explicit protocol must win as-is, got %q", created.Protocol)
	}
	if hit {
		t.Fatalf("feed must NOT be fetched when protocol is supplied explicitly")
	}
	list, err := store.List(context.Background())
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(list) != 1 || list[0].Protocol != "torrent" {
		t.Fatalf("stored protocol must be the explicit value, got %+v", list)
	}
}

// TestCreateRssFeedHandler_AutoDetectsProtocol proves an omitted protocol
// triggers a fetch + confident detection, storing the detected value.
func TestCreateRssFeedHandler_AutoDetectsProtocol(t *testing.T) {
	feed := fakeRSSFeed(t, torrentTypeFeedXML)
	srv, store := newRssFeedTestServer(t)

	body, _ := json.Marshal(apidto.RssFeedCreateRequest{
		Title: "Auto", FeedURL: feed.URL, Target: "adult", Enabled: true, // Protocol nil → auto-detect
	})
	resp, err := http.Post(srv.URL+"/api/discover/rss-feeds", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 from confident auto-detect, got %d", resp.StatusCode)
	}
	var created apidto.RssFeed
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if created.Protocol != "torrent" {
		t.Fatalf("expected detected protocol torrent, got %q", created.Protocol)
	}
	list, err := store.List(context.Background())
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(list) != 1 || list[0].Protocol != "torrent" {
		t.Fatalf("stored protocol must be the detected value, got %+v", list)
	}
}

// TestCreateRssFeedHandler_InconclusiveReturns422 proves an omitted protocol
// with an inconclusive feed returns the protocol_undetected 422 and stores
// nothing.
func TestCreateRssFeedHandler_InconclusiveReturns422(t *testing.T) {
	feed := fakeRSSFeed(t, inconclusiveFeedXML)
	srv, store := newRssFeedTestServer(t)

	body, _ := json.Marshal(apidto.RssFeedCreateRequest{
		Title: "Undetectable", FeedURL: feed.URL, Target: "adult", Enabled: true, // Protocol nil
	})
	resp, err := http.Post(srv.URL+"/api/discover/rss-feeds", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422 for inconclusive detection, got %d", resp.StatusCode)
	}
	var undetected apidto.ProtocolUndetectedResponse
	if err := json.NewDecoder(resp.Body).Decode(&undetected); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if undetected.Error != "protocol_undetected" {
		t.Fatalf("expected protocol_undetected error body, got %q", undetected.Error)
	}
	list, err := store.List(context.Background())
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("no feed must be stored on inconclusive detection, got %+v", list)
	}
}

// TestRescanRssFeedHandler_Confident proves rescan overwrites only the stored
// protocol on a confident result, preserving title/feedURL/enabled/sortOrder.
func TestRescanRssFeedHandler_Confident(t *testing.T) {
	feed := fakeRSSFeed(t, torrentTypeFeedXML)
	srv, store := newRssFeedTestServer(t)
	ctx := context.Background()

	// Seed a feed with the WRONG protocol (usenet) pointing at a torrent feed.
	f, err := store.Create(ctx, "Rescan Me", feed.URL, "adult", "usenet", true)
	if err != nil {
		t.Fatalf("seeding feed: %v", err)
	}

	resp, err := http.Post(srv.URL+"/api/discover/rss-feeds/"+strconv.Itoa(f.ID)+"/rescan", "application/json", nil)
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 from confident rescan, got %d", resp.StatusCode)
	}
	var updated apidto.RssFeed
	if err := json.NewDecoder(resp.Body).Decode(&updated); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if updated.Protocol != "torrent" {
		t.Fatalf("expected rescanned protocol torrent, got %q", updated.Protocol)
	}

	list, err := store.List(ctx)
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 feed, got %+v", list)
	}
	row := list[0]
	if row.Protocol != "torrent" {
		t.Errorf("stored protocol must be updated to torrent, got %q", row.Protocol)
	}
	if row.Title != "Rescan Me" || row.FeedURL != feed.URL || !row.Enabled || row.SortOrder != f.SortOrder {
		t.Errorf("rescan must preserve title/feedURL/enabled/sortOrder, got %+v", row)
	}
}

// TestRescanRssFeedHandler_InconclusiveReturns422 proves rescan returns the
// shared 422 protocol_undetected shape and leaves the stored protocol unchanged
// when detection is inconclusive.
func TestRescanRssFeedHandler_InconclusiveReturns422(t *testing.T) {
	feed := fakeRSSFeed(t, inconclusiveFeedXML)
	srv, store := newRssFeedTestServer(t)
	ctx := context.Background()

	f, err := store.Create(ctx, "Keep My Protocol", feed.URL, "adult", "usenet", true)
	if err != nil {
		t.Fatalf("seeding feed: %v", err)
	}

	resp, err := http.Post(srv.URL+"/api/discover/rss-feeds/"+strconv.Itoa(f.ID)+"/rescan", "application/json", nil)
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422 for inconclusive rescan, got %d", resp.StatusCode)
	}
	var undetected apidto.ProtocolUndetectedResponse
	if err := json.NewDecoder(resp.Body).Decode(&undetected); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if undetected.Error != "protocol_undetected" {
		t.Fatalf("expected protocol_undetected, got %q", undetected.Error)
	}
	list, err := store.List(ctx)
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(list) != 1 || list[0].Protocol != "usenet" {
		t.Fatalf("inconclusive rescan must leave the stored protocol unchanged, got %+v", list)
	}
}

// TestUpdateRssFeedHandler_PreservesProtocolFeedURLEnabled is the plan's single
// most important regression test: an Edit that changes only title/target (with
// feedUrl omitted and protocol/enabled re-sent from the current row, exactly as
// the Edit form reconstructs the body) must leave the stored protocol, feed URL,
// and enabled state intact. feedUrl is asserted via a store read-back, because
// the HTTP response always masks it to "".
func TestUpdateRssFeedHandler_PreservesProtocolFeedURLEnabled(t *testing.T) {
	srv, store := newRssFeedTestServer(t)
	ctx := context.Background()

	const secretURL = "https://real.example/rss?apikey=SECRET"
	f, err := store.Create(ctx, "Original", secretURL, "movie", "usenet", true)
	if err != nil {
		t.Fatalf("seeding feed: %v", err)
	}

	// Edit title+target only; feedUrl nil (preserve), protocol/enabled re-sent as
	// the row's current values (what the Edit form reconstructs).
	updateBody, _ := json.Marshal(apidto.RssFeedUpdateRequest{
		Title: "Renamed", FeedURL: nil, Target: "tv", Protocol: "usenet", Enabled: true,
	})
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/discover/rss-feeds/"+strconv.Itoa(f.ID), bytes.NewReader(updateBody))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 from update, got %d", resp.StatusCode)
	}

	list, err := store.List(ctx)
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 feed, got %+v", list)
	}
	row := list[0]
	if row.FeedURL != secretURL {
		t.Errorf("feed URL must be preserved on an edit that omits feedUrl, got %q", row.FeedURL)
	}
	if row.Protocol != "usenet" {
		t.Errorf("protocol must be preserved, got %q", row.Protocol)
	}
	if !row.Enabled {
		t.Errorf("enabled must be preserved, got %v", row.Enabled)
	}
	if row.Title != "Renamed" || row.Target != "tv" {
		t.Errorf("title/target must be updated, got %+v", row)
	}
}

// TestRssFeed_MainstreamPayloadRegression proves the create/update DTO split did
// not change Mainstream's wire behavior: a create with an explicit protocol +
// string feedUrl + target movie, and a subsequent update, behave identically to
// before the split.
func TestRssFeed_MainstreamPayloadRegression(t *testing.T) {
	srv, store := newRssFeedTestServer(t)
	ctx := context.Background()

	const createURL = "https://nzbgeek.info/rss?movies"
	createBody, _ := json.Marshal(apidto.RssFeedCreateRequest{
		Title: "Mainstream Feed", FeedURL: createURL, Target: "movie", Protocol: rssProtoPtr("usenet"), Enabled: true,
	})
	createResp, err := http.Post(srv.URL+"/api/discover/rss-feeds", "application/json", bytes.NewReader(createBody))
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	defer createResp.Body.Close()
	if createResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 from Mainstream create, got %d", createResp.StatusCode)
	}
	var created apidto.RssFeed
	if err := json.NewDecoder(createResp.Body).Decode(&created); err != nil {
		t.Fatalf("decoding create: %v", err)
	}
	if created.Protocol != "usenet" || created.Target != "movie" || !created.Enabled {
		t.Fatalf("unexpected Mainstream create result: %+v", created)
	}
	list, err := store.List(ctx)
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(list) != 1 || list[0].FeedURL != createURL || list[0].Protocol != "usenet" {
		t.Fatalf("Mainstream create must store the exact URL/protocol, got %+v", list)
	}

	// Mainstream UPDATE via the (renamed, unchanged) update DTO.
	const updateURL = "https://nzbgeek.info/rss?tv"
	updateBody, _ := json.Marshal(apidto.RssFeedUpdateRequest{
		Title: "Mainstream Feed 2", FeedURL: rssURLPtr(updateURL), Target: "tv", Protocol: "torrent", Enabled: false,
	})
	updReq, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/discover/rss-feeds/"+strconv.Itoa(created.ID), bytes.NewReader(updateBody))
	updResp, err := http.DefaultClient.Do(updReq)
	if err != nil {
		t.Fatalf("PUT failed: %v", err)
	}
	defer updResp.Body.Close()
	if updResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 from Mainstream update, got %d", updResp.StatusCode)
	}
	var updated apidto.RssFeed
	if err := json.NewDecoder(updResp.Body).Decode(&updated); err != nil {
		t.Fatalf("decoding update: %v", err)
	}
	if updated.Target != "tv" || updated.Protocol != "torrent" || updated.Enabled {
		t.Fatalf("unexpected Mainstream update result: %+v", updated)
	}
	list, err = store.List(ctx)
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(list) != 1 || list[0].FeedURL != updateURL || list[0].Protocol != "torrent" {
		t.Fatalf("Mainstream update must store the new URL/protocol, got %+v", list)
	}
}
