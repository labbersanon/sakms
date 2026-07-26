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
)

// rssURLPtr wraps a plaintext feed URL as the three-state *string the upsert DTO
// now takes (non-nil, non-empty = set/replace).
func rssURLPtr(s string) *string { return &s }

// TestRssFeedCRUD_EndToEnd exercises create/list/update/delete against the
// real HTTP handlers backed by a real migrated SQLite file.
func TestRssFeedCRUD_EndToEnd(t *testing.T) {
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, testFeedHealth(), rssFeedsStore, nil, nil, nil, nil, nil))
	defer srv.Close()

	createBody, _ := json.Marshal(apidto.RssFeedUpsertRequest{
		Title: "NZBGeek Saved Search", FeedURL: rssURLPtr("https://nzbgeek.info/rss?t=1"), Target: "movie", Protocol: "usenet", Enabled: true,
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
	updateBody, _ := json.Marshal(apidto.RssFeedUpsertRequest{
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

	body, _ := json.Marshal(apidto.RssFeedUpsertRequest{Title: "Bad", FeedURL: rssURLPtr("https://example.com/rss"), Target: "not-a-real-target", Protocol: "usenet"})
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
// title/studio/image populated, while an item with no pool match keeps them empty
// (raw-feed fallback), exactly as before enrichment existed.
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
	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %+v", items)
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

	// Unmatched item: no resolved fields, raw fallback exactly as before.
	if items[1].ResolvedTitle != "" || items[1].ResolvedStudio != "" || items[1].ResolvedImage != "" {
		t.Errorf("expected unmatched item to keep empty resolved fields, got %+v", items[1])
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
