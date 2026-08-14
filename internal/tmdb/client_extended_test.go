package tmdb

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

// recordingClient serves body for every request and captures the last request's
// path + query — enough to assert both the response decode and the request
// shape the new detail/calendar methods build.
func recordingClient(t *testing.T, body string) (*Client, *string, *url.Values) {
	t.Helper()
	var gotPath string
	var gotQuery url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.Query()
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	c := New(Config{BaseURL: srv.URL, APIKey: "test-key"}, srv.Client())
	// Per-client cache, same reason as client_test.go's newTestClient: the
	// production default is package-level and process-lifetime (cache.go), so a
	// reused ephemeral httptest port could otherwise serve an earlier test's
	// body here — and this helper's assertions read gotPath/gotQuery, which a
	// cache-served response never writes.
	c.cache = newCache(defaultCacheCap, defaultCacheTTL)
	return c, &gotPath, &gotQuery
}

func TestMovieFullCredits_FiltersCrewToKeyRoles(t *testing.T) {
	const body = `{
	  "cast": [
	    {"name": "Lead Actor", "character": "Hero", "profile_path": "/a.jpg"},
	    {"name": "Second Actor", "character": "Villain", "profile_path": "/b.jpg"}
	  ],
	  "crew": [
	    {"name": "The Director", "job": "Director", "department": "Directing", "profile_path": "/d.jpg"},
	    {"name": "A Gaffer", "job": "Gaffer", "department": "Lighting", "profile_path": "/g.jpg"},
	    {"name": "The Writer", "job": "Screenplay", "department": "Writing", "profile_path": "/w.jpg"}
	  ]
	}`
	c, path, _ := recordingClient(t, body)
	credits, err := c.MovieFullCredits(context.Background(), 42)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if *path != "/movie/42/credits" {
		t.Errorf("unexpected path: %s", *path)
	}
	if len(credits.Cast) != 2 || credits.Cast[0].Name != "Lead Actor" || credits.Cast[0].Character != "Hero" || credits.Cast[0].ProfilePath != "/a.jpg" {
		t.Errorf("unexpected cast: %+v", credits.Cast)
	}
	// Gaffer must be filtered out; only Director + Screenplay survive.
	if len(credits.Crew) != 2 {
		t.Fatalf("expected 2 key crew (Director, Screenplay), got %d: %+v", len(credits.Crew), credits.Crew)
	}
	if credits.Crew[0].Job != "Director" || credits.Crew[1].Job != "Screenplay" {
		t.Errorf("unexpected crew jobs: %+v", credits.Crew)
	}
}

func TestTVAggregateFullCredits_ReadsRolesAndJobsArrays(t *testing.T) {
	// TV's aggregate_credits nests character under roles[] and job under jobs[].
	const body = `{
	  "cast": [
	    {"name": "Star", "profile_path": "/s.jpg", "roles": [{"character": "Captain"}]}
	  ],
	  "crew": [
	    {"name": "Showrunner", "department": "Production", "profile_path": "/sr.jpg", "jobs": [{"job": "Producer"}]},
	    {"name": "Boom Op", "department": "Sound", "profile_path": "/bo.jpg", "jobs": [{"job": "Boom Operator"}]}
	  ]
	}`
	c, path, _ := recordingClient(t, body)
	credits, err := c.TVAggregateFullCredits(context.Background(), 7)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if *path != "/tv/7/aggregate_credits" {
		t.Errorf("unexpected path: %s", *path)
	}
	if len(credits.Cast) != 1 || credits.Cast[0].Character != "Captain" {
		t.Errorf("expected character read from roles[0], got %+v", credits.Cast)
	}
	if len(credits.Crew) != 1 || credits.Crew[0].Job != "Producer" {
		t.Errorf("expected only the key-role Producer from jobs[0], got %+v", credits.Crew)
	}
}

func TestMovieKeywords_ReadsKeywordsArray(t *testing.T) {
	c, path, _ := recordingClient(t, `{"keywords": [{"name": "heist"}, {"name": "revenge"}]}`)
	kw, err := c.MovieKeywords(context.Background(), 42)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if *path != "/movie/42/keywords" {
		t.Errorf("unexpected path: %s", *path)
	}
	if len(kw) != 2 || kw[0] != "heist" || kw[1] != "revenge" {
		t.Errorf("unexpected keywords: %v", kw)
	}
}

func TestTVKeywords_ReadsResultsArray(t *testing.T) {
	// TV's /keywords nests under results[], not keywords[] — the shape difference.
	c, path, _ := recordingClient(t, `{"results": [{"name": "space"}, {"name": "drama"}]}`)
	kw, err := c.TVKeywords(context.Background(), 7)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if *path != "/tv/7/keywords" {
		t.Errorf("unexpected path: %s", *path)
	}
	if len(kw) != 2 || kw[0] != "space" {
		t.Errorf("unexpected keywords: %v", kw)
	}
}

func TestMovieWatchProviders_ReadsUSFlatrateOnly(t *testing.T) {
	// GB present but must be ignored; US rent/buy present but only flatrate read.
	const body = `{"results": {
	  "US": {
	    "flatrate": [{"provider_name": "Netflix", "logo_path": "/nf.jpg"}],
	    "rent": [{"provider_name": "Apple TV", "logo_path": "/at.jpg"}]
	  },
	  "GB": {"flatrate": [{"provider_name": "BBC", "logo_path": "/bbc.jpg"}]}
	}}`
	c, path, _ := recordingClient(t, body)
	providers, err := c.MovieWatchProviders(context.Background(), 42)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if *path != "/movie/42/watch/providers" {
		t.Errorf("unexpected path: %s", *path)
	}
	if len(providers) != 1 || providers[0].Name != "Netflix" || providers[0].LogoPath != "/nf.jpg" {
		t.Errorf("expected only the US flatrate provider, got %+v", providers)
	}
}

func TestMovieRecommendations_NormalizesAndPaginates(t *testing.T) {
	c, path, query := recordingClient(t, `{"results": [{"id": 99, "title": "Similar Movie", "poster_path": "/x.jpg", "release_date": "2021-01-01", "vote_average": 6.6}]}`)
	items, err := c.MovieRecommendations(context.Background(), 42, 2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if *path != "/movie/42/recommendations" {
		t.Errorf("unexpected path: %s", *path)
	}
	if query.Get("page") != "2" {
		t.Errorf("expected page=2, got %q", query.Get("page"))
	}
	if len(items) != 1 || items[0].Title != "Similar Movie" || items[0].MediaType != Movie {
		t.Errorf("unexpected items: %+v", items)
	}
}

func TestMovieDetails_ExtendedFields(t *testing.T) {
	const body = `{
	  "id": 42, "title": "A Movie", "poster_path": "/p.jpg", "imdb_id": "tt1", "runtime": 100,
	  "release_date": "2023-05-01", "status": "Released", "original_language": "en",
	  "genres": [{"name": "Action"}],
	  "belongs_to_collection": {"id": 500, "name": "A Collection"},
	  "production_companies": [{"name": "Studio One"}, {"name": "Studio Two"}],
	  "production_countries": [{"iso_3166_1": "US", "name": "United States of America"}],
	  "release_dates": {"results": [
	    {"iso_3166_1": "US", "release_dates": [
	      {"type": 3, "release_date": "2023-05-01T00:00:00.000Z"},
	      {"type": 4, "release_date": "2023-07-01T00:00:00.000Z"}
	    ]},
	    {"iso_3166_1": "GB", "release_dates": [{"type": 3, "release_date": "2023-06-01T00:00:00.000Z"}]}
	  ]}
	}`
	c, path, query := recordingClient(t, body)
	d, err := c.MovieDetails(context.Background(), 42)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if *path != "/movie/42" {
		t.Errorf("unexpected path: %s", *path)
	}
	if query.Get("append_to_response") != "release_dates" {
		t.Errorf("expected append_to_response=release_dates, got %q", query.Get("append_to_response"))
	}
	if d.Status != "Released" || d.OriginalLanguage != "en" {
		t.Errorf("unexpected status/lang: %+v", d)
	}
	if d.ProductionCountry != "United States of America" || d.ProductionCountryCode != "US" {
		t.Errorf("unexpected production country: %+v", d)
	}
	if len(d.Studios) != 2 || d.Studios[0] != "Studio One" {
		t.Errorf("unexpected studios: %v", d.Studios)
	}
	if d.Collection.ID != 500 || d.Collection.Name != "A Collection" {
		t.Errorf("unexpected collection: %+v", d.Collection)
	}
	// US-only: the two US entries survive, the GB one is dropped.
	if len(d.ReleaseDates) != 2 || d.ReleaseDates[0].Type != 3 || d.ReleaseDates[1].Type != 4 {
		t.Errorf("expected 2 US release dates, got %+v", d.ReleaseDates)
	}
}

func TestTVDetails_ExtendedFields(t *testing.T) {
	const body = `{
	  "id": 7, "name": "A Show", "poster_path": "/p.jpg", "status": "Returning Series",
	  "overview": "A show synopsis.",
	  "original_language": "ja", "episode_run_time": [24, 25],
	  "genres": [{"name": "Animation"}],
	  "networks": [{"name": "Some Network"}],
	  "production_countries": [{"iso_3166_1": "JP", "name": "Japan"}],
	  "origin_country": ["JP"]
	}`
	c, path, _ := recordingClient(t, body)
	d, err := c.TVDetails(context.Background(), 7)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if *path != "/tv/7" {
		t.Errorf("unexpected path: %s", *path)
	}
	if d.Title != "A Show" || d.Status != "Returning Series" || d.OriginalLanguage != "ja" {
		t.Errorf("unexpected base/extended fields: %+v", d)
	}
	if d.Overview != "A show synopsis." {
		t.Errorf("unexpected overview: %q", d.Overview)
	}
	if d.Runtime != 24 {
		t.Errorf("expected Runtime from episode_run_time[0]=24, got %d", d.Runtime)
	}
	if len(d.Networks) != 1 || d.Networks[0] != "Some Network" {
		t.Errorf("unexpected networks: %v", d.Networks)
	}
	if d.ProductionCountry != "Japan" || d.ProductionCountryCode != "JP" {
		t.Errorf("unexpected production country: %+v", d)
	}
}

func TestDiscoverFiltered_DateRangeQuery(t *testing.T) {
	c, _, query := recordingClient(t, `{"results": []}`)
	_, err := c.DiscoverMoviesFiltered(context.Background(), FilterOptions{DateFrom: "2026-07-01", DateTo: "2026-07-31"}, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if query.Get("primary_release_date.gte") != "2026-07-01" || query.Get("primary_release_date.lte") != "2026-07-31" {
		t.Errorf("expected movie primary_release_date range, got %v", *query)
	}
	// No sort_by set → no today-cap collision on .lte.
	if query.Get("sort_by") != "" {
		t.Errorf("expected no sort_by for a bare date-range browse, got %q", query.Get("sort_by"))
	}

	c2, _, query2 := recordingClient(t, `{"results": []}`)
	_, err = c2.DiscoverTVFiltered(context.Background(), FilterOptions{DateFrom: "2026-07-01", DateTo: "2026-07-31"}, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if query2.Get("first_air_date.gte") != "2026-07-01" || query2.Get("first_air_date.lte") != "2026-07-31" {
		t.Errorf("expected tv first_air_date range, got %v", *query2)
	}
}

// upcomingClient serves digitalBody to DiscoverMoviesUpcoming's query A (the
// with_release_type one) and plannedBody to query B, recording every request's
// query in order. recordingClient above can't be reused here: it keeps only the
// LAST request, and this method issues two.
func upcomingClient(t *testing.T, digitalBody, plannedBody string) (*Client, *[]url.Values, *[]string) {
	t.Helper()
	var queries []url.Values
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		queries = append(queries, r.URL.Query())
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("with_release_type") != "" {
			w.Write([]byte(digitalBody))
			return
		}
		w.Write([]byte(plannedBody))
	}))
	t.Cleanup(srv.Close)
	c := New(Config{BaseURL: srv.URL, APIKey: "test-key"}, srv.Client())
	// Per-client cache, same reason as the two helpers above.
	c.cache = newCache(defaultCacheCap, defaultCacheTTL)
	return c, &queries, &paths
}

// TestDiscoverMoviesUpcoming_QueryShapes proves the two queries §4.1's table
// specifies actually go out: A carries region + pipe-joined release types + a
// release_date window and no primary_release_date bound; B is the plain
// primary_release_date window the existing calendar browse already issues,
// with neither of the new params on it.
func TestDiscoverMoviesUpcoming_QueryShapes(t *testing.T) {
	c, queries, paths := upcomingClient(t, `{"results": []}`, `{"results": []}`)
	if _, err := c.DiscoverMoviesUpcoming(context.Background(), "2026-09-01", "2026-09-30"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(*queries) != 2 {
		t.Fatalf("expected exactly 2 TMDB calls (A + B, page 1 only), got %d", len(*queries))
	}
	for _, p := range *paths {
		if p != "/discover/movie" {
			t.Errorf("expected both calls on /discover/movie, got %q", p)
		}
	}

	a := (*queries)[0]
	if a.Get("region") != UpcomingRegion {
		t.Errorf("query A: expected region=%s, got %q", UpcomingRegion, a.Get("region"))
	}
	if a.Get("with_release_type") != "4|5" {
		t.Errorf("query A: expected pipe-joined with_release_type=4|5, got %q", a.Get("with_release_type"))
	}
	if a.Get("release_date.gte") != "2026-09-01" || a.Get("release_date.lte") != "2026-09-30" {
		t.Errorf("query A: expected the release_date window, got %v", a)
	}
	if a.Has("primary_release_date.gte") || a.Has("primary_release_date.lte") {
		t.Errorf("query A: expected no primary_release_date bound, got %v", a)
	}
	if a.Has("sort_by") {
		t.Errorf("query A: expected no sort_by (the newest sort's own cap would collide with the window), got %q", a.Get("sort_by"))
	}

	b := (*queries)[1]
	if b.Get("primary_release_date.gte") != "2026-09-01" || b.Get("primary_release_date.lte") != "2026-09-30" {
		t.Errorf("query B: expected the primary_release_date window, got %v", b)
	}
	if b.Has("region") || b.Has("with_release_type") || b.Has("release_date.gte") {
		t.Errorf("query B must stay byte-identical to today's calendar browse, got %v", b)
	}
}

// TestDiscoverMoviesUpcoming_MergeAWinsOnCollision covers the merge rules: an
// id in both queries keeps A's typed date and is tagged "digital"; an id only
// in B is tagged "planned" (the "has release_dates but no type-4/5 entry" case,
// since TMDB itself filters A on those ids); no id appears twice; and the order
// is deterministic — A's results in order, then B-only ones.
func TestDiscoverMoviesUpcoming_MergeAWinsOnCollision(t *testing.T) {
	const digitalBody = `{"results": [
	  {"id": 10, "title": "In Both", "poster_path": "/both.jpg", "release_date": "2026-09-15"},
	  {"id": 11, "title": "Digital Only", "release_date": "2026-09-20"}
	]}`
	const plannedBody = `{"results": [
	  {"id": 10, "title": "In Both", "release_date": "2026-06-01"},
	  {"id": 12, "title": "Planned Only", "release_date": "2026-09-05"}
	]}`
	c, _, _ := upcomingClient(t, digitalBody, plannedBody)
	got, err := c.DiscoverMoviesUpcoming(context.Background(), "2026-09-01", "2026-09-30")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []UpcomingMovie{
		{Item: Item{ID: 10, Title: "In Both", PosterPath: "/both.jpg", ReleaseDate: "2026-09-15", MediaType: Movie}, ReleaseKind: ReleaseKindDigital},
		{Item: Item{ID: 11, Title: "Digital Only", ReleaseDate: "2026-09-20", MediaType: Movie}, ReleaseKind: ReleaseKindDigital},
		{Item: Item{ID: 12, Title: "Planned Only", ReleaseDate: "2026-09-05", MediaType: Movie}, ReleaseKind: ReleaseKindPlanned},
	}
	if len(got) != len(want) {
		t.Fatalf("expected %d merged items (id 10 deduped), got %d: %+v", len(want), len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("item %d:\n got %+v\nwant %+v", i, got[i], want[i])
		}
	}
}

// TestDiscoverMoviesUpcoming_PropagatesQueryError proves a failed query is an
// error, not a silent partial result — "fallback" in §4.1 describes B's DATA
// coverage (planned-only titles), never error recovery for A.
func TestDiscoverMoviesUpcoming_PropagatesQueryError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("with_release_type") != "" {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"results": [{"id": 12, "title": "Planned Only"}]}`))
	}))
	t.Cleanup(srv.Close)
	c := New(Config{BaseURL: srv.URL, APIKey: "test-key"}, srv.Client())
	c.cache = newCache(defaultCacheCap, defaultCacheTTL)

	items, err := c.DiscoverMoviesUpcoming(context.Background(), "2026-09-01", "2026-09-30")
	if err == nil {
		t.Fatalf("expected an error when query A fails, got %d items", len(items))
	}
	if items != nil {
		t.Errorf("expected no items alongside the error, got %+v", items)
	}
}
