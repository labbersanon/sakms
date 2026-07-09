package tmdb

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func newTestClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return New(Config{BaseURL: srv.URL, APIKey: "test-key"}, srv.Client())
}

// movieFixture is a plausible /trending/movie/week or /movie/popular
// response — no media_type field, since these endpoints are already
// scoped to one media type.
const movieFixture = `{"results": [
  {"id": 1, "title": "Some Movie", "poster_path": "/abc.jpg", "overview": "A movie.", "release_date": "2023-05-01", "vote_average": 7.5}
]}`

// tvFixture likewise for a /trending/tv/week or /tv/popular response.
const tvFixture = `{"results": [
  {"id": 2, "name": "Some Show", "poster_path": "/def.jpg", "overview": "A show.", "first_air_date": "2022-01-01", "vote_average": 8.1}
]}`

func TestTrending_Movie_NormalizesTitleAndReleaseDate(t *testing.T) {
	var gotPath, gotKey string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotKey = r.URL.Query().Get("api_key")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(movieFixture))
	})

	items, err := c.Trending(context.Background(), Movie, "week")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotPath != "/trending/movie/week" {
		t.Errorf("unexpected path: %s", gotPath)
	}
	if gotKey != "test-key" {
		t.Errorf("expected api_key query param, got %q", gotKey)
	}
	if len(items) != 1 || items[0].Title != "Some Movie" || items[0].ReleaseDate != "2023-05-01" || items[0].MediaType != Movie {
		t.Errorf("unexpected items: %+v", items)
	}
}

func TestTrending_TV_NormalizesNameAndFirstAirDate(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/trending/tv/day" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(tvFixture))
	})

	items, err := c.Trending(context.Background(), TV, "day")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(items) != 1 || items[0].Title != "Some Show" || items[0].ReleaseDate != "2022-01-01" || items[0].MediaType != TV {
		t.Errorf("unexpected items: %+v", items)
	}
}

func TestPopular_Movie(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/movie/popular" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(movieFixture))
	})

	items, err := c.Popular(context.Background(), Movie)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(items) != 1 || items[0].MediaType != Movie {
		t.Errorf("unexpected items: %+v", items)
	}
}

func TestExternalIDs_ResolvesTVDBID(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/tv/2/external_ids" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id": 2, "tvdb_id": 12345, "imdb_id": "tt1234567"}`))
	})

	tvdbID, err := c.ExternalIDs(context.Background(), 2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tvdbID != 12345 {
		t.Errorf("expected tvdb id 12345, got %d", tvdbID)
	}
}

func TestExternalIDs_MissingTVDBIDReturnsZero(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id": 2, "tvdb_id": null}`))
	})

	tvdbID, err := c.ExternalIDs(context.Background(), 2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tvdbID != 0 {
		t.Errorf("expected 0 when tvdb_id is null, got %d", tvdbID)
	}
}
