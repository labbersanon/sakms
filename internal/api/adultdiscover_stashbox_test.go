package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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
func overrideFixedURL(t *testing.T, service, u string) {
	t.Helper()
	switch service {
	case "tmdb":
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
	return NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore, nil, nil, nil, nil, nil)
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

// --- merged "Recently Released" ---

func TestAdultMergedRecent_StashDBNotConfigured_TPDBOnly(t *testing.T) {
	tpdb := fakeTPDB(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("orderBy"); got != "recently_released" {
			t.Errorf("expected orderBy=recently_released, got %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"data":[{"_id":"t1","title":"TPDB Scene","date":"2024-01-01","site":{"name":"Tushy"}}]}`))
	})

	srv := httptest.NewServer(newAdultMux(t, map[string]string{"tpdb": tpdb.URL}))
	defer srv.Close()

	var items []adultScene
	getJSON(t, srv.URL+"/api/modes/adult/discover/recent-merged", &items)
	if len(items) != 1 || items[0].ID != "t1" || items[0].Source != "tpdb" {
		t.Errorf("expected TPDB-only output, got %+v", items)
	}
}

func TestAdultMergedRecent_DropsStashDBDuplicateBySharedPHash(t *testing.T) {
	tpdb := fakeTPDB(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"data":[{"_id":"t1","title":"TPDB Scene","date":"2024-05-05","site":{"name":"Tushy"},` +
			`"hashes":[{"hash":"shared","type":"phash"}]}]}`))
	})
	stash := fakeStashBox(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"data":{"queryScenes":{"scenes":[{"id":"sb1","title":"Dup Scene","release_date":"2024-05-04",` +
			`"studio":{"name":"Blacked","parent":null},"images":[],"duration":0,` +
			`"fingerprints":[{"hash":"shared","algorithm":"PHASH"}]}]}}}`))
	})

	srv := httptest.NewServer(newAdultMux(t, map[string]string{"tpdb": tpdb.URL, "stashdb": stash.URL}))
	defer srv.Close()

	var items []adultScene
	getJSON(t, srv.URL+"/api/modes/adult/discover/recent-merged", &items)
	if len(items) != 1 || items[0].ID != "t1" {
		t.Fatalf("expected the StashDB duplicate dropped, TPDB kept, got %+v", items)
	}
}

func TestAdultMergedRecent_DisjointPHash_BothAppear(t *testing.T) {
	tpdb := fakeTPDB(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"data":[{"_id":"t1","title":"TPDB Scene","date":"2024-01-01","site":{"name":"Tushy"},` +
			`"hashes":[{"hash":"tpdbhash","type":"phash"}]}]}`))
	})
	stash := fakeStashBox(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"data":{"queryScenes":{"scenes":[{"id":"sb1","title":"Exclusive Scene","release_date":"2024-02-02",` +
			`"studio":{"name":"Blacked","parent":null},"images":[],"duration":0,` +
			`"fingerprints":[{"hash":"stashhash","algorithm":"PHASH"}]}]}}}`))
	})

	srv := httptest.NewServer(newAdultMux(t, map[string]string{"tpdb": tpdb.URL, "stashdb": stash.URL}))
	defer srv.Close()

	var items []adultScene
	getJSON(t, srv.URL+"/api/modes/adult/discover/recent-merged", &items)
	if len(items) != 2 {
		t.Fatalf("expected both scenes (disjoint phashes), got %+v", items)
	}
}

// TestAdultMergedRecent_NoTPDBHashesDoesNotMask proves an empty TPDB hash set
// never falsely dedups a StashDB scene — the favor-false-negatives contract.
func TestAdultMergedRecent_NoTPDBHashesDoesNotMask(t *testing.T) {
	tpdb := fakeTPDB(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// No "hashes" at all on the TPDB scene.
		w.Write([]byte(`{"data":[{"_id":"t1","title":"TPDB Scene","date":"2024-01-01","site":{"name":"Tushy"}}]}`))
	})
	stash := fakeStashBox(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"data":{"queryScenes":{"scenes":[{"id":"sb1","title":"StashDB Scene","release_date":"2024-02-02",` +
			`"studio":{"name":"Blacked","parent":null},"images":[],"duration":0,` +
			`"fingerprints":[{"hash":"stashhash","algorithm":"PHASH"}]}]}}}`))
	})

	srv := httptest.NewServer(newAdultMux(t, map[string]string{"tpdb": tpdb.URL, "stashdb": stash.URL}))
	defer srv.Close()

	var items []adultScene
	getJSON(t, srv.URL+"/api/modes/adult/discover/recent-merged", &items)
	if len(items) != 2 {
		t.Fatalf("an empty TPDB hash set must not mask the StashDB scene, got %+v", items)
	}
}

func TestAdultMergedRecent_SortedByDateDescending(t *testing.T) {
	tpdb := fakeTPDB(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"data":[{"_id":"t1","title":"Old TPDB","date":"2024-01-01","site":{"name":"Tushy"}}]}`))
	})
	stash := fakeStashBox(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"data":{"queryScenes":{"scenes":[{"id":"sb1","title":"New StashDB","release_date":"2024-12-31",` +
			`"studio":{"name":"Blacked","parent":null},"images":[],"duration":0,"fingerprints":[]}]}}}`))
	})

	srv := httptest.NewServer(newAdultMux(t, map[string]string{"tpdb": tpdb.URL, "stashdb": stash.URL}))
	defer srv.Close()

	var items []adultScene
	getJSON(t, srv.URL+"/api/modes/adult/discover/recent-merged", &items)
	if len(items) != 2 {
		t.Fatalf("expected 2 scenes, got %+v", items)
	}
	// Newest first, across both sources.
	if items[0].ID != "sb1" || items[1].ID != "t1" {
		t.Errorf("expected newest-first order [sb1, t1], got [%s, %s]", items[0].ID, items[1].ID)
	}
}

// TestAdultMergedRecent_StashDBErrors_DegradesToTPDBOnly proves a StashDB
// failure (configured, but the upstream call itself errors) degrades to
// TPDB-only output instead of failing the whole row — the fix for a real
// regression where a transient StashDB hiccup 502'd the entire "Recently
// Released" row and discarded the TPDB scenes already fetched successfully.
func TestAdultMergedRecent_StashDBErrors_DegradesToTPDBOnly(t *testing.T) {
	tpdb := fakeTPDB(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"data":[{"_id":"t1","title":"TPDB Scene","date":"2024-01-01","site":{"name":"Tushy"}}]}`))
	})
	stash := fakeStashBox(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"errors":[{"message":"boom"}]}`))
	})

	srv := httptest.NewServer(newAdultMux(t, map[string]string{"tpdb": tpdb.URL, "stashdb": stash.URL}))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/modes/adult/discover/recent-merged")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 (degrade to TPDB-only), got %d", resp.StatusCode)
	}
	var items []adultScene
	if err := json.NewDecoder(resp.Body).Decode(&items); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if len(items) != 1 || items[0].ID != "t1" || items[0].Source != "tpdb" {
		t.Errorf("expected TPDB-only output despite the StashDB error, got %+v", items)
	}
}

// TestAdultMergedRecent_TruncatesToPerPage proves the merged output never
// exceeds perPage even when TPDB's page plus StashDB's disjoint (non-dup)
// scenes together would — the fix for a real bug where a "page" of the
// merged row could silently return up to 2×perPage items.
func TestAdultMergedRecent_TruncatesToPerPage(t *testing.T) {
	tpdb := fakeTPDB(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"data":[` +
			`{"_id":"t1","title":"T1","date":"2024-06-01","site":{"name":"Tushy"}},` +
			`{"_id":"t2","title":"T2","date":"2024-05-01","site":{"name":"Tushy"}}` +
			`]}`))
	})
	stash := fakeStashBox(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"data":{"queryScenes":{"scenes":[` +
			`{"id":"s1","title":"S1","release_date":"2024-07-01","studio":{"name":"Blacked","parent":null},"images":[],"duration":0,"fingerprints":[{"hash":"a","algorithm":"PHASH"}]},` +
			`{"id":"s2","title":"S2","release_date":"2024-04-01","studio":{"name":"Blacked","parent":null},"images":[],"duration":0,"fingerprints":[{"hash":"b","algorithm":"PHASH"}]}` +
			`]}}}`))
	})

	srv := httptest.NewServer(newAdultMux(t, map[string]string{"tpdb": tpdb.URL, "stashdb": stash.URL}))
	defer srv.Close()

	var items []adultScene
	getJSON(t, srv.URL+"/api/modes/adult/discover/recent-merged?page=1&perPage=3", &items)
	if len(items) != 3 {
		t.Fatalf("expected output truncated to perPage=3 (4 disjoint scenes available), got %d: %+v", len(items), items)
	}
	// Truncation keeps the newest-first items: s1 (07-01), t1 (06-01), t2 (05-01) — s2 (04-01) is dropped.
	wantOrder := []string{"s1", "t1", "t2"}
	for i, id := range wantOrder {
		if items[i].ID != id {
			t.Errorf("position %d: expected %q, got %q (full: %+v)", i, id, items[i].ID, items)
		}
	}
}

func TestAdultMergedRecent_TPDBNotConfiguredIs400(t *testing.T) {
	// TPDB is required — the merged route must behave exactly like the
	// category=recent path when TPDB is absent (400 setup prompt), not silently
	// empty.
	srv := httptest.NewServer(newAdultMux(t, map[string]string{}))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/modes/adult/discover/recent-merged")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 when TPDB isn't configured, got %d", resp.StatusCode)
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
