package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/labbersanon/sakms/internal/apidto"
)

// TestRssFeedCRUD_EndToEnd exercises create/list/update/delete against the
// real HTTP handlers backed by a real migrated SQLite file.
func TestRssFeedCRUD_EndToEnd(t *testing.T) {
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore, nil, nil, nil, nil, nil))
	defer srv.Close()

	createBody, _ := json.Marshal(apidto.RssFeedUpsertRequest{
		Title: "NZBGeek Saved Search", FeedURL: "https://nzbgeek.info/rss?t=1", Target: "movie", Protocol: "usenet", Enabled: true,
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
		Title: "NZBGeek Saved Search (renamed)", FeedURL: "https://nzbgeek.info/rss?t=2", Target: "tv", Protocol: "torrent", Enabled: false,
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
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore, nil, nil, nil, nil, nil))
	defer srv.Close()

	body, _ := json.Marshal(apidto.RssFeedUpsertRequest{Title: "Bad", FeedURL: "https://example.com/rss", Target: "not-a-real-target", Protocol: "usenet"})
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

	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore, nil, nil, nil, nil, nil))
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

	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore, nil, nil, nil, nil, nil))
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

// TestResolveRssFeedHandler_UnknownIDReturns404 proves resolving a
// nonexistent feed id 404s instead of panicking or 500ing.
func TestResolveRssFeedHandler_UnknownIDReturns404(t *testing.T) {
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore, nil, nil, nil, nil, nil))
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
