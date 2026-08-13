// Claude 2026-08-02: this file was adultdiscover_stashbox_test.go and held the
// TestAdultStashBox_* / *StudioScenes / *PerformerScenes / *EntityScenes /
// TestStashBoxScenesCarryGenres suites plus their newAdultMux helper. All of
// those are deleted with the 12 stash-box routes they covered.
// Reason: the file itself was NOT deleted, and must not be. Despite the old
// name it is package api's de-facto SHARED TEST HELPER file — overrideFixedURL
// is called from ~19 other test files, and fakeStashBox / newAdultPoolServer /
// getJSON each have external callers, so deleting it is a compile failure of
// the whole internal/api test package, not a lost test or two.
// Troubleshooting: the old name described contents the file no longer had,
// which is how a draft of this change came to propose deleting it outright.
// Review if: the shared helpers below move to a dedicated helpers_test.go — at
// that point this file is only the merged-recent suite and the note is stale.
// Related files: internal/api/adultdiscover_merged.go, internal/api/fixed_url_test.go

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/labbersanon/sakms/internal/adultnewest"
	"github.com/labbersanon/sakms/internal/bravesearch"
	"github.com/labbersanon/sakms/internal/stashbox"
	"github.com/labbersanon/sakms/internal/tmdb"
	"github.com/labbersanon/sakms/internal/tpdbrest"
	"github.com/labbersanon/sakms/internal/tvdb"
)

// overrideFixedURL points the hardcoded base-URL package var for a fixed-URL
// service (tmdb/tvdb/stashdb/fansdb/tpdb/brave) at u for the duration of the test,
// restoring it on cleanup. Handlers now ignore Connection.URL for these
// services and read the package var instead, so a test that stands up a fake
// server must redirect the var, not just store the URL. No-op for any other
// service (e.g. stash/prowlarr, which legitimately still use Connection.URL).
//
// The "tmdb" case ALSO empties internal/tmdb's package-level response cache —
// see that case's own comment for why it belongs here rather than per-file.
func overrideFixedURL(t *testing.T, service, u string) {
	t.Helper()
	switch service {
	case "tmdb":
		// Claude 2026-08-02: reset the TMDB response cache here, in the shared
		// helper, so all 53 call sites across 17 files get it automatically.
		// Reason: internal/tmdb's cache is a package-level singleton keyed on
		// base URL + path, and package api cannot reach the per-Client field —
		// it builds clients through mode.Build against this swapped
		// DefaultBaseURL. Sequential httptest servers in one process routinely
		// reuse an ephemeral port, so two unrelated tests can land on the same
		// base URL inside the cache's 10-minute TTL and one gets served the
		// other's body — silently, with a passing-looking result. Only ONE file
		// used to reset (discover_detail_test.go's detailMux); every other call
		// site was exposed. Hoisted rather than sprinkled because a per-file
		// reset is a rule every future caller has to know about and none of them
		// will.
		// Troubleshooting: Phase-4 review FIX 2 (architect F2 + code-reviewer).
		// Review if: the cache stops being a package-level singleton.
		//
		// Reset up front AND on cleanup: up front so this test starts cold, on
		// cleanup so it does not leave its own entries for the next one. It runs
		// during setup, before any handler is built or any request is issued, at
		// every current call site — deliberately NOT between a test's own
		// subtests, which share one fake server and whose cache sharing is both
		// correct and load-bearing (e.g. autograb_batch_test.go's cap-boundary
		// subtests).
		tmdb.ResetDefaultCache()
		t.Cleanup(tmdb.ResetDefaultCache)
		prev := tmdb.DefaultBaseURL
		tmdb.DefaultBaseURL = u
		t.Cleanup(func() { tmdb.DefaultBaseURL = prev })
	case "tvdb":
		prev := tvdb.DefaultBaseURL
		tvdb.DefaultBaseURL = u
		t.Cleanup(func() { tvdb.DefaultBaseURL = prev })
	case "stashdb":
		prev := stashbox.StashDBURL
		stashbox.StashDBURL = u
		t.Cleanup(func() { stashbox.StashDBURL = prev })
	case "fansdb":
		prev := stashbox.FansDBURL
		stashbox.FansDBURL = u
		t.Cleanup(func() { stashbox.FansDBURL = prev })
	case "tpdb":
		prev := tpdbrest.DefaultBaseURL
		tpdbrest.DefaultBaseURL = u
		t.Cleanup(func() { tpdbrest.DefaultBaseURL = prev })
	case "brave":
		prev := bravesearch.DefaultBaseURL
		bravesearch.DefaultBaseURL = u
		t.Cleanup(func() { bravesearch.DefaultBaseURL = prev })
	}
}

// fakeStashBox serves a stash-box GraphQL endpoint from a handler the test
// supplies (all stash-box calls are POSTs to a single endpoint), mirroring
// fakeTPDB for the REST side.
func fakeStashBox(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv
}

// --- merged "Recently Released" (pooled + D5-gated, see adultdiscover_merged.go) ---
//
// adultDiscoverMergedRecentHandler was re-pointed off the old live TPDB+StashDB
// merge and onto the identified-available pool (ReleaseStore.ListRecentScenes),
// gated by the D5 visibility rule + DirectGrabURL exposure. These tests seed the
// adult_newest_releases cache directly and assert the pooled/gated contract; the
// former live-fetch/dedup/merge tests were removed with that behavior.

// newAdultPoolServer builds a server whose adult_newest_releases cache is seeded
// with seed and whose injected FeedHealth is fh, so the pooled Adult Discover
// read paths (recent-merged, search) can be exercised with no live upstream call
// and a controllable feed-health state.
func newAdultPoolServer(t *testing.T, fh *adultnewest.FeedHealth, seed []adultnewest.MatchedRelease) *httptest.Server {
	t.Helper()
	connStore, propStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	for _, m := range seed {
		if err := adultNewestReleaseStore.Insert(context.Background(), m); err != nil {
			t.Fatalf("seeding release %q: %v", m.EntityID, err)
		}
	}
	mux := NewMux(testHTTPClient(), connStore, nil, propStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, fh, rssFeedsStore, nil, nil, nil, nil, nil, nil, nil, nil)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestAdultMergedRecent_EmptyCacheReturnsEmptyArray(t *testing.T) {
	srv := newAdultPoolServer(t, adultnewest.NewFeedHealth(), nil)
	var items []adultScene
	getJSON(t, srv.URL+"/api/modes/adult/discover/recent-merged", &items)
	if len(items) != 0 {
		t.Errorf("expected [] from an empty pool, got %+v", items)
	}
}

func TestAdultMergedRecent_ReturnsPooledScenesNewestFirst(t *testing.T) {
	seed := []adultnewest.MatchedRelease{
		{RowType: adultnewest.RowScene, EntityID: "s-old", EntitySource: "tpdb", EntityTitle: "Old", EntityDate: "2024-01-01", BrowseConfirmed: true},
		{RowType: adultnewest.RowScene, EntityID: "s-new", EntitySource: "tpdb", EntityTitle: "New", EntityDate: "2024-12-31", BrowseConfirmed: true},
	}
	srv := newAdultPoolServer(t, adultnewest.NewFeedHealth(), seed)
	var items []adultScene
	getJSON(t, srv.URL+"/api/modes/adult/discover/recent-merged", &items)
	if len(items) != 2 {
		t.Fatalf("expected 2 pooled scenes, got %+v", items)
	}
	if items[0].ID != "s-new" || items[1].ID != "s-old" {
		t.Errorf("expected newest-date-first [s-new, s-old], got [%s, %s]", items[0].ID, items[1].ID)
	}
	if items[0].Source != "tpdb" {
		t.Errorf("expected source preserved from the cache row, got %q", items[0].Source)
	}
}

func TestAdultMergedRecent_GatesOutFeedOnlyWhenFeedNotFresh(t *testing.T) {
	now := time.Now().Unix()
	seed := []adultnewest.MatchedRelease{
		// Browse-confirmed scene — always visible.
		{RowType: adultnewest.RowScene, EntityID: "browse", EntitySource: "tpdb", EntityTitle: "Browse", EntityDate: "2024-02-02", BrowseConfirmed: true},
		// Feed-only scene whose feed (id 7) has no health entry → gated out.
		{RowType: adultnewest.RowScene, EntityID: "feedonly", EntitySource: "tpdb", EntityTitle: "FeedOnly", EntityDate: "2024-03-03",
			DownloadURL: "http://feed/x.torrent", DownloadProtocol: "torrent", FeedID: 7, FeedItemKey: "http://feed/x.torrent", LastConfirmedSeen: now},
	}
	// FeedHealth with NO entry for feed 7 → feedFresh false → feed-only gates out.
	srv := newAdultPoolServer(t, adultnewest.NewFeedHealth(), seed)
	var items []adultScene
	getJSON(t, srv.URL+"/api/modes/adult/discover/recent-merged", &items)
	if len(items) != 1 || items[0].ID != "browse" {
		t.Fatalf("expected only the browse-confirmed scene visible, got %+v", items)
	}
}

func TestAdultMergedRecent_BothSourcedFeedDownStaysVisibleWithoutEnclosure(t *testing.T) {
	now := time.Now().Unix()
	seed := []adultnewest.MatchedRelease{
		// Both-sourced: browse-confirmed AND has a feed enclosure on feed 7.
		{RowType: adultnewest.RowScene, EntityID: "both", EntitySource: "tpdb", EntityTitle: "Both", EntityDate: "2024-04-04", BrowseConfirmed: true,
			DownloadURL: "http://feed/both.torrent", DownloadProtocol: "torrent", FeedID: 7, FeedItemKey: "http://feed/both.torrent", LastConfirmedSeen: now},
	}
	// Feed 7 is NOT healthy (empty FeedHealth) → the row stays visible via
	// browse_confirmed but its enclosure is not exposed (Prowlarr fallback).
	srv := newAdultPoolServer(t, adultnewest.NewFeedHealth(), seed)
	var items []adultScene
	getJSON(t, srv.URL+"/api/modes/adult/discover/recent-merged", &items)
	if len(items) != 1 || items[0].ID != "both" {
		t.Fatalf("expected the both-sourced row still visible, got %+v", items)
	}
	if items[0].DownloadURL != "" {
		t.Errorf("expected empty downloadUrl when the feed is down (Prowlarr fallback), got %q", items[0].DownloadURL)
	}
}

func TestAdultMergedRecent_FreshFeedExposesEnclosure(t *testing.T) {
	now := time.Now()
	seed := []adultnewest.MatchedRelease{
		{RowType: adultnewest.RowScene, EntityID: "feedonly", EntitySource: "tpdb", EntityTitle: "FeedOnly", EntityDate: "2024-05-05",
			DownloadURL: "http://feed/x.torrent", DownloadProtocol: "torrent", SizeBytes: 4242, FeedID: 7, FeedItemKey: "http://feed/x.torrent", LastConfirmedSeen: now.Unix()},
	}
	fh := adultnewest.NewFeedHealth()
	fh.SetHealthy(7, now) // feed 7 fresh → enclosure exposed and row visible
	srv := newAdultPoolServer(t, fh, seed)
	var items []adultScene
	getJSON(t, srv.URL+"/api/modes/adult/discover/recent-merged", &items)
	if len(items) != 1 || items[0].ID != "feedonly" {
		t.Fatalf("expected the feed-only row visible on a fresh feed, got %+v", items)
	}
	if items[0].DownloadURL != "http://feed/x.torrent" || items[0].Protocol != "torrent" || items[0].SizeBytes != 4242 {
		t.Errorf("expected the enclosure exposed on a fresh feed, got %+v", items[0])
	}
}

// getJSON GETs url, asserts 200, and decodes the body into out.
func getJSON(t *testing.T, url string, out any) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: expected 200, got %d", url, resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		t.Fatalf("GET %s: decoding: %v", url, err)
	}
}
