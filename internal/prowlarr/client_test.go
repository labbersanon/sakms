package prowlarr

import (
	"context"
	"net/http"
	"net/http/httptest"
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

func TestSearchByID_PropagatesErrorStatus(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})

	if _, err := c.SearchByID(context.Background(), SearchByIDParams{TMDBID: 1}); err == nil {
		t.Fatal("expected an error for a 401 response")
	}
}
