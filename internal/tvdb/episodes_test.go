package tvdb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
)

// episodePages is the fixture shape for the fake episode-listing server: the
// outer index is the `page` query value, and each inner slice is that page's
// `data.episodes` array. A page beyond the fixture's length answers with an
// empty episodes array, which is SeriesEpisodes' exhaustion signal.
type episodePages [][]episodeItem

// newEpisodeServer builds a TVDB v4 httptest server answering POST /v4/login
// and GET /v4/series/{id}/episodes/{type}, in the same shape as
// client_test.go's newTestServer (which is deliberately left untouched).
// The returned pointer collects, in order, every `page` value the handler
// saw, so a test can assert the pagination walk itself.
func newEpisodeServer(t *testing.T, pages episodePages) (*httptest.Server, *[]int) {
	t.Helper()
	var mu sync.Mutex
	seen := []int{}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v4/login", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"status":"success","data":{"token":"tok"}}`)
	})
	mux.HandleFunc("GET /v4/series/{id}/episodes/{type}", func(w http.ResponseWriter, r *http.Request) {
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		mu.Lock()
		seen = append(seen, page)
		mu.Unlock()
		var items []episodeItem
		if page >= 0 && page < len(pages) {
			items = pages[page]
		}
		writeEpisodePage(t, w, items)
	})
	return httptest.NewServer(mux), &seen
}

// writeEpisodePage emits the live-confirmed envelope:
// {status, data:{series, episodes[]}, links}. The `links` block is written
// with plausible values on purpose — SeriesEpisodes must ignore it entirely
// and terminate on an empty episodes array instead.
func writeEpisodePage(t *testing.T, w http.ResponseWriter, items []episodeItem) {
	t.Helper()
	if items == nil {
		items = []episodeItem{}
	}
	body := map[string]any{
		"status": "success",
		"data": map[string]any{
			"series":   map[string]any{"id": 73910, "name": "Laurel & Hardy", "year": "1921"},
			"episodes": items,
		},
		"links": map[string]any{
			"prev": nil, "self": 0, "next": nil,
			"total_items": len(items), "page_size": 500,
		},
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(body); err != nil {
		t.Fatalf("encoding fixture page: %v", err)
	}
}

func newEpisodeClient(t *testing.T, srv *httptest.Server) *Client {
	t.Helper()
	return New(Config{BaseURL: srv.URL, APIKey: "k"}, srv.Client())
}

// T1a — the wire-format test. Every other case in this file builds its
// fixture by marshaling []episodeItem, which round-trips through the SAME
// json tags the decoder reads, so those tags cancel out and a renamed tag
// would go unnoticed: the fixture would emit the typo, the decoder would
// read the typo, and the test would pass while the real API silently
// decoded to zero values (then vanished through the Number<=0 filter as an
// "empty catalog"). So this one serves a RAW JSON literal, with the field
// names and the number-not-string types transcribed from TheTVDB's v4
// EpisodeBaseRecord. This is the test that pins the number-vs-string
// decoding asymmetry against /search's searchItem; it follows
// client_test.go:23-32's own hand-written-JSON precedent, for the same
// reason that file states.
func TestSeriesEpisodes_DecodesRealWireFormat(t *testing.T) {
	const page0 = `{"status":"success","data":{` +
		`"series":{"id":73910,"name":"Laurel & Hardy","year":"1921"},` +
		`"episodes":[{"id":1001,"seriesId":73910,"name":"Duck Soup","number":1,` +
		`"seasonNumber":3,"aired":"1927-03-13","runtime":20}]},` +
		`"links":{"prev":null,"self":0,"next":null,"total_items":1,"page_size":500}}`
	const pageEmpty = `{"status":"success","data":{` +
		`"series":{"id":73910,"name":"Laurel & Hardy","year":"1921"},"episodes":[]},` +
		`"links":{"prev":null,"self":1,"next":null,"total_items":1,"page_size":500}}`

	mux := http.NewServeMux()
	mux.HandleFunc("POST /v4/login", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"status":"success","data":{"token":"tok"}}`)
	})
	mux.HandleFunc("GET /v4/series/{id}/episodes/{type}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("page") == "0" {
			fmt.Fprint(w, page0)
			return
		}
		fmt.Fprint(w, pageEmpty)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	got, err := newEpisodeClient(t, srv).SeriesEpisodes(context.Background(), 73910, SeasonTypeOfficial)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 episode from the raw wire fixture, got %d (%+v)", len(got), got)
	}
	want := Episode{
		ID: 1001, SeriesID: 73910, Name: "Duck Soup",
		Number: 1, SeasonNumber: 3, Aired: "1927-03-13", Runtime: 20,
	}
	if got[0] != want {
		t.Errorf("wire decode: want %+v, got %+v", want, got[0])
	}
}

// T1b — a single page of 3 episodes, decoded in order. The wire field names
// themselves are pinned by TestSeriesEpisodes_DecodesRealWireFormat above,
// not here.
func TestSeriesEpisodes_SinglePageDecodesNumericFields(t *testing.T) {
	srv, seen := newEpisodeServer(t, episodePages{{
		{ID: 1001, SeriesID: 73910, Name: "Duck Soup", Number: 1, SeasonNumber: 3, Aired: "1927-03-13", Runtime: 20},
		{ID: 1002, SeriesID: 73910, Name: "The Music Box", Number: 3, SeasonNumber: 8, Aired: "1932-04-16", Runtime: 29},
		{ID: 1003, SeriesID: 73910, Name: "Angora Love", Number: 12, SeasonNumber: 5, Aired: "1929-12-14", Runtime: 21},
	}})
	defer srv.Close()

	got, err := newEpisodeClient(t, srv).SeriesEpisodes(context.Background(), 73910, SeasonTypeOfficial)
	if err != nil {
		t.Fatal(err)
	}
	want := []Episode{
		{ID: 1001, SeriesID: 73910, Name: "Duck Soup", Number: 1, SeasonNumber: 3, Aired: "1927-03-13", Runtime: 20},
		{ID: 1002, SeriesID: 73910, Name: "The Music Box", Number: 3, SeasonNumber: 8, Aired: "1932-04-16", Runtime: 29},
		{ID: 1003, SeriesID: 73910, Name: "Angora Love", Number: 12, SeasonNumber: 5, Aired: "1929-12-14", Runtime: 21},
	}
	if len(got) != len(want) {
		t.Fatalf("expected %d episodes, got %d (%+v)", len(want), len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("episode %d: want %+v, got %+v", i, want[i], got[i])
		}
	}
	if len(*seen) != 2 || (*seen)[0] != 0 {
		t.Errorf("expected pages [0 1] (0 then the exhausting page), got %v", *seen)
	}
}

// T2 — two populated pages then an empty one. Asserts both the concatenated
// order and that the walk started at page 0 and advanced by one.
func TestSeriesEpisodes_PaginatesUntilEmptyPage(t *testing.T) {
	srv, seen := newEpisodeServer(t, episodePages{
		{
			{ID: 1, SeriesID: 7, Name: "A", Number: 1, SeasonNumber: 1},
			{ID: 2, SeriesID: 7, Name: "B", Number: 2, SeasonNumber: 1},
		},
		{
			{ID: 3, SeriesID: 7, Name: "C", Number: 1, SeasonNumber: 2},
			{ID: 4, SeriesID: 7, Name: "D", Number: 2, SeasonNumber: 2},
		},
	})
	defer srv.Close()

	got, err := newEpisodeClient(t, srv).SeriesEpisodes(context.Background(), 7, SeasonTypeOfficial)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 {
		t.Fatalf("expected 4 episodes across 2 pages, got %d (%+v)", len(got), got)
	}
	for i, wantName := range []string{"A", "B", "C", "D"} {
		if got[i].Name != wantName {
			t.Errorf("episode %d: want name %q, got %q", i, wantName, got[i].Name)
		}
	}
	if want := []int{0, 1, 2}; len(*seen) != 3 || (*seen)[0] != want[0] || (*seen)[1] != want[1] || (*seen)[2] != want[2] {
		t.Errorf("expected page sequence %v, got %v", want, *seen)
	}
}

// T3 — page 1 fails with a 500. Fail-closed: no partial slice, ever.
func TestSeriesEpisodes_MidPageErrorReturnsNoPartialResult(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v4/login", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"status":"success","data":{"token":"tok"}}`)
	})
	mux.HandleFunc("GET /v4/series/{id}/episodes/{type}", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") == "0" {
			writeEpisodePage(t, w, []episodeItem{{ID: 1, SeriesID: 7, Name: "A", Number: 1, SeasonNumber: 1}})
			return
		}
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	got, err := newEpisodeClient(t, srv).SeriesEpisodes(context.Background(), 7, SeasonTypeOfficial)
	if err == nil {
		t.Fatalf("expected an error when page 1 fails, got nil (episodes %+v)", got)
	}
	if got != nil {
		t.Errorf("fail-closed violated: expected nil episodes on error, got %+v", got)
	}
}

// T4 — the server never returns an empty page, so pagination overruns.
// Overrun is an ERROR, not a truncated success.
func TestSeriesEpisodes_OverrunReturnsErrEpisodeListTruncated(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v4/login", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"status":"success","data":{"token":"tok"}}`)
	})
	var mu sync.Mutex
	requests := 0
	mux.HandleFunc("GET /v4/series/{id}/episodes/{type}", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests++
		n := requests
		mu.Unlock()
		writeEpisodePage(t, w, []episodeItem{{ID: n, SeriesID: 7, Name: "endless", Number: 1, SeasonNumber: 1}})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	got, err := newEpisodeClient(t, srv).SeriesEpisodes(context.Background(), 7, SeasonTypeOfficial)
	if !errors.Is(err, ErrEpisodeListTruncated) {
		t.Fatalf("expected ErrEpisodeListTruncated, got %v", err)
	}
	if got != nil {
		t.Errorf("expected nil episodes on overrun, got %d items", len(got))
	}
	mu.Lock()
	total := requests
	mu.Unlock()
	if total != maxEpisodePages {
		t.Errorf("expected exactly %d page requests, got %d", maxEpisodePages, total)
	}
}

// T5 — structurally unusable slots are dropped, not errored, mirroring
// doSearch's existing "silently drop malformed items" convention.
func TestSeriesEpisodes_DropsUnaddressableSlots(t *testing.T) {
	srv, _ := newEpisodeServer(t, episodePages{{
		{ID: 1, SeriesID: 7, Name: "no number", Number: 0, SeasonNumber: 1},
		{ID: 2, SeriesID: 7, Name: "negative season", Number: 4, SeasonNumber: -1},
		{ID: 3, SeriesID: 7, Name: "negative number", Number: -2, SeasonNumber: 1},
		{ID: 4, SeriesID: 7, Name: "Valid", Number: 5, SeasonNumber: 2},
	}})
	defer srv.Close()

	got, err := newEpisodeClient(t, srv).SeriesEpisodes(context.Background(), 7, SeasonTypeOfficial)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Name != "Valid" {
		t.Fatalf("expected only the addressable slot, got %+v", got)
	}
}

// T6 — an untitled Season-0 (Specials) slot is KEPT. Dropping it here would
// change the catalog's shape for no gain; the caller's placeholder-name
// filter is what excludes it, and season 0 is deliberately in scope.
func TestSeriesEpisodes_KeepsUntitledAndSeasonZero(t *testing.T) {
	srv, _ := newEpisodeServer(t, episodePages{{
		{ID: 1, SeriesID: 7, Name: "", Number: 2, SeasonNumber: 0, Aired: "1930-01-01"},
		{ID: 2, SeriesID: 7, Name: "Regular", Number: 1, SeasonNumber: 1},
	}})
	defer srv.Close()

	got, err := newEpisodeClient(t, srv).SeriesEpisodes(context.Background(), 7, SeasonTypeOfficial)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 episodes (untitled Specials slot kept), got %+v", got)
	}
	if got[0].Name != "" || got[0].SeasonNumber != 0 || got[0].Number != 2 {
		t.Errorf("expected the untitled S00E02 slot preserved verbatim, got %+v", got[0])
	}
}

// T7 — a failing login surfaces as an error with no episodes.
func TestSeriesEpisodes_LoginFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	defer srv.Close()

	got, err := newEpisodeClient(t, srv).SeriesEpisodes(context.Background(), 7, SeasonTypeOfficial)
	if err == nil {
		t.Fatalf("expected an error on login 401, got nil (episodes %+v)", got)
	}
	if got != nil {
		t.Errorf("expected nil episodes on login failure, got %+v", got)
	}
}

// T8 — an empty catalog: page 0 itself returns no episodes. That is a
// successful empty result, not an error; every caller treats it as no-match.
func TestSeriesEpisodes_EmptyCatalog(t *testing.T) {
	srv, seen := newEpisodeServer(t, episodePages{{}})
	defer srv.Close()

	got, err := newEpisodeClient(t, srv).SeriesEpisodes(context.Background(), 7, SeasonTypeOfficial)
	if err != nil {
		t.Fatalf("an empty catalog must not be an error, got %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected 0 episodes, got %+v", got)
	}
	if len(*seen) != 1 || (*seen)[0] != 0 {
		t.Errorf("expected exactly one request for page 0, got %v", *seen)
	}
}

// T9 — a malformed response: the /search-shaped array envelope
// ({status, data:[...]}) served from the episodes route. This is the exact
// asymmetry the package's VERIFICATION STATUS block warns about, so it must
// surface as a decode ERROR rather than silently yielding an empty catalog.
func TestSeriesEpisodes_MalformedEnvelopeIsAnError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v4/login", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"status":"success","data":{"token":"tok"}}`)
	})
	mux.HandleFunc("GET /v4/series/{id}/episodes/{type}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"status":"success","data":[{"id":1,"name":"A"}]}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	got, err := newEpisodeClient(t, srv).SeriesEpisodes(context.Background(), 7, SeasonTypeOfficial)
	if err == nil {
		t.Fatalf("expected a decode error for the /search-shaped array envelope, got nil (episodes %+v)", got)
	}
	if got != nil {
		t.Errorf("expected nil episodes on a malformed envelope, got %+v", got)
	}
}

// A non-positive series id is rejected before any request is issued.
func TestSeriesEpisodes_RejectsInvalidSeriesID(t *testing.T) {
	srv, seen := newEpisodeServer(t, episodePages{{{ID: 1, SeriesID: 7, Name: "A", Number: 1, SeasonNumber: 1}}})
	defer srv.Close()

	if _, err := newEpisodeClient(t, srv).SeriesEpisodes(context.Background(), 0, SeasonTypeOfficial); err == nil {
		t.Fatal("expected an error for series id 0, got nil")
	}
	if len(*seen) != 0 {
		t.Errorf("expected no HTTP requests for an invalid series id, got %v", *seen)
	}
}

// An empty seasonType defaults to SeasonTypeOfficial, and the path segment
// is what actually reaches the server.
func TestSeriesEpisodes_EmptySeasonTypeDefaultsToOfficial(t *testing.T) {
	var gotPath string
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v4/login", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"status":"success","data":{"token":"tok"}}`)
	})
	mux.HandleFunc("GET /v4/series/{id}/episodes/{type}", func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		writeEpisodePage(t, w, nil)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	if _, err := newEpisodeClient(t, srv).SeriesEpisodes(context.Background(), 73910, ""); err != nil {
		t.Fatal(err)
	}
	if want := "/v4/series/73910/episodes/" + SeasonTypeOfficial; gotPath != want {
		t.Errorf("path: want %q, got %q", want, gotPath)
	}
}

// The bearer token from POST /v4/login is presented on the episodes request.
func TestSeriesEpisodes_SendsBearerToken(t *testing.T) {
	var gotAuth string
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v4/login", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"status":"success","data":{"token":"tok-episodes"}}`)
	})
	mux.HandleFunc("GET /v4/series/{id}/episodes/{type}", func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		writeEpisodePage(t, w, nil)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	if _, err := newEpisodeClient(t, srv).SeriesEpisodes(context.Background(), 7, SeasonTypeOfficial); err != nil {
		t.Fatal(err)
	}
	if want := "Bearer tok-episodes"; gotAuth != want {
		t.Errorf("Authorization header: want %q, got %q", want, gotAuth)
	}
}
