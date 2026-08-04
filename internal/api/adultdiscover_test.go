package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/labbersanon/sakms/internal/adultnewest"
)

// fakeTPDB serves ThePornDB's /scenes REST endpoint from a handler the test
// supplies, so a test can assert the exact query params (browse vs. search).
func fakeTPDB(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	// Handlers now read tpdbrest.DefaultBaseURL, not Connection.URL — point it
	// at this fake for the test's duration so outbound REST calls land here.
	overrideFixedURL(t, "tpdb", srv.URL)
	return srv
}

func TestAdultDiscoverHandler_Browse(t *testing.T) {
	var gotPage, gotPerPage, gotQ string
	tpdb := fakeTPDB(t, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		gotPage, gotPerPage = q.Get("page"), q.Get("per_page")
		_, hasQ := q["q"]
		if hasQ {
			gotQ = "present"
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"data":[{"_id":"s1","title":"A Scene","date":"2024-01-01","site":{"name":"Tushy"},"duration":1800}]}`))
	})

	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	if err := connStore.Upsert(context.Background(), "tpdb", tpdb.URL, "key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, nil, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, testFeedHealth(), rssFeedsStore, nil, nil, nil, nil, nil, nil, nil))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/modes/adult/discover?page=2&perPage=15")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if gotPage != "2" || gotPerPage != "15" {
		t.Errorf("expected page=2 per_page=15, got page=%q per_page=%q", gotPage, gotPerPage)
	}
	if gotQ != "" {
		t.Errorf("expected no search term on a browse, got one")
	}

	var items []adultScene
	if err := json.NewDecoder(resp.Body).Decode(&items); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if len(items) != 1 || items[0].Title != "A Scene" || items[0].Studio != "Tushy" || items[0].ID != "s1" {
		t.Errorf("unexpected items (studio must map from Site): %+v", items)
	}
	if items[0].DurationSeconds != 1800 {
		t.Errorf("expected DurationSeconds to map from TPDB's duration field, got %d", items[0].DurationSeconds)
	}
}

// TestAdultDiscoverHandler_MapsTagsAndPerformers proves a TPDB scene's tags
// and performers arrays map onto the wire adultScene's Genres/Performers
// fields — backing the Discover detail popup's tags/performers list.
func TestAdultDiscoverHandler_MapsTagsAndPerformers(t *testing.T) {
	tpdb := fakeTPDB(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"data":[{"_id":"s1","title":"A Scene","date":"2024-01-01","site":{"name":"Tushy"},"tags":[{"id":1,"name":"Anal"},{"id":2,"name":"Blonde"}],"performers":[{"name":"Jane Doe"},{"name":"John Roe"}]}]}`))
	})

	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	if err := connStore.Upsert(context.Background(), "tpdb", tpdb.URL, "key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, nil, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, testFeedHealth(), rssFeedsStore, nil, nil, nil, nil, nil, nil, nil))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/modes/adult/discover")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()

	var items []adultScene
	if err := json.NewDecoder(resp.Body).Decode(&items); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	if len(items[0].Genres) != 2 || items[0].Genres[0] != "Anal" || items[0].Genres[1] != "Blonde" {
		t.Errorf("unexpected genres: %+v", items[0].Genres)
	}
	if len(items[0].Performers) != 2 || items[0].Performers[0] != "Jane Doe" || items[0].Performers[1] != "John Roe" {
		t.Errorf("unexpected performers: %+v", items[0].Performers)
	}
}

// TestAdultDiscoverHandler_SearchByTerm proves the q param is re-pointed off the
// live TPDB SearchByTitle call and onto the identified-available pool (D4b): a
// case-insensitive title match over adult_newest_releases, gated by D5. No TPDB
// connection is configured — the pooled search must not need one.
func TestAdultDiscoverHandler_SearchByTerm(t *testing.T) {
	seed := []adultnewest.MatchedRelease{
		{RowType: adultnewest.RowScene, EntityID: "s2", EntitySource: "tpdb", EntityTitle: "Found Scene", EntityStudio: "Vixen", EntityDate: "2023-05-05", BrowseConfirmed: true},
		{RowType: adultnewest.RowScene, EntityID: "s3", EntitySource: "tpdb", EntityTitle: "Unrelated", EntityDate: "2023-05-06", BrowseConfirmed: true},
	}
	srv := newAdultPoolServer(t, adultnewest.NewFeedHealth(), seed)

	resp, err := http.Get(srv.URL + "/api/modes/adult/discover?q=found+scene")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var items []adultScene
	if err := json.NewDecoder(resp.Body).Decode(&items); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if len(items) != 1 || items[0].Title != "Found Scene" || items[0].Studio != "Vixen" {
		t.Errorf("expected only the title-matching pooled scene, got %+v", items)
	}
}

// TestAdultDiscoverHandler_CategoryRecent proves category=recent asks TPDB for
// the recently_released server-side ordering.
func TestAdultDiscoverHandler_CategoryRecent(t *testing.T) {
	var gotOrderBy string
	tpdb := fakeTPDB(t, func(w http.ResponseWriter, r *http.Request) {
		gotOrderBy = r.URL.Query().Get("orderBy")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"data":[{"_id":"s1","title":"Recent Scene","date":"2024-01-01","site":{"name":"Tushy"}}]}`))
	})

	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	if err := connStore.Upsert(context.Background(), "tpdb", tpdb.URL, "key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, nil, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, testFeedHealth(), rssFeedsStore, nil, nil, nil, nil, nil, nil, nil))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/modes/adult/discover?category=recent")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if gotOrderBy != "recently_released" {
		t.Errorf("expected orderBy=recently_released for category=recent, got %q", gotOrderBy)
	}
}

// TestAdultDiscoverHandler_CategoryTopRated proves category=top-rated makes a
// plain (unordered) browse and then re-sorts the returned page by rating
// descending, server-side — the documented same-page re-sort, not a server sort.
func TestAdultDiscoverHandler_CategoryTopRated(t *testing.T) {
	var gotOrderBy string
	var hadOrderBy bool
	tpdb := fakeTPDB(t, func(w http.ResponseWriter, r *http.Request) {
		gotOrderBy = r.URL.Query().Get("orderBy")
		_, hadOrderBy = r.URL.Query()["orderBy"]
		w.Header().Set("Content-Type", "application/json")
		// Deliberately out of rating order in the response.
		w.Write([]byte(`{"data":[
			{"_id":"low","title":"Low","date":"2024-01-01","site":{"name":"S"},"rating":2},
			{"_id":"high","title":"High","date":"2024-01-01","site":{"name":"S"},"rating":9},
			{"_id":"mid","title":"Mid","date":"2024-01-01","site":{"name":"S"},"rating":5}
		]}`))
	})

	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	if err := connStore.Upsert(context.Background(), "tpdb", tpdb.URL, "key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, nil, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, testFeedHealth(), rssFeedsStore, nil, nil, nil, nil, nil, nil, nil))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/modes/adult/discover?category=top-rated")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	// Honesty check: no server-side ordering is requested for top-rated.
	if hadOrderBy {
		t.Errorf("expected NO orderBy param for category=top-rated (it's a same-page re-sort), got %q", gotOrderBy)
	}
	var items []adultScene
	if err := json.NewDecoder(resp.Body).Decode(&items); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if len(items) != 3 || items[0].ID != "high" || items[1].ID != "mid" || items[2].ID != "low" {
		t.Errorf("expected scenes re-sorted by rating desc (high,mid,low), got %+v", items)
	}
	if items[0].Rating != 9 {
		t.Errorf("expected rating to be exposed on the wire, got %v", items[0].Rating)
	}
}

// TestAdultDiscoverHandler_SortByPassthrough proves a valid allow-listed
// sortBy on the plain browse path is forwarded to TPDB as orderBy.
func TestAdultDiscoverHandler_SortByPassthrough(t *testing.T) {
	var gotOrderBy string
	tpdb := fakeTPDB(t, func(w http.ResponseWriter, r *http.Request) {
		gotOrderBy = r.URL.Query().Get("orderBy")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"data":[{"_id":"s1","title":"A Scene","date":"2024-01-01","site":{"name":"Tushy"}}]}`))
	})

	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	if err := connStore.Upsert(context.Background(), "tpdb", tpdb.URL, "key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, nil, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, testFeedHealth(), rssFeedsStore, nil, nil, nil, nil, nil, nil, nil))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/modes/adult/discover?sortBy=recently_created")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if gotOrderBy != "recently_created" {
		t.Errorf("expected a valid sortBy to forward as orderBy=recently_created, got %q", gotOrderBy)
	}
}

// TestAdultDiscoverHandler_InvalidSortByFallsBack proves an unknown sortBy is
// rejected by the allow-list and resolves to "" — no orderBy param is sent,
// preserving the historical plain unordered browse.
func TestAdultDiscoverHandler_InvalidSortByFallsBack(t *testing.T) {
	var hadOrderBy bool
	tpdb := fakeTPDB(t, func(w http.ResponseWriter, r *http.Request) {
		_, hadOrderBy = r.URL.Query()["orderBy"]
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"data":[{"_id":"s1","title":"A Scene","date":"2024-01-01","site":{"name":"Tushy"}}]}`))
	})

	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	if err := connStore.Upsert(context.Background(), "tpdb", tpdb.URL, "key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, nil, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, testFeedHealth(), rssFeedsStore, nil, nil, nil, nil, nil, nil, nil))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/modes/adult/discover?sortBy=most_relevant")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if hadOrderBy {
		t.Errorf("expected an unknown sortBy to send NO orderBy param (plain browse), but one was sent")
	}
}

// TestAdultStudiosHandler_Browse also guards BrowseSites' junk-network-filter
// walk (2026-07-26, see tpdbrest.BrowseSites): the fake TPDB serves 15 real
// items on each of pages 1-2 and an empty page 3 (no junk in this fixture —
// that behavior is covered directly in tpdbrest's own tests), so app page 2
// at perPage 15 must resolve to exactly TPDB page 2's 15 items, proving the
// handler's plumbing survives BrowseSites now walking internally instead of
// making one direct pass-through request.
func TestAdultStudiosHandler_Browse(t *testing.T) {
	var gotPath string
	tpdb := fakeTPDB(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Query().Get("page") {
		case "1":
			w.Write([]byte(`{"data":[` + fakeSitesPage("p1", 15) + `]}`))
		case "2":
			w.Write([]byte(`{"data":[` + fakeSitesPage("p2", 15) + `]}`))
		default:
			w.Write([]byte(`{"data":[]}`))
		}
	})

	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	if err := connStore.Upsert(context.Background(), "tpdb", tpdb.URL, "key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, nil, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, testFeedHealth(), rssFeedsStore, nil, nil, nil, nil, nil, nil, nil))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/modes/adult/studios?page=2&perPage=15")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if gotPath != "/sites" {
		t.Errorf("expected browse to hit /sites, got %q", gotPath)
	}
	var studios []adultStudio
	if err := json.NewDecoder(resp.Body).Decode(&studios); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if len(studios) != 15 {
		t.Fatalf("expected 15 studios (app page 2 = TPDB page 2's real items), got %d: %+v", len(studios), studios)
	}
	if studios[0].ID != "p2-1" || studios[0].Name != "p2-1" {
		t.Errorf("expected app page 2 to be TPDB page 2's content, got first=%+v", studios[0])
	}
}

// fakeSitesPage builds n raw TPDB site JSON objects with ids/names
// "{prefix}-1".."{prefix}-n", no network field (never junk-filtered).
func fakeSitesPage(prefix string, n int) string {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		if i > 1 {
			b.WriteByte(',')
		}
		id := prefix + "-" + strconv.Itoa(i)
		b.WriteString(`{"uuid":"` + id + `","name":"` + id + `"}`)
	}
	return b.String()
}

func TestAdultPerformersHandler_Browse(t *testing.T) {
	var gotPath string
	tpdb := fakeTPDB(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"data":[{"_id":"pf1","name":"Riley Reid","thumbnail":"http://cdn/thumb.jpg"}]}`))
	})

	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	if err := connStore.Upsert(context.Background(), "tpdb", tpdb.URL, "key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, nil, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, testFeedHealth(), rssFeedsStore, nil, nil, nil, nil, nil, nil, nil))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/modes/adult/performers")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if gotPath != "/performers" {
		t.Errorf("expected browse to hit /performers, got %q", gotPath)
	}
	var performers []adultPerformer
	if err := json.NewDecoder(resp.Body).Decode(&performers); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if len(performers) != 1 || performers[0].ID != "pf1" || performers[0].Name != "Riley Reid" || performers[0].Image != "http://cdn/thumb.jpg" {
		t.Errorf("unexpected performers: %+v", performers)
	}
}

func TestAdultStudioScenesHandler_DrillDown(t *testing.T) {
	var gotPath string
	tpdb := fakeTPDB(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"data":[{"_id":"sc1","title":"Studio Scene","date":"2024-01-01","site":{"name":"Tushy"}}]}`))
	})

	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	if err := connStore.Upsert(context.Background(), "tpdb", tpdb.URL, "key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, nil, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, testFeedHealth(), rssFeedsStore, nil, nil, nil, nil, nil, nil, nil))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/modes/adult/studios/st1/scenes")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if gotPath != "/sites/st1/scenes" {
		t.Errorf("expected drill-down to hit /sites/st1/scenes, got %q", gotPath)
	}
	var scenes []adultScene
	if err := json.NewDecoder(resp.Body).Decode(&scenes); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if len(scenes) != 1 || scenes[0].Title != "Studio Scene" || scenes[0].Studio != "Tushy" {
		t.Errorf("unexpected scenes: %+v", scenes)
	}
}

func TestAdultPerformerScenesHandler_DrillDown(t *testing.T) {
	var gotPath string
	tpdb := fakeTPDB(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"data":[{"_id":"sc2","title":"Performer Scene","date":"2024-01-01","site":{"name":"Vixen"}}]}`))
	})

	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	if err := connStore.Upsert(context.Background(), "tpdb", tpdb.URL, "key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, nil, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, testFeedHealth(), rssFeedsStore, nil, nil, nil, nil, nil, nil, nil))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/modes/adult/performers/pf1/scenes")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if gotPath != "/performers/pf1/scenes" {
		t.Errorf("expected drill-down to hit /performers/pf1/scenes, got %q", gotPath)
	}
	var scenes []adultScene
	if err := json.NewDecoder(resp.Body).Decode(&scenes); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if len(scenes) != 1 || scenes[0].Title != "Performer Scene" {
		t.Errorf("unexpected scenes: %+v", scenes)
	}
}

func TestAdultDiscoverHandler_TPDBNotConfigured(t *testing.T) {
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, nil, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, testFeedHealth(), rssFeedsStore, nil, nil, nil, nil, nil, nil, nil))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/modes/adult/discover")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 when tpdb isn't configured, got %d", resp.StatusCode)
	}
}
