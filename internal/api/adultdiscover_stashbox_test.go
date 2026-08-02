package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/labbersanon/sakms/internal/adultnewest"
	"github.com/labbersanon/sakms/internal/bravesearch"
	"github.com/labbersanon/sakms/internal/stashbox"
	"github.com/labbersanon/sakms/internal/tmdb"
	"github.com/labbersanon/sakms/internal/tpdbrest"
)

// overrideFixedURL points the hardcoded base-URL package var for a fixed-URL
// service (tmdb/stashdb/fansdb/tpdb/brave) at u for the duration of the test,
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

// newAdultMux builds a mux with the given connections already upserted. Each
// entry is service→URL; the API key is a constant, the URL is what matters for
// routing outbound calls to a fake server.
func newAdultMux(t *testing.T, conns map[string]string) *http.ServeMux {
	t.Helper()
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	for service, u := range conns {
		if err := connStore.Upsert(context.Background(), service, u, "key"); err != nil {
			t.Fatalf("upserting %s: %v", service, err)
		}
		overrideFixedURL(t, service, u)
	}
	return NewMux(testHTTPClient(), connStore, nil, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, testFeedHealth(), rssFeedsStore, nil, nil, nil, nil, nil, nil)
}

func TestAdultStashBox_NotConfiguredReturnsEmptyArray(t *testing.T) {
	// No stashdb connection upserted → the recent/trending/studios/performers
	// routes must all answer 200 with a literal [] (optional-source contract),
	// never a 4xx setup prompt.
	srv := httptest.NewServer(newAdultMux(t, map[string]string{}))
	defer srv.Close()

	for _, path := range []string{
		"/api/modes/adult/discover/stashdb/recent",
		"/api/modes/adult/discover/stashdb/trending",
		"/api/modes/adult/discover/stashdb/studios",
		"/api/modes/adult/discover/stashdb/performers",
		"/api/modes/adult/discover/fansdb/recent",
	} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		func() {
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("%s: expected 200, got %d", path, resp.StatusCode)
			}
			var items []adultScene
			if err := json.NewDecoder(resp.Body).Decode(&items); err != nil {
				t.Fatalf("%s: decoding: %v", path, err)
			}
			if len(items) != 0 {
				t.Errorf("%s: expected empty array, got %+v", path, items)
			}
		}()
	}
}

func TestAdultStashBox_RecentHappyPath(t *testing.T) {
	box := fakeStashBox(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"data":{"queryScenes":{"scenes":[{"id":"sb1","title":"StashDB Scene",` +
			`"release_date":"2024-06-06","studio":{"name":"Blacked","parent":null},` +
			`"images":[{"url":"http://cdn/sb1.jpg"}],"duration":1200,` +
			`"fingerprints":[{"hash":"ph1","algorithm":"PHASH"}]}]}}}`))
	})

	srv := httptest.NewServer(newAdultMux(t, map[string]string{"stashdb": box.URL}))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/modes/adult/discover/stashdb/recent")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var items []adultScene
	if err := json.NewDecoder(resp.Body).Decode(&items); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 scene, got %d", len(items))
	}
	got := items[0]
	if got.ID != "sb1" || got.Title != "StashDB Scene" || got.Studio != "Blacked" ||
		got.Date != "2024-06-06" || got.Image != "http://cdn/sb1.jpg" || got.DurationSeconds != 1200 {
		t.Errorf("unexpected mapping: %+v", got)
	}
	if got.Source != "stashdb" {
		t.Errorf("expected source=stashdb, got %q", got.Source)
	}
	if got.Rating != 0 {
		t.Errorf("expected Rating 0 (stash-box has no rating), got %v", got.Rating)
	}
}

func TestAdultStashBox_StudiosAndPerformersSetSource(t *testing.T) {
	box := fakeStashBox(t, func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Query string `json:"query"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(req.Query, "queryStudios") {
			w.Write([]byte(`{"data":{"queryStudios":{"studios":[{"id":"st1","name":"Vixen","images":[{"url":"http://cdn/st.png"}]}]}}}`))
			return
		}
		w.Write([]byte(`{"data":{"queryPerformers":{"performers":[{"id":"pf1","name":"Riley","images":[]}]}}}`))
	})

	srv := httptest.NewServer(newAdultMux(t, map[string]string{"fansdb": box.URL}))
	defer srv.Close()

	// Studios.
	var studios []adultStudio
	getJSON(t, srv.URL+"/api/modes/adult/discover/fansdb/studios", &studios)
	if len(studios) != 1 || studios[0].Name != "Vixen" || studios[0].Image != "http://cdn/st.png" || studios[0].Source != "fansdb" {
		t.Errorf("unexpected studios: %+v", studios)
	}

	// Performers (empty images → blank Image).
	var performers []adultPerformer
	getJSON(t, srv.URL+"/api/modes/adult/discover/fansdb/performers", &performers)
	if len(performers) != 1 || performers[0].Name != "Riley" || performers[0].Image != "" || performers[0].Source != "fansdb" {
		t.Errorf("unexpected performers: %+v", performers)
	}
}

func TestAdultStashBox_UpstreamErrorIs502(t *testing.T) {
	box := fakeStashBox(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"errors":[{"message":"boom"}]}`))
	})

	srv := httptest.NewServer(newAdultMux(t, map[string]string{"stashdb": box.URL}))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/modes/adult/discover/stashdb/trending")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("expected 502 on a GraphQL/upstream error, got %d", resp.StatusCode)
	}
}

func TestAdultStashBoxStudioScenes_HappyPathStampsSourceAndPassesID(t *testing.T) {
	var gotFilter map[string]any
	box := fakeStashBox(t, func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Variables struct {
				Input map[string]any `json:"input"`
			} `json:"variables"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if raw, ok := req.Variables.Input["studios"].(map[string]any); ok {
			gotFilter = raw
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"data":{"queryScenes":{"scenes":[{"id":"sb-st-1","title":"Studio Drill Scene",` +
			`"release_date":"2024-09-09","studio":{"name":"Blacked","parent":null},` +
			`"images":[{"url":"http://cdn/drill.jpg"}],"duration":1800,"fingerprints":[]}]}}}`))
	})

	srv := httptest.NewServer(newAdultMux(t, map[string]string{"stashdb": box.URL}))
	defer srv.Close()

	var items []adultScene
	getJSON(t, srv.URL+"/api/modes/adult/discover/stashdb/studios/st-42/scenes", &items)
	if len(items) != 1 {
		t.Fatalf("expected 1 scene, got %d", len(items))
	}
	got := items[0]
	if got.ID != "sb-st-1" || got.Title != "Studio Drill Scene" || got.Studio != "Blacked" ||
		got.Date != "2024-09-09" || got.Image != "http://cdn/drill.jpg" || got.DurationSeconds != 1800 {
		t.Errorf("unexpected mapping: %+v", got)
	}
	if got.Source != "stashdb" {
		t.Errorf("expected source=stashdb, got %q", got.Source)
	}
	// The {id} path segment must reach the outgoing studios filter.
	if gotFilter == nil {
		t.Fatal("expected a studios filter in the outgoing request")
	}
	if gotFilter["modifier"] != "INCLUDES" {
		t.Errorf("studios.modifier = %v, want INCLUDES", gotFilter["modifier"])
	}
	vals, _ := gotFilter["value"].([]any)
	if len(vals) != 1 || vals[0] != "st-42" {
		t.Errorf("studios.value = %v, want [st-42] (the {id} path segment)", gotFilter["value"])
	}
}

func TestAdultStashBoxPerformerScenes_HappyPathStampsSourceAndPassesID(t *testing.T) {
	var gotFilter map[string]any
	box := fakeStashBox(t, func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Variables struct {
				Input map[string]any `json:"input"`
			} `json:"variables"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if raw, ok := req.Variables.Input["performers"].(map[string]any); ok {
			gotFilter = raw
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"data":{"queryScenes":{"scenes":[{"id":"sb-pf-1","title":"Perf Drill Scene",` +
			`"release_date":"2024-10-10","studio":{"name":"Tushy","parent":null},` +
			`"images":[],"duration":1200,"fingerprints":[]}]}}}`))
	})

	srv := httptest.NewServer(newAdultMux(t, map[string]string{"fansdb": box.URL}))
	defer srv.Close()

	var items []adultScene
	getJSON(t, srv.URL+"/api/modes/adult/discover/fansdb/performers/pf-77/scenes", &items)
	if len(items) != 1 {
		t.Fatalf("expected 1 scene, got %d", len(items))
	}
	if items[0].ID != "sb-pf-1" || items[0].Source != "fansdb" {
		t.Errorf("unexpected mapping: %+v", items[0])
	}
	if gotFilter == nil {
		t.Fatal("expected a performers filter in the outgoing request")
	}
	vals, _ := gotFilter["value"].([]any)
	if len(vals) != 1 || vals[0] != "pf-77" {
		t.Errorf("performers.value = %v, want [pf-77] (the {id} path segment)", gotFilter["value"])
	}
}

func TestAdultStashBoxEntityScenes_NotConfiguredReturnsEmptyArray(t *testing.T) {
	// No stashdb/fansdb connection upserted → the studio/performer drill-down
	// routes must answer 200 with a literal [] (optional-source contract).
	srv := httptest.NewServer(newAdultMux(t, map[string]string{}))
	defer srv.Close()

	for _, path := range []string{
		"/api/modes/adult/discover/stashdb/studios/st1/scenes",
		"/api/modes/adult/discover/stashdb/performers/pf1/scenes",
		"/api/modes/adult/discover/fansdb/studios/st1/scenes",
		"/api/modes/adult/discover/fansdb/performers/pf1/scenes",
	} {
		var items []adultScene
		getJSON(t, srv.URL+path, &items)
		if len(items) != 0 {
			t.Errorf("%s: expected empty array, got %+v", path, items)
		}
	}
}

func TestAdultStashBoxEntityScenes_UpstreamErrorIs502(t *testing.T) {
	box := fakeStashBox(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"errors":[{"message":"boom"}]}`))
	})

	srv := httptest.NewServer(newAdultMux(t, map[string]string{"stashdb": box.URL}))
	defer srv.Close()

	for _, path := range []string{
		"/api/modes/adult/discover/stashdb/studios/st1/scenes",
		"/api/modes/adult/discover/stashdb/performers/pf1/scenes",
	} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		func() {
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusBadGateway {
				t.Fatalf("%s: expected 502 on a GraphQL/upstream error, got %d", path, resp.StatusCode)
			}
		}()
	}
}

// TestStashBoxScenesCarryGenres pins §3.3: BOTH stash-box adultScene
// construction sites — the inline one in adultStashBoxScenesHandler (the
// recent/trending browse rows) and writeStashBoxScenes (the studio/performer
// drill-downs) — must emit the scene's stash-box tags as genres. Before this,
// both omitted Genres entirely and the Discover popup's tag pills were empty
// for every stashdb/fansdb scene.
//
// Performers is asserted empty in the same test on purpose: stashbox.Scene has
// no performers field, so the two are NOT symmetric and a future session must
// not "complete" this by wiring a performers source that doesn't exist.
func TestStashBoxScenesCarryGenres(t *testing.T) {
	box := fakeStashBox(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"data":{"queryScenes":{"scenes":[{"id":"sb-g-1","title":"Tagged Scene",` +
			`"release_date":"2024-11-11","studio":{"name":"Blacked","parent":null},` +
			`"images":[],"duration":900,"tags":[{"name":"Anal"},{"name":"Blonde"}],` +
			`"fingerprints":[]}]}}}`))
	})

	srv := httptest.NewServer(newAdultMux(t, map[string]string{"stashdb": box.URL}))
	defer srv.Close()

	for _, path := range []string{
		// adultStashBoxScenesHandler's inline construction.
		"/api/modes/adult/discover/stashdb/recent",
		// writeStashBoxScenes, shared by the studio and performer drill-downs.
		"/api/modes/adult/discover/stashdb/studios/st-9/scenes",
		"/api/modes/adult/discover/stashdb/performers/pf-9/scenes",
	} {
		var items []adultScene
		getJSON(t, srv.URL+path, &items)
		if len(items) != 1 {
			t.Fatalf("%s: expected 1 scene, got %d", path, len(items))
		}
		if got := strings.Join(items[0].Genres, ","); got != "Anal,Blonde" {
			t.Errorf("%s: genres = %q, want \"Anal,Blonde\"", path, got)
		}
		if len(items[0].Performers) != 0 {
			t.Errorf("%s: performers = %v, want empty (stash-box has no performers field)", path, items[0].Performers)
		}
	}
}

// --- merged "Recently Released" (now pooled + D5-gated, see adultdiscover_stashbox.go) ---
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
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	for _, m := range seed {
		if err := adultNewestReleaseStore.Insert(context.Background(), m); err != nil {
			t.Fatalf("seeding release %q: %v", m.EntityID, err)
		}
	}
	mux := NewMux(testHTTPClient(), connStore, nil, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, fh, rssFeedsStore, nil, nil, nil, nil, nil, nil)
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
