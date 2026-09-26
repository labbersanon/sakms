package prowlarr

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func newTestClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return New(Config{BaseURL: srv.URL, APIKey: "test-key"}, srv.Client())
}

// searchFixture is a plausible (but not live-confirmed — see package doc)
// /api/v1/search response spanning both protocols.
const searchFixture = `[
  {
    "guid": "prowlarr-guid-1",
    "title": "Some.Movie.2023.1080p.WEB-DL.x264-GROUP",
    "indexer": "SomeTorrentIndexer",
    "protocol": "torrent",
    "size": 4294967296,
    "seeders": 42,
    "downloadUrl": "https://indexer.example/download/1.torrent",
    "publishDate": "2023-05-01T00:00:00Z",
    "categories": [{"id": 2000}, {"id": 2040}],
    "indexerFlags": ["freeleech"]
  },
  {
    "guid": "prowlarr-guid-2",
    "title": "Some.Movie.2023.2160p.WEB-DL.x265-GROUP",
    "indexer": "SomeUsenetIndexer",
    "protocol": "usenet",
    "size": 8589934592,
    "seeders": 0,
    "downloadUrl": "https://indexer.example/download/2.nzb",
    "publishDate": "2023-05-02T00:00:00Z",
    "categories": [{"id": 2000}]
  }
]`

func TestSearch_ParsesFixtureAcrossBothProtocols(t *testing.T) {
	var gotPath string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.String()
		if r.Header.Get("X-Api-Key") != "test-key" {
			t.Error("missing X-Api-Key header")
		}
		w.Write([]byte(searchFixture))
	})

	releases, err := c.Search(context.Background(), "Some Movie 2023", []int{2000})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(releases) != 2 {
		t.Fatalf("expected 2 releases, got %d", len(releases))
	}
	if releases[0].Protocol != Torrent || releases[0].Seeders != 42 {
		t.Errorf("unexpected first release: %+v", releases[0])
	}
	if len(releases[0].IndexerFlags) != 1 || releases[0].IndexerFlags[0] != "freeleech" {
		t.Errorf("expected indexerFlags to parse, got %+v", releases[0].IndexerFlags)
	}
	if releases[1].Protocol != Usenet {
		t.Errorf("unexpected second release: %+v", releases[1])
	}
	if !strings.Contains(gotPath, "query=Some+Movie+2023") {
		t.Errorf("expected query param in request path, got %q", gotPath)
	}
	if !strings.Contains(gotPath, "categories=2000") {
		t.Errorf("expected categories param in request path, got %q", gotPath)
	}
}

// Claude 2026-08-11: cover Prowlarr's alternate torrent enclosure fields.
// Reason: magnet-only releases must remain grabbable, while downloadUrl keeps
// precedence when Prowlarr supplies both representations.
// Troubleshooting: reproduces the Adult DetailPopup empty-downloadUrl failure.
// Review if: releaseResource no longer accepts magnetUrl or magnet GUIDs.
func TestSearch_DownloadURLFallbacksToMagnetURLThenMagnetGUID(t *testing.T) {
	const downloadURL = "https://indexer.example/download/1.torrent"
	const magnetURL = "magnet:?xt=urn:btih:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	const guidMagnet = "magnet:?xt=urn:btih:BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB"
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[
			{"guid":"guid-1","title":"download wins","protocol":"torrent","downloadUrl":"` + downloadURL + `","magnetUrl":"` + magnetURL + `"},
			{"guid":"guid-2","title":"magnet fallback","protocol":"torrent","magnetUrl":"` + magnetURL + `"},
			{"guid":"` + guidMagnet + `","title":"guid fallback","protocol":"torrent"},
			{"guid":"ordinary-guid","title":"no enclosure","protocol":"torrent"}
		]`))
	})

	releases, err := c.Search(context.Background(), "anything", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(releases) != 4 {
		t.Fatalf("expected 4 releases, got %d", len(releases))
	}
	if got := releases[0].DownloadURL; got != downloadURL {
		t.Errorf("downloadUrl precedence = %q, want %q", got, downloadURL)
	}
	if got := releases[1].DownloadURL; got != magnetURL {
		t.Errorf("magnetUrl fallback = %q, want %q", got, magnetURL)
	}
	if got := releases[2].DownloadURL; got != guidMagnet {
		t.Errorf("magnet guid fallback = %q, want %q", got, guidMagnet)
	}
	if got := releases[3].DownloadURL; got != "" {
		t.Errorf("ordinary guid must not become a download URL, got %q", got)
	}
}

func TestSearch_NoCategoriesOmitsParam(t *testing.T) {
	var gotPath string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.String()
		w.Write([]byte(`[]`))
	})

	if _, err := c.Search(context.Background(), "anything", nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(gotPath, "categories") {
		t.Errorf("expected no categories param when none given, got %q", gotPath)
	}
}

func TestSearch_PropagatesErrorStatus(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})

	if _, err := c.Search(context.Background(), "anything", nil); err == nil {
		t.Fatal("expected an error for a 401 response")
	}
}

// TestSearch_QueryStringUnaffectedByStructuredSearch guards the refactor:
// factoring the shared do+parse helper must leave Search's exact wire
// contract (type=search + query + no structured params) byte-identical.
func TestSearch_QueryStringUnaffectedByStructuredSearch(t *testing.T) {
	var gotPath string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.String()
		w.Write([]byte(`[]`))
	})

	if _, err := c.Search(context.Background(), "Some Movie", []int{2000}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(gotPath, "type=search") {
		t.Errorf("expected type=search, got %q", gotPath)
	}
	if !strings.Contains(gotPath, "query=Some+Movie") {
		t.Errorf("expected query param, got %q", gotPath)
	}
	for _, structured := range []string{"tmdbid", "imdbid", "tvdbid", "season", "ep="} {
		if strings.Contains(gotPath, structured) {
			t.Errorf("free-text Search leaked structured param %q: %q", structured, gotPath)
		}
	}
}

// TestSearchByID is the regression test for a real "picking Season 4 grabs
// S1E1" bug: Prowlarr's /api/v1/search does NOT read standalone
// tmdbid=/imdbid=/tvdbid=/season=/ep= params at all — it only extracts ids
// from bracketed tokens ({TmdbId:550}, {ImdbId:tt0137523}, {TvdbId:81189},
// {Season:4}, {Episode:5}) embedded in the free-text query= string (see
// SearchByID's doc comment for the confirmed Prowlarr source reference).
// Every case below asserts the EXACT resulting query= value (via the
// server's parsed r.URL.Query(), not raw path substring matching, since
// brace tokens URL-encode into "%7B...%7D" which is unreadable to match
// against directly) and separately proves none of the old top-level params
// ever appear on the wire again.
func TestSearchByID(t *testing.T) {
	tests := []struct {
		name      string
		params    SearchByIDParams
		wantType  string
		wantQuery string
		wantCats  string // "" means don't check
	}{
		{
			name:      "TMDBID only routes to movie search",
			params:    SearchByIDParams{TMDBID: 550, Categories: []int{2000}},
			wantType:  "movie",
			wantQuery: "{TmdbId:550}",
			wantCats:  "2000",
		},
		{
			// Regression for a real "nothing is being found to grab" bug: an
			// id-only request (no free-text title) wasn't reliably honored
			// as a precise filter by every indexer — some fall back to
			// Torznab's "empty query = list recent releases" RSS-style
			// behavior, silently ignoring the id params. The title must
			// travel ALONGSIDE the brace tokens, not replace them.
			name:      "Query travels alongside id braces, not instead of them",
			params:    SearchByIDParams{Query: "Moana", TMDBID: 550, Categories: []int{2000}},
			wantType:  "movie",
			wantQuery: "{TmdbId:550} Moana",
			wantCats:  "2000",
		},
		{
			name:      "IMDBID with tt prefix routes to movie search, tt kept in the brace",
			params:    SearchByIDParams{IMDBID: "tt0137523"},
			wantType:  "movie",
			wantQuery: "{ImdbId:tt0137523}",
		},
		{
			name:      "IMDBID without a tt prefix gets tt added for the brace form",
			params:    SearchByIDParams{IMDBID: "0137523"},
			wantType:  "movie",
			wantQuery: "{ImdbId:tt0137523}",
		},
		{
			name:      "TVDBID with season and episode routes to tv search",
			params:    SearchByIDParams{TVDBID: 81189, Season: 2, SeasonSpecified: true, Episode: 5},
			wantType:  "tvsearch",
			wantQuery: "{TvdbId:81189} {Season:2} {Episode:5}",
		},
		{
			// Season 0 is Specials — a real, deliberate scope, distinct from
			// "no season was picked at all" (Season's own zero value). It
			// must still produce a {Season:0} token when SeasonSpecified is
			// true.
			name:      "Season 0 (Specials) is included when SeasonSpecified is true",
			params:    SearchByIDParams{TVDBID: 81189, SeasonSpecified: true},
			wantType:  "tvsearch",
			wantQuery: "{TvdbId:81189} {Season:0}",
		},
		{
			// The inverse: a nonzero Season number with SeasonSpecified left
			// false must NOT produce a {Season:...} token at all — this is
			// what lets an unscoped whole-show probe stay unscoped even if a
			// caller happens to carry a stale Season number.
			name:      "Season omitted entirely when SeasonSpecified is false, even with a nonzero Season number",
			params:    SearchByIDParams{TVDBID: 81189, Season: 4},
			wantType:  "tvsearch",
			wantQuery: "{TvdbId:81189}",
		},
		{
			name:      "Query travels alongside braces for a TV search too",
			params:    SearchByIDParams{Query: "Some Show", TVDBID: 81189, Season: 4, SeasonSpecified: true},
			wantType:  "tvsearch",
			wantQuery: "{TvdbId:81189} {Season:4} Some Show",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotType, gotQuery, gotCats, gotRawPath string
			c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				gotType = r.URL.Query().Get("type")
				gotQuery = r.URL.Query().Get("query")
				gotCats = r.URL.Query().Get("categories")
				gotRawPath = r.URL.String()
				if r.Header.Get("X-Api-Key") != "test-key" {
					t.Error("missing X-Api-Key header")
				}
				w.Write([]byte(searchFixture))
			})

			releases, err := c.SearchByID(context.Background(), tt.params)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			// Shared parse path must yield the same Release shape as Search.
			if len(releases) != 2 || releases[0].Protocol != Torrent || releases[1].Protocol != Usenet {
				t.Errorf("unexpected releases from shared parse path: %+v", releases)
			}
			if gotType != tt.wantType {
				t.Errorf("expected type=%q, got %q", tt.wantType, gotType)
			}
			if gotQuery != tt.wantQuery {
				t.Errorf("expected query=%q, got %q (raw path %q)", tt.wantQuery, gotQuery, gotRawPath)
			}
			if tt.wantCats != "" && gotCats != tt.wantCats {
				t.Errorf("expected categories=%q, got %q", tt.wantCats, gotCats)
			}
			// The core regression check: none of the old top-level
			// structured params may ever appear on the wire again —
			// Prowlarr's /api/v1/search silently ignores every one of them.
			for _, oldParam := range []string{"tmdbid=", "imdbid=", "tvdbid=", "season=", "ep="} {
				if strings.Contains(gotRawPath, oldParam) {
					t.Errorf("old top-level structured param %q leaked into the request: %q", oldParam, gotRawPath)
				}
			}
		})
	}
}

// TestSearchByID_IndexerScope covers the three Scope sentinels and the explicit
// IndexerIDs override. Acceptance criteria from the plan:
//   - ScopeUsenet emits exactly one indexerIds=-1
//   - ScopeTorrent emits exactly one indexerIds=-2
//   - ScopeAll (zero value) emits no indexerIds at all
//   - Explicit IndexerIDs{3,7} emits two repeated params, suppresses sentinel
//   - A zero-value SearchByIDParams produces a byte-identical query to today
func TestSearchByID_IndexerScope(t *testing.T) {
	tests := []struct {
		name             string
		params           SearchByIDParams
		wantIndexerIDs   []string // all expected indexerIds values
		wantNoIndexerIDs bool     // true when no indexerIds param at all
	}{
		{
			name:             "ScopeAll (zero value) emits no indexerIds param",
			params:           SearchByIDParams{TMDBID: 550, Scope: ScopeAll},
			wantNoIndexerIDs: true,
		},
		{
			name:           "ScopeUsenet emits indexerIds=-1",
			params:         SearchByIDParams{TMDBID: 550, Scope: ScopeUsenet},
			wantIndexerIDs: []string{"-1"},
		},
		{
			name:           "ScopeTorrent emits indexerIds=-2",
			params:         SearchByIDParams{TMDBID: 550, Scope: ScopeTorrent},
			wantIndexerIDs: []string{"-2"},
		},
		{
			name:           "explicit IndexerIDs wins over Scope, repeated not comma-joined",
			params:         SearchByIDParams{TMDBID: 550, Scope: ScopeUsenet, IndexerIDs: []int{3, 7}},
			wantIndexerIDs: []string{"3", "7"},
		},
		{
			// Regression: a zero-value SearchByIDParams (every existing caller's shape)
			// must produce no indexerIds param — the wire contract is unchanged.
			name:             "zero-value params produce no indexerIds (regression guard)",
			params:           SearchByIDParams{TMDBID: 1},
			wantNoIndexerIDs: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotRaw string
			c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				gotRaw = r.URL.RawQuery
				w.Write([]byte(searchFixture))
			})

			if _, err := c.SearchByID(context.Background(), tt.params); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			// Parse the query so we get all repeated values for indexerIds.
			parsed, err := url.ParseQuery(gotRaw)
			if err != nil {
				t.Fatalf("could not parse raw query %q: %v", gotRaw, err)
			}

			got := parsed["indexerIds"]
			if tt.wantNoIndexerIDs {
				if len(got) > 0 {
					t.Errorf("expected no indexerIds param, got %v (raw: %s)", got, gotRaw)
				}
				return
			}
			if len(got) != len(tt.wantIndexerIDs) {
				t.Errorf("indexerIds count: want %v, got %v (raw: %s)", tt.wantIndexerIDs, got, gotRaw)
				return
			}
			for i, want := range tt.wantIndexerIDs {
				if got[i] != want {
					t.Errorf("indexerIds[%d]: want %q, got %q (raw: %s)", i, want, got[i], gotRaw)
				}
			}
		})
	}
}

func TestSearchByID_PropagatesErrorStatus(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})

	if _, err := c.SearchByID(context.Background(), SearchByIDParams{TMDBID: 1}); err == nil {
		t.Fatal("expected an error for a 401 response")
	}
}

func TestSearchByID_MovieEmptyFallsBackToTitleSearch(t *testing.T) {
	var calls int
	var secondType, secondQuery, secondCats string
	var secondIDs []string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		q := r.URL.Query()
		if calls == 1 {
			if q.Get("type") != "movie" {
				t.Errorf("first request type: want movie, got %q", q.Get("type"))
			}
			if !strings.Contains(q.Get("query"), "{TmdbId:594328}") {
				t.Errorf("first request should keep ID tokens, got %q", q.Get("query"))
			}
			w.Write([]byte("[]"))
			return
		}
		secondType = q.Get("type")
		secondQuery = q.Get("query")
		secondCats = q.Get("categories")
		secondIDs = q["indexerIds"]
		w.Write([]byte(searchFixture))
	})

	releases, err := c.SearchByID(context.Background(), SearchByIDParams{
		Query:      "Phineas and Ferb the Movie",
		TMDBID:     594328,
		IMDBID:     "tt1817232",
		Categories: []int{2000},
		Scope:      ScopeUsenet,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if calls != 2 {
		t.Fatalf("expected a type=search retry after empty type=movie, got %d calls", calls)
	}
	if secondType != "search" {
		t.Errorf("fallback type: want search, got %q", secondType)
	}
	if secondQuery != "Phineas and Ferb the Movie" {
		t.Errorf("fallback query should be the title only, got %q", secondQuery)
	}
	if strings.Contains(secondQuery, "{") {
		t.Errorf("fallback must not send ID tokens, got %q", secondQuery)
	}
	if secondCats != "2000" {
		t.Errorf("fallback categories: want 2000, got %q", secondCats)
	}
	if len(secondIDs) != 1 || secondIDs[0] != "-1" {
		t.Errorf("fallback should keep ScopeUsenet indexerIds=-1, got %v", secondIDs)
	}
	if len(releases) != 2 {
		t.Errorf("expected fallback hits, got %d", len(releases))
	}
}

func TestSearchByID_TVEmptyDoesNotFallBack(t *testing.T) {
	var calls int
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Query().Get("type") != "tvsearch" {
			t.Errorf("TV search type: want tvsearch, got %q", r.URL.Query().Get("type"))
		}
		w.Write([]byte("[]"))
	})

	releases, err := c.SearchByID(context.Background(), SearchByIDParams{
		Query:           "Some Show",
		TVDBID:          81189,
		Season:          4,
		SeasonSpecified: true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if calls != 1 {
		t.Fatalf("TV empty search must not retry as type=search, got %d calls", calls)
	}
	if len(releases) != 0 {
		t.Errorf("expected empty TV result, got %d", len(releases))
	}
}

func TestSearchByID_MovieEmptyWithoutTitleDoesNotFallBack(t *testing.T) {
	var calls int
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Write([]byte("[]"))
	})

	releases, err := c.SearchByID(context.Background(), SearchByIDParams{TMDBID: 550})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if calls != 1 {
		t.Fatalf("title-less movie miss must not retry, got %d calls", calls)
	}
	if len(releases) != 0 {
		t.Errorf("expected empty result, got %d", len(releases))
	}
}
