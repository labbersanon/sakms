package tpdbrest

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSearchByHash_ParsesResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("hash") != "abc123" || q.Get("hash_type") != "phash" {
			t.Errorf("unexpected query params: %v", q)
		}
		if auth := r.Header.Get("Authorization"); auth != "Bearer testkey" {
			t.Errorf("expected Bearer auth, got %q", auth)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"_id":"1","title":"A Scene","date":"2024-01-01","site":{"name":"Some Site"}}]}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "testkey", &http.Client{Timeout: 5 * time.Second})
	out, err := c.SearchByHash(context.Background(), "abc123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 1 || out[0].Title != "A Scene" || out[0].Site != "Some Site" {
		t.Fatalf("got %+v", out)
	}
}

// TestSearchByHash_ToleratesNumericSceneID regression-covers a real production
// error: TPDB returns a bare JSON number for _id on some scenes, not always a
// quoted string, which the plain-string rawScene.ID field used to reject
// outright ("json: cannot unmarshal number into Go struct field
// rawScene.data._id of type string"). flexID must decode either shape.
func TestSearchByHash_ToleratesNumericSceneID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"_id":42,"title":"Numeric ID Scene","date":"2024-03-03","site":{"name":"Some Site"}}]}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "testkey", &http.Client{Timeout: 5 * time.Second})
	out, err := c.SearchByHash(context.Background(), "abc123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 1 || out[0].ID != "42" || out[0].Title != "Numeric ID Scene" {
		t.Fatalf("got %+v", out)
	}
}

func TestBrowseScenes_PaginatesWithoutSearchTerm(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("per_page") != "10" || q.Get("page") != "3" {
			t.Errorf("expected per_page=10 page=3, got %v", q)
		}
		if _, hasQ := q["q"]; hasQ {
			t.Errorf("expected no search term on a browse, got %v", q)
		}
		if _, hasOrder := q["orderBy"]; hasOrder {
			t.Errorf("expected no orderBy param when orderBy is empty, got %v", q)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"_id":"s9","title":"Browsed Scene","date":"2024-02-02","site":{"name":"BrowseSite"}}]}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "testkey", &http.Client{Timeout: 5 * time.Second})
	out, err := c.BrowseScenes(context.Background(), 3, 10, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 1 || out[0].Title != "Browsed Scene" || out[0].Site != "BrowseSite" {
		t.Fatalf("got %+v", out)
	}
}

func TestBrowseScenes_ClampsBadPagination(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("per_page") != "20" || q.Get("page") != "1" {
			t.Errorf("expected defaulted per_page=20 page=1, got %v", q)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "testkey", &http.Client{Timeout: 5 * time.Second})
	if _, err := c.BrowseScenes(context.Background(), 0, -5, ""); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestBrowseScenes_SendsOrderByWhenSet proves the new orderBy param is sent
// verbatim (exact "orderBy" casing) when non-empty — the "recently_released"
// ordering Adult Discover's Recently Released row relies on.
func TestBrowseScenes_SendsOrderByWhenSet(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("orderBy"); got != "recently_released" {
			t.Errorf("expected orderBy=recently_released, got %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "testkey", &http.Client{Timeout: 5 * time.Second})
	if _, err := c.BrowseScenes(context.Background(), 1, 20, "recently_released"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestGet_ParsesRating proves the scene's numeric "rating" field decodes into
// Scene.Rating — the field Adult Discover's Highest Rated row sorts on.
func TestGet_ParsesRating(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"_id":"1","title":"Rated Scene","date":"2024-01-01","site":{"name":"Some Site"},"rating":5}]}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "testkey", &http.Client{Timeout: 5 * time.Second})
	out, err := c.SearchByHash(context.Background(), "x")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 1 || out[0].Rating != 5 {
		t.Fatalf("expected Rating=5, got %+v", out)
	}
}

// TestGet_ParsesSlug proves the scene's "slug" field decodes into Scene.Slug
// — the field the external "More on TPDB" link (Discover detail popup) uses
// to build https://theporndb.net/scenes/{slug}, since TPDB's own scene pages
// are slug-path, not the `_id`-path StashDB/FansDB use.
func TestGet_ParsesSlug(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"_id":"1","title":"Slugged Scene","slug":"evilangel-ivy-ireland-dp-dvp-threesome-1","date":"2024-01-01","site":{"name":"Some Site"}}]}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "testkey", &http.Client{Timeout: 5 * time.Second})
	out, err := c.SearchByHash(context.Background(), "x")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 1 || out[0].Slug != "evilangel-ivy-ireland-dp-dvp-threesome-1" {
		t.Fatalf("expected the parsed slug, got %+v", out)
	}
}

func TestBrowsePerformers_PaginatesWithoutSearchTerm(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/performers" {
			t.Errorf("expected path /performers, got %q", r.URL.Path)
		}
		q := r.URL.Query()
		if q.Get("per_page") != "10" || q.Get("page") != "2" {
			t.Errorf("expected per_page=10 page=2, got %v", q)
		}
		if _, hasQ := q["q"]; hasQ {
			t.Errorf("expected no search term on a browse, got %v", q)
		}
		w.Header().Set("Content-Type", "application/json")
		// image empty → falls back to thumbnail per the first-non-empty rule.
		_, _ = w.Write([]byte(`{"data":[{"_id":"p1","name":"Riley Reid","image":"","thumbnail":"http://cdn/thumb.jpg","face":"http://cdn/face.jpg"}]}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "testkey", &http.Client{Timeout: 5 * time.Second})
	out, err := c.BrowsePerformers(context.Background(), 2, 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 1 || out[0].Name != "Riley Reid" || out[0].ID != "p1" {
		t.Fatalf("got %+v", out)
	}
	if out[0].Image != "http://cdn/thumb.jpg" {
		t.Errorf("expected Image to fall back to thumbnail, got %q", out[0].Image)
	}
}

// TestBrowsePerformers_PrefersPostersOverFlatFields guards the fix for a real
// missing-poster bug found on this deployment's live data (2026-07-26): TPDB's
// image/thumbnail/face fields are empty for a large share of real performers
// even though TPDB has art for them in the separate "posters" array
// (cdn.theporndb.net re-hosted, the performer analogue of a scene's
// Background.Large). A regression that stopped reading posters[0].url would
// pass every other performer test (none set posters) while reintroducing the
// exact missing-poster symptom this test exists to catch.
func TestBrowsePerformers_PrefersPostersOverFlatFields(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"_id":"p1","name":"Danna Blacke","image":"","thumbnail":"","face":"","posters":[{"id":1,"url":"https://cdn.theporndb.net/performer/poster.jpg","size":1024,"order":1}]}]}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "testkey", &http.Client{Timeout: 5 * time.Second})
	out, err := c.BrowsePerformers(context.Background(), 1, 20)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 1 || out[0].Image != "https://cdn.theporndb.net/performer/poster.jpg" {
		t.Fatalf("expected Image from posters[0].url, got %+v", out)
	}
}

// TestBrowsePerformers_FallsBackWhenPostersEmpty guards the other half of the
// same preference chain: when posters is empty/absent, the flat fields are
// still used exactly as before (no regression to the pre-fix happy path).
func TestBrowsePerformers_FallsBackWhenPostersEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"_id":"p1","name":"Riley Reid","image":"http://cdn/image.jpg","thumbnail":"http://cdn/thumb.jpg"}]}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "testkey", &http.Client{Timeout: 5 * time.Second})
	out, err := c.BrowsePerformers(context.Background(), 1, 20)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 1 || out[0].Image != "http://cdn/image.jpg" {
		t.Fatalf("expected Image to fall back to flat image field, got %+v", out)
	}
}

// TestBrowsePerformers_BackfillsFromDetailEndpoint guards the fallback added
// after live testing showed the "posters" fix (above) didn't change anything
// for performers whose list-endpoint entry is empty across every image
// field: GET /performers/{id} is called for exactly the empty ones, and its
// result backfills Image.
func TestBrowsePerformers_BackfillsFromDetailEndpoint(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/performers":
			_, _ = w.Write([]byte(`{"data":[{"_id":"p1","name":"Liv M"}]}`))
		case "/performers/p1":
			_, _ = w.Write([]byte(`{"data":{"_id":"p1","name":"Liv M","posters":[{"id":1,"url":"https://cdn.theporndb.net/performer/detail-only.jpg"}]}}`))
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
		}
	}))
	defer srv.Close()

	c := New(srv.URL, "testkey", &http.Client{Timeout: 5 * time.Second})
	out, err := c.BrowsePerformers(context.Background(), 1, 20)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 1 || out[0].Image != "https://cdn.theporndb.net/performer/detail-only.jpg" {
		t.Fatalf("expected Image backfilled from detail endpoint, got %+v", out)
	}
}

// TestBrowsePerformers_BackfillSkipsAlreadyPopulated guards the cost side of
// the backfill: a performer whose list entry already has an image must never
// trigger a detail fetch, so a fully-populated page costs zero extra
// requests.
func TestBrowsePerformers_BackfillSkipsAlreadyPopulated(t *testing.T) {
	var detailCalls int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/performers" {
			atomic.AddInt64(&detailCalls, 1)
			_, _ = w.Write([]byte(`{"data":{"_id":"p1","name":"x"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"_id":"p1","name":"Danna Blacke","image":"https://cdn.theporndb.net/performer/already-there.jpg"}]}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "testkey", &http.Client{Timeout: 5 * time.Second})
	out, err := c.BrowsePerformers(context.Background(), 1, 20)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 1 || out[0].Image != "https://cdn.theporndb.net/performer/already-there.jpg" {
		t.Fatalf("got %+v", out)
	}
	if atomic.LoadInt64(&detailCalls) != 0 {
		t.Errorf("expected zero detail-endpoint calls for an already-populated performer, got %d", detailCalls)
	}
}

// TestBrowsePerformers_BackfillToleratesDetailError guards the best-effort
// contract: a failing detail fetch must never fail the whole browse, and
// other performers on the same page must still resolve normally.
func TestBrowsePerformers_BackfillToleratesDetailError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/performers" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":[{"_id":"broken","name":"No Art Anywhere"},{"_id":"p2","name":"Already Fine","image":"https://cdn.theporndb.net/performer/fine.jpg"}]}`))
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := New(srv.URL, "testkey", &http.Client{Timeout: 5 * time.Second})
	out, err := c.BrowsePerformers(context.Background(), 1, 20)
	if err != nil {
		t.Fatalf("a failing detail backfill must not fail the browse: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("got %+v", out)
	}
	if out[0].Image != "" {
		t.Errorf("expected the unbackfillable performer to keep an empty Image, got %q", out[0].Image)
	}
	if out[1].Image != "https://cdn.theporndb.net/performer/fine.jpg" {
		t.Errorf("expected the already-populated sibling to be unaffected, got %q", out[1].Image)
	}
}

// TestBrowsePerformers_BackfillIsBounded guards
// maxImageBackfillConcurrency: a page where every performer needs a backfill
// must never exceed that many detail requests in flight at once.
func TestBrowsePerformers_BackfillIsBounded(t *testing.T) {
	var mu sync.Mutex
	inFlight, maxInFlight := 0, 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/performers" {
			data := `{"data":[`
			for i := 0; i < 20; i++ {
				if i > 0 {
					data += ","
				}
				data += `{"_id":"p` + string(rune('a'+i)) + `","name":"x"}`
			}
			data += `]}`
			_, _ = w.Write([]byte(data))
			return
		}
		mu.Lock()
		inFlight++
		if inFlight > maxInFlight {
			maxInFlight = inFlight
		}
		mu.Unlock()
		time.Sleep(20 * time.Millisecond)
		mu.Lock()
		inFlight--
		mu.Unlock()
		_, _ = w.Write([]byte(`{"data":{"_id":"x","name":"x"}}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "testkey", &http.Client{Timeout: 5 * time.Second})
	if _, err := c.BrowsePerformers(context.Background(), 1, 20); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if maxInFlight > maxImageBackfillConcurrency {
		t.Errorf("expected at most %d concurrent detail requests, saw %d", maxImageBackfillConcurrency, maxInFlight)
	}
}

// TestBrowsePerformers_ToleratesNumericID guards flexID's reuse on
// rawPerformer.ID: a regression that narrowed it back to a plain string would
// pass every other performer test (all use quoted-string ids) while still
// crashing on the exact bug class TPDB already shipped once for scenes.
func TestBrowsePerformers_ToleratesNumericID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"_id":42,"name":"Numeric ID Performer"}]}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "testkey", &http.Client{Timeout: 5 * time.Second})
	out, err := c.BrowsePerformers(context.Background(), 1, 20)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 1 || out[0].ID != "42" {
		t.Fatalf("got %+v", out)
	}
}

func TestBrowseSites_PaginatesWithoutSearchTerm(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/sites" {
			t.Errorf("expected path /sites, got %q", r.URL.Path)
		}
		q := r.URL.Query()
		if _, hasQ := q["q"]; hasQ {
			t.Errorf("expected no search term on a browse, got %v", q)
		}
		w.Header().Set("Content-Type", "application/json")
		if q.Get("page") == "1" {
			if q.Get("per_page") != "20" {
				t.Errorf("expected defaulted per_page=20, got %v", q)
			}
			// logo present → chosen first over poster/favicon.
			_, _ = w.Write([]byte(`{"data":[{"uuid":"s1","name":"Tushy","logo":"http://cdn/logo.png","favicon":"http://cdn/fav.ico","poster":"http://cdn/poster.jpg"}]}`))
			return
		}
		// Any page past 1 is the walk's end-of-catalog probe (1 real item is
		// short of the requested 20, so BrowseSites keeps walking) — an
		// empty-200 stops it.
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "testkey", &http.Client{Timeout: 5 * time.Second})
	out, err := c.BrowseSites(context.Background(), 0, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 1 || out[0].Name != "Tushy" || out[0].ID != "s1" {
		t.Fatalf("got %+v", out)
	}
	if out[0].Image != "http://cdn/logo.png" {
		t.Errorf("expected Image to prefer logo, got %q", out[0].Image)
	}
}

// TestBrowseSites_DecodesUUIDNotID guards a real live-verified bug fix
// (2026-07-26): SiteResource has no "_id" field at all (confirmed against the
// live OpenAPI spec — only "uuid" and a plain numeric "id" exist; "_id" is a
// PerformerResource-only field), so rawSiteEntry decoding "_id" left every
// Site.ID silently empty. A regression that reverted the field back to "_id"
// would pass every other site test (none happen to include a stray "id"/"_id"
// key) while reintroducing the exact bug that broke TPDB studio drill-down.
// This fixture includes a decoy numeric "id" alongside the real "uuid" to
// prove decode prefers uuid, not id or _id.
func TestBrowseSites_DecodesUUIDNotID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("page") == "1" {
			_, _ = w.Write([]byte(`{"data":[{"id":7,"uuid":"7e8d5654-2ca4-46de-ac1e-8683f9e7f778","name":"Real UUID Site"}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":[]}`)) // end-of-catalog probe past the 1 real item
	}))
	defer srv.Close()

	c := New(srv.URL, "testkey", &http.Client{Timeout: 5 * time.Second})
	out, err := c.BrowseSites(context.Background(), 1, 20)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 1 || out[0].ID != "7e8d5654-2ca4-46de-ac1e-8683f9e7f778" {
		t.Fatalf("expected ID from uuid, got %+v", out)
	}
}

// TestBrowseSites_FiltersJunkNetworks proves entries belonging to a known
// clip-platform storefront network (junkNetworkIDs) are excluded, while a
// legitimate entry with no network, and one with a non-denylisted network,
// both survive.
func TestBrowseSites_FiltersJunkNetworks(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("page") == "1" {
			_, _ = w.Write([]byte(`{"data":[
				{"uuid":"junk1","name":"Manyvids: Aryana","network":{"id":5502}},
				{"uuid":"real1","name":"Real Indie Studio"},
				{"uuid":"real2","name":"1000 Facials","network":{"id":4320}}
			]}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "testkey", &http.Client{Timeout: 5 * time.Second})
	out, err := c.BrowseSites(context.Background(), 1, 20)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("expected 2 non-junk entries, got %d: %+v", len(out), out)
	}
	for _, s := range out {
		if s.ID == "junk1" {
			t.Errorf("junk-network entry %q should have been filtered out", s.ID)
		}
	}
}

// TestBrowseSites_WalksPastAllJunkPage is the core regression guard for the
// bug this method's walk fixes: a raw TPDB page that is 100% junk-network
// entries (but NOT empty) must NOT be treated as end-of-catalog. A revert to
// naive single-page filtering would return 0 items here and the merged
// Studios row would terminate early, well before the real catalog ends.
func TestBrowseSites_WalksPastAllJunkPage(t *testing.T) {
	var pageRequests []string
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page := r.URL.Query().Get("page")
		mu.Lock()
		pageRequests = append(pageRequests, page)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch page {
		case "1":
			// Entirely junk, but NOT empty — must not stop the walk.
			_, _ = w.Write([]byte(`{"data":[
				{"uuid":"j1","name":"Manyvids: A","network":{"id":5502}},
				{"uuid":"j2","name":"Manyvids: B","network":{"id":5502}}
			]}`))
		case "2":
			_, _ = w.Write([]byte(`{"data":[{"uuid":"real1","name":"Real Studio"}]}`))
		default:
			_, _ = w.Write([]byte(`{"data":[]}`))
		}
	}))
	defer srv.Close()

	c := New(srv.URL, "testkey", &http.Client{Timeout: 5 * time.Second})
	out, err := c.BrowseSites(context.Background(), 1, 20)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 1 || out[0].ID != "real1" {
		t.Fatalf("expected the walk to reach the real entry on page 2, got %+v", out)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(pageRequests) < 2 {
		t.Fatalf("expected BrowseSites to walk past the all-junk page 1, only requested %v", pageRequests)
	}
}

// TestBrowseSites_SecondAppPageOffsetsIntoAccumulated proves app-level page 2
// (perPage 1) returns the SECOND real (non-junk) entry, not the second raw
// TPDB entry — i.e. filtering happens before the page-slice offset is
// applied, not after.
func TestBrowseSites_SecondAppPageOffsetsIntoAccumulated(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Query().Get("page") {
		case "1":
			_, _ = w.Write([]byte(`{"data":[
				{"uuid":"real1","name":"Alpha Studio"},
				{"uuid":"junk1","name":"Manyvids: X","network":{"id":5502}},
				{"uuid":"real2","name":"Beta Studio"}
			]}`))
		default:
			_, _ = w.Write([]byte(`{"data":[]}`))
		}
	}))
	defer srv.Close()

	c := New(srv.URL, "testkey", &http.Client{Timeout: 5 * time.Second})
	out, err := c.BrowseSites(context.Background(), 2, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 1 || out[0].ID != "real2" {
		t.Fatalf("expected app-page 2 (perPage 1) to be the second REAL entry, got %+v", out)
	}
}

// TestBrowseSites_CapBoundsTheWalk proves the walk stops at
// maxSitePagesPerRequest rather than looping forever against a pathological
// upstream that never returns a full empty-200 (e.g. an all-junk catalog).
// This is the second, independent backstop behind the empty-200 terminator.
func TestBrowseSites_CapBoundsTheWalk(t *testing.T) {
	var requestCount int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&requestCount, 1)
		w.Header().Set("Content-Type", "application/json")
		// Every page: 1 junk item, never empty, never enough real items.
		_, _ = w.Write([]byte(`{"data":[{"uuid":"j","name":"Manyvids: X","network":{"id":5502}}]}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "testkey", &http.Client{Timeout: 5 * time.Second})
	out, err := c.BrowseSites(context.Background(), 1, 20)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 0 {
		t.Fatalf("expected 0 real entries (all junk), got %+v", out)
	}
	if got := atomic.LoadInt64(&requestCount); got != maxSitePagesPerRequest {
		t.Fatalf("expected the walk to stop exactly at the %d-page cap, made %d requests", maxSitePagesPerRequest, got)
	}
}

func TestScenesBySite_UsesDedicatedEndpoint(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/sites/s%201/scenes" && r.URL.Path != "/sites/s 1/scenes" {
			t.Errorf("expected path /sites/{id}/scenes with escaped id, got %q", r.URL.Path)
		}
		q := r.URL.Query()
		if q.Get("per_page") != "5" || q.Get("page") != "2" {
			t.Errorf("expected per_page=5 page=2, got %v", q)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"_id":"sc1","title":"Site Scene","date":"2024-01-01","site":{"name":"Tushy"}}]}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "testkey", &http.Client{Timeout: 5 * time.Second})
	// "s 1" (with a space) proves the id is path-escaped, not parsed as an int.
	out, err := c.ScenesBySite(context.Background(), "s 1", 2, 5)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 1 || out[0].Title != "Site Scene" || out[0].Site != "Tushy" {
		t.Fatalf("got %+v", out)
	}
}

func TestScenesByPerformer_UsesDedicatedEndpoint(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/performers/p1/scenes" {
			t.Errorf("expected path /performers/p1/scenes, got %q", r.URL.Path)
		}
		q := r.URL.Query()
		if q.Get("per_page") != "20" || q.Get("page") != "1" {
			t.Errorf("expected defaulted per_page=20 page=1, got %v", q)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"_id":"sc2","title":"Performer Scene","date":"2024-01-01","site":{"name":"Vixen"}}]}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "testkey", &http.Client{Timeout: 5 * time.Second})
	out, err := c.ScenesByPerformer(context.Background(), "p1", 0, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 1 || out[0].Title != "Performer Scene" {
		t.Fatalf("got %+v", out)
	}
}

func TestSearchByTitle_OmitsSiteWhenEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("q") != "Some Title" {
			t.Errorf("expected q=Some Title, got %q", q.Get("q"))
		}
		if _, has := q["site"]; has {
			t.Errorf("expected no 'site' param when studio is empty, got %v", q)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "testkey", &http.Client{Timeout: 5 * time.Second})
	if _, err := c.SearchByTitle(context.Background(), "Some Title", ""); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSearchByTitle_IncludesSiteWhenSet(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("site") != "Tushy" {
			t.Errorf("expected site=Tushy, got %q", q.Get("site"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "testkey", &http.Client{Timeout: 5 * time.Second})
	if _, err := c.SearchByTitle(context.Background(), "Some Title", "Tushy"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestPing_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("per_page") != "1" {
			t.Errorf("expected per_page=1, got %q", q.Get("per_page"))
		}
		if _, hasQ := q["q"]; hasQ {
			t.Errorf("expected no search term on a ping, got %v", q)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "testkey", &http.Client{Timeout: 5 * time.Second})
	if err := c.Ping(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestPing_UnauthorizedKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	c := New(srv.URL, "badkey", &http.Client{Timeout: 5 * time.Second})
	if err := c.Ping(context.Background()); err == nil {
		t.Fatal("expected an error for a bad key")
	}
}

func TestGet_NonOKStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	c := New(srv.URL, "badkey", &http.Client{Timeout: 5 * time.Second})
	_, err := c.SearchByHash(context.Background(), "x")
	if err == nil {
		t.Fatal("expected an error on non-200 status")
	}
}

func TestGet_ParsesDuration(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"_id":"1","title":"Timed Scene","date":"2024-01-01","site":{"name":"Some Site"},"duration":1800}]}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "testkey", &http.Client{Timeout: 5 * time.Second})
	out, err := c.SearchByHash(context.Background(), "x")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 1 || out[0].Duration != 1800 {
		t.Fatalf("expected Duration=1800 seconds, got %+v", out)
	}
}

func TestGet_MissingDurationIsZero(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"_id":"1","title":"No Duration Scene","date":"2024-01-01","site":{"name":"Some Site"}}]}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "testkey", &http.Client{Timeout: 5 * time.Second})
	out, err := c.SearchByHash(context.Background(), "x")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// A scene with no "duration" in the response must decode to 0, the
	// documented "unknown, skip the bitrate check" sentinel — never an error.
	if len(out) != 1 || out[0].Duration != 0 {
		t.Fatalf("expected Duration=0 when absent from response, got %+v", out)
	}
}

// TestGet_ParsesHashes proves a scene's "hashes" array decodes into
// Scene.Hashes filtered to type=="phash" — the merged-recent dedup's TPDB-side
// hash set — with non-phash entries (e.g. oshash) excluded.
func TestGet_ParsesHashes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"_id":"1","title":"Hashed Scene","date":"2024-01-01","site":{"name":"Some Site"},"hashes":[{"hash":"abc123","type":"phash"},{"hash":"def456","type":"oshash"}]}]}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "testkey", &http.Client{Timeout: 5 * time.Second})
	out, err := c.SearchByHash(context.Background(), "x")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("expected 1 scene, got %d", len(out))
	}
	if len(out[0].Hashes) != 1 || out[0].Hashes[0] != "abc123" {
		t.Fatalf("expected Hashes=[abc123] (oshash excluded), got %+v", out[0].Hashes)
	}
}

// TestGet_ParsesTags proves a scene's "tags" array (TagResource objects)
// decodes into Scene.Tags with each entry's flat id/uuid/name populated — and
// that the recursive `parents` array carried by a TagResource is ignored, not
// a decode error.
func TestGet_ParsesTags(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"_id":"1","title":"Tagged Scene","date":"2024-01-01","site":{"name":"Some Site"},"tags":[{"id":7,"uuid":"u-7","name":"Anal","parents":[{"id":1,"uuid":"u-1","name":"Category"}]},{"id":9,"uuid":"u-9","name":"Blonde"}]}]}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "testkey", &http.Client{Timeout: 5 * time.Second})
	out, err := c.SearchByHash(context.Background(), "x")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("expected 1 scene, got %d", len(out))
	}
	tags := out[0].Tags
	if len(tags) != 2 {
		t.Fatalf("expected 2 tags, got %+v", tags)
	}
	if tags[0].ID != 7 || tags[0].UUID != "u-7" || tags[0].Name != "Anal" {
		t.Errorf("unexpected first tag: %+v", tags[0])
	}
	if tags[1].ID != 9 || tags[1].UUID != "u-9" || tags[1].Name != "Blonde" {
		t.Errorf("unexpected second tag: %+v", tags[1])
	}
}

// TestGet_ParsesPerformers proves a scene's "performers" array
// (PerformerResource objects) decodes into Scene.Performers as a plain name
// list — only the name is consumed, id/image/etc. are ignored.
func TestGet_ParsesPerformers(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"_id":"1","title":"Cast Scene","date":"2024-01-01","site":{"name":"Some Site"},"performers":[{"_id":"p1","name":"Jane Doe","image":"https://example.com/jane.jpg"},{"_id":"p2","name":"John Roe"}]}]}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "testkey", &http.Client{Timeout: 5 * time.Second})
	out, err := c.SearchByHash(context.Background(), "x")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("expected 1 scene, got %d", len(out))
	}
	performers := out[0].Performers
	if len(performers) != 2 || performers[0] != "Jane Doe" || performers[1] != "John Roe" {
		t.Fatalf("unexpected performers: %+v", performers)
	}
}

// TestSearchMovies_UsesMoviesEndpoint proves SearchMovies hits GET /movies with
// q + per_page=5 (mirroring SearchByTitle's shape) and decodes the shared
// SceneResource envelope, including the "type":"Movie" discriminator, into
// []Scene.
func TestSearchMovies_UsesMoviesEndpoint(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/movies" {
			t.Errorf("expected path /movies, got %q", r.URL.Path)
		}
		q := r.URL.Query()
		if q.Get("q") != "Some Movie" {
			t.Errorf("expected q=Some Movie, got %q", q.Get("q"))
		}
		if q.Get("per_page") != "5" {
			t.Errorf("expected per_page=5, got %q", q.Get("per_page"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"_id":"m1","title":"Some Movie","date":"2024-01-01","site":{"name":"MovieStudio"},"type":"Movie"}]}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "testkey", &http.Client{Timeout: 5 * time.Second})
	out, err := c.SearchMovies(context.Background(), "Some Movie")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 1 || out[0].Title != "Some Movie" || out[0].ID != "m1" || out[0].Type != "Movie" {
		t.Fatalf("got %+v", out)
	}
}

// TestBrowseMovies_PaginatesWithoutSearchTerm proves BrowseMovies hits
// GET /movies with page/per_page only — no q, no orderBy — and decodes
// Type:"Movie" correctly.
func TestBrowseMovies_PaginatesWithoutSearchTerm(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/movies" {
			t.Errorf("expected path /movies, got %q", r.URL.Path)
		}
		q := r.URL.Query()
		if q.Get("per_page") != "10" || q.Get("page") != "2" {
			t.Errorf("expected per_page=10 page=2, got %v", q)
		}
		if _, hasQ := q["q"]; hasQ {
			t.Errorf("expected no search term on a browse, got %v", q)
		}
		if _, hasOrder := q["orderBy"]; hasOrder {
			t.Errorf("expected no orderBy param on BrowseMovies, got %v", q)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"_id":"m9","title":"Browsed Movie","date":"2024-02-02","site":{"name":"MovieStudio"},"type":"Movie"}]}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "testkey", &http.Client{Timeout: 5 * time.Second})
	out, err := c.BrowseMovies(context.Background(), 2, 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 1 || out[0].Title != "Browsed Movie" || out[0].Type != "Movie" {
		t.Fatalf("got %+v", out)
	}
}

// TestBrowseMovies_ClampsBadPagination proves BrowseMovies defaults a
// non-positive page/perPage to page=1/per_page=defaultBrowsePerPage, same as
// BrowseScenes.
func TestBrowseMovies_ClampsBadPagination(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("per_page") != "20" || q.Get("page") != "1" {
			t.Errorf("expected defaulted per_page=20 page=1, got %v", q)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "testkey", &http.Client{Timeout: 5 * time.Second})
	if _, err := c.BrowseMovies(context.Background(), 0, -5); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestMoviesBySite_UsesDedicatedEndpoint proves MoviesBySite hits the dedicated
// GET /sites/{id}/movies endpoint with a path-escaped id and page/per_page.
func TestMoviesBySite_UsesDedicatedEndpoint(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/sites/s%201/movies" && r.URL.Path != "/sites/s 1/movies" {
			t.Errorf("expected path /sites/{id}/movies with escaped id, got %q", r.URL.Path)
		}
		q := r.URL.Query()
		if q.Get("per_page") != "5" || q.Get("page") != "2" {
			t.Errorf("expected per_page=5 page=2, got %v", q)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"_id":"m2","title":"Site Movie","date":"2024-01-01","site":{"name":"MovieStudio"},"type":"Movie"}]}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "testkey", &http.Client{Timeout: 5 * time.Second})
	// "s 1" (with a space) proves the id is path-escaped, not parsed as an int.
	out, err := c.MoviesBySite(context.Background(), "s 1", 2, 5)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 1 || out[0].Title != "Site Movie" || out[0].Type != "Movie" {
		t.Fatalf("got %+v", out)
	}
}

func TestGet_EmptySiteFallback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"_id":"1","title":"No Site Scene","date":"2024-01-01","site":null}]}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "k", &http.Client{Timeout: 5 * time.Second})
	out, err := c.SearchByHash(context.Background(), "x")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out[0].Site != "" {
		t.Fatalf("expected empty site for null site, got %q", out[0].Site)
	}
}

func TestSearchPerformers_ParsesResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/performers" {
			t.Errorf("expected path /performers, got %q", r.URL.Path)
		}
		if r.URL.Query().Get("q") != "riley reid" {
			t.Errorf("expected q=riley reid, got %q", r.URL.Query().Get("q"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"_id":"p1","name":"Riley Reid","image":"https://cdn.theporndb.net/performer/riley.jpg"}]}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "testkey", &http.Client{Timeout: 5 * time.Second})
	out, err := c.SearchPerformers(context.Background(), "riley reid")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 1 || out[0].Name != "Riley Reid" || out[0].ID != "p1" {
		t.Fatalf("got %+v", out)
	}
}

// TestSearchPerformers_NeverBackfills guards a real regression an architect
// review caught (2026-07-26): SearchPerformers backs
// internal/identify's entity-verification pipeline, which only reads
// performer Name (never Image) and already rate-limits its own TPDB calls via
// internal/throttle before calling SearchPerformers. If backfillMissingImages
// were ever wired into the shared getPerformers again (as it briefly was),
// it would fire unthrottled GET /performers/{id} calls through this path for
// data no caller uses -- defeating the exact protection internal/throttle
// exists for. An empty-image result must never trigger any request beyond
// the original /performers search call.
func TestSearchPerformers_NeverBackfills(t *testing.T) {
	var requestPaths []string
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requestPaths = append(requestPaths, r.URL.Path)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"_id":"p1","name":"No Art Performer"}]}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "testkey", &http.Client{Timeout: 5 * time.Second})
	out, err := c.SearchPerformers(context.Background(), "no art performer")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 1 || out[0].Image != "" {
		t.Fatalf("got %+v", out)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(requestPaths) != 1 || requestPaths[0] != "/performers" {
		t.Errorf("expected exactly one request to /performers and no detail-endpoint backfill, got %v", requestPaths)
	}
}

func TestSearchSites_ParsesResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/sites" {
			t.Errorf("expected path /sites, got %q", r.URL.Path)
		}
		if r.URL.Query().Get("q") != "tushy" {
			t.Errorf("expected q=tushy, got %q", r.URL.Query().Get("q"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"uuid":"s1","name":"Tushy"}]}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "testkey", &http.Client{Timeout: 5 * time.Second})
	out, err := c.SearchSites(context.Background(), "tushy")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 1 || out[0].Name != "Tushy" || out[0].ID != "s1" {
		t.Fatalf("got %+v", out)
	}
}

// TestScene_ImagePrefersTPDBHostedFieldsOverStudioPassthrough is the
// regression test for a real live bug: the flat "image" field is a raw
// passthrough of whatever URL the studio itself submitted — confirmed live
// in production to include short-lived signed CDN links (HTTP 410 once
// expired) and studio-side hotlink/bot-blocked hosts (HTTP 403) — while
// TPDB's own site uses its own re-hosted "background.large" for the exact
// same scene. toScene() must prefer background.large, then poster, and
// only fall back to the raw image field when TPDB has nothing better.
func TestScene_ImagePrefersTPDBHostedFieldsOverStudioPassthrough(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[
			{"_id":"s1","title":"Has Background","image":"https://studio-cdn.example/broken.jpg","poster":"https://thumb.theporndb.net/p.jpg","background":{"large":"https://cdn.theporndb.net/scene/x/large.jpg"}},
			{"_id":"s2","title":"Poster Only","image":"https://studio-cdn.example/broken2.jpg","poster":"https://thumb.theporndb.net/p2.jpg"},
			{"_id":"s3","title":"Image Only","image":"https://studio-cdn.example/broken3.jpg"}
		]}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "testkey", &http.Client{Timeout: 5 * time.Second})
	out, err := c.BrowseScenes(context.Background(), 1, 20, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 3 {
		t.Fatalf("expected 3 scenes, got %d", len(out))
	}
	if out[0].Image != "https://cdn.theporndb.net/scene/x/large.jpg" {
		t.Errorf("expected background.large to win when present, got %q", out[0].Image)
	}
	if out[1].Image != "https://thumb.theporndb.net/p2.jpg" {
		t.Errorf("expected poster to win when background is absent, got %q", out[1].Image)
	}
	if out[2].Image != "https://studio-cdn.example/broken3.jpg" {
		t.Errorf("expected the raw image field as last-resort fallback, got %q", out[2].Image)
	}
}

// TestBrowsePerformers_SendsMostRelevantSort proves orderBy=most_relevant is
// sent (NOT the original asc_name — switched 2026-07-26, operator-approved,
// after live sampling found TPDB's alphabetically-first performer pages are
// dominated by non-performer junk; most_relevant was live-sampled and found
// to return only real performer names). A regression that reverted this to
// asc_name would reintroduce the junk-on-page-1 symptom.
func TestBrowsePerformers_SendsMostRelevantSort(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("orderBy"); got != "most_relevant" {
			t.Errorf("expected orderBy=most_relevant (lowercase), got %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "testkey", &http.Client{Timeout: 5 * time.Second})
	if _, err := c.BrowsePerformers(context.Background(), 1, 20); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestBrowseSites_SendsNameSort is the /sites analogue: the live API accepts
// orderBy=asc_name (lowercase) even though the OpenAPI operation omits the
// param entirely.
func TestBrowseSites_SendsNameSort(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("orderBy"); got != "asc_name" {
			t.Errorf("expected orderBy=asc_name (lowercase), got %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "testkey", &http.Client{Timeout: 5 * time.Second})
	if _, err := c.BrowseSites(context.Background(), 1, 20); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestBrowsePerformers_DetectsClampAndSkipsBackfill guards the live-verified
// TPDB /performers out-of-range clamp mitigation: TPDB answers a page past the
// last one with HTTP 200 + the final page's items repeated, echoing
// meta.current_page as the last real page (not the requested one).
// BrowsePerformers must detect that (current_page < requested page) and return
// an empty slice (the synthetic empty-200 the pagination contract needs), and
// must NOT fire the image backfill on the discarded stale page.
//
// The clamped-page fixture items have EMPTY Image and NON-EMPTY ID on purpose:
// backfillMissingImages skips any entry that already has an Image OR has an
// empty ID, so only empty-Image + non-empty-ID items make the "zero
// detail-fetch hits" assertion a real proof that the clamp branch skipped
// backfill — rather than passing vacuously through either skip clause.
func TestBrowsePerformers_DetectsClampAndSkipsBackfill(t *testing.T) {
	const finalPage = 2
	// Both the real final page (2) and the out-of-range page (3) return the
	// same items with meta.current_page pinned to 2 — the live clamp behavior.
	const pageBody = `{"data":[{"_id":"p1","name":"Zed One"},{"_id":"p2","name":"Zed Two"}],"meta":{"current_page":2}}`
	var detailCalls int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/performers" {
			_, _ = w.Write([]byte(pageBody))
			return
		}
		// Any /performers/{id} hit is a backfill detail fetch.
		atomic.AddInt64(&detailCalls, 1)
		_, _ = w.Write([]byte(`{"data":{"_id":"p1","name":"Zed One","posters":[{"url":"http://cdn/x.jpg"}]}}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "testkey", &http.Client{Timeout: 5 * time.Second})

	// Control: the real final page (current_page == requested) returns items,
	// and — since the items have empty images — backfill DOES fire here.
	out, err := c.BrowsePerformers(context.Background(), finalPage, 20)
	if err != nil {
		t.Fatalf("control final page: unexpected error: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("control final page: expected 2 items, got %d", len(out))
	}
	if atomic.LoadInt64(&detailCalls) == 0 {
		t.Errorf("control final page: expected backfill to fire on empty-image items, got 0 detail fetches")
	}

	// Clamped: one page past final. meta.current_page (2) < requested (3) →
	// synthetic empty-200 and NO backfill on the discarded stale page.
	atomic.StoreInt64(&detailCalls, 0)
	out, err = c.BrowsePerformers(context.Background(), finalPage+1, 20)
	if err != nil {
		t.Fatalf("clamped page: unexpected error: %v", err)
	}
	if out != nil {
		t.Fatalf("clamped page: expected nil (synthetic empty-200), got %+v", out)
	}
	if got := atomic.LoadInt64(&detailCalls); got != 0 {
		t.Errorf("clamped page: expected zero detail-fetch hits (backfill skipped), got %d", got)
	}
}

// TestBrowsePerformers_MissingMetaNotClamped guards the conservative side of
// clamp detection: a response with no meta block (current_page decodes to 0)
// must NOT be treated as a clamp, even at a high page number — no false drain.
func TestBrowsePerformers_MissingMetaNotClamped(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"_id":"p1","name":"Riley","image":"http://cdn/a.jpg"}]}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "testkey", &http.Client{Timeout: 5 * time.Second})
	out, err := c.BrowsePerformers(context.Background(), 999, 20)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("expected 1 item (no false drain on missing meta), got %+v", out)
	}
}

// TestScenesByPerformer_DetectsClamp guards ScenesByPerformer's by-analogy clamp
// mitigation (see its doc comment): the guard mirrors BrowsePerformers' — a page
// whose echoed meta.current_page is below the requested page is TPDB's
// out-of-range clamp (HTTP 200 + the final page's scenes REPEATED), so
// ScenesByPerformer must return an empty slice (the synthetic empty-200 the
// merged drill-down's exhaustion check needs) instead of the stale repeated
// scenes.
//
// Unlike the performers clamp test there is no image-backfill side-effect to
// count, so non-vacuity rests entirely on the fixture: the clamped-page mock
// serves a NON-EMPTY data array (one real scene) with meta.current_page pinned
// BELOW the requested page. Asserting the method returns empty there proves the
// clamp branch fired — an empty-data mock would pass regardless. The control
// case (requested page == echoed current_page → the real scene comes back)
// proves the guard doesn't false-drain a legitimate final page.
func TestScenesByPerformer_DetectsClamp(t *testing.T) {
	const finalPage = 2
	// Both the real final page (2) and the out-of-range page (3) return the same
	// scene with meta.current_page pinned to 2 — the live clamp behavior.
	const pageBody = `{"data":[{"_id":"sc1","title":"Repeated Scene","date":"2024-01-01","site":{"name":"Vixen"}}],"meta":{"current_page":2}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/performers/p1/scenes" {
			t.Errorf("expected path /performers/p1/scenes, got %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(pageBody))
	}))
	defer srv.Close()

	c := New(srv.URL, "testkey", &http.Client{Timeout: 5 * time.Second})

	// Control: the real final page (current_page == requested) returns its scene.
	out, err := c.ScenesByPerformer(context.Background(), "p1", finalPage, 20)
	if err != nil {
		t.Fatalf("control final page: unexpected error: %v", err)
	}
	if len(out) != 1 || out[0].Title != "Repeated Scene" {
		t.Fatalf("control final page: expected the real scene, got %+v", out)
	}

	// Clamped: one page past final. meta.current_page (2) < requested (3) →
	// synthetic empty-200, NOT the stale repeated scene.
	out, err = c.ScenesByPerformer(context.Background(), "p1", finalPage+1, 20)
	if err != nil {
		t.Fatalf("clamped page: unexpected error: %v", err)
	}
	if out != nil {
		t.Fatalf("clamped page: expected nil (synthetic empty-200), got %+v", out)
	}
}

// TestScenesByPerformer_MissingMetaNotClamped guards the conservative side of
// ScenesByPerformer's clamp detection (mirrors TestBrowsePerformers_
// MissingMetaNotClamped): a response with no meta block (current_page decodes to
// 0) must NOT be treated as a clamp, even at a high page number — no false drain.
func TestScenesByPerformer_MissingMetaNotClamped(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"_id":"sc1","title":"Only Scene","date":"2024-01-01"}]}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "testkey", &http.Client{Timeout: 5 * time.Second})
	out, err := c.ScenesByPerformer(context.Background(), "p1", 999, 20)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("expected 1 scene (no false drain on missing meta), got %+v", out)
	}
}
