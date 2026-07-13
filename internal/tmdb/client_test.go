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

	items, err := c.Trending(context.Background(), Movie, "week", 1)
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

	items, err := c.Trending(context.Background(), TV, "day", 1)
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

	items, err := c.Popular(context.Background(), Movie, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(items) != 1 || items[0].MediaType != Movie {
		t.Errorf("unexpected items: %+v", items)
	}
}

// TestTrending_PageParam proves the page cursor rides through to TMDB: page 1
// (and page <= 1) sends no `page` param (identical URL to the pre-pagination
// call), while page 2 sends page=2 — the two requests are distinguishable,
// which is exactly what a "Show more" append relies on.
func TestTrending_PageParam(t *testing.T) {
	var gotPage string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPage = r.URL.Query().Get("page")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(movieFixture))
	})

	if _, err := c.Trending(context.Background(), Movie, "week", 1); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotPage != "" {
		t.Errorf("page 1 should omit the page param, got %q", gotPage)
	}

	if _, err := c.Trending(context.Background(), Movie, "week", 2); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotPage != "2" {
		t.Errorf("expected page=2, got %q", gotPage)
	}
}

// TestPopular_PageParam is Trending's direct sibling for the /popular endpoint.
func TestPopular_PageParam(t *testing.T) {
	var gotPage string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPage = r.URL.Query().Get("page")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(movieFixture))
	})

	if _, err := c.Popular(context.Background(), Movie, 1); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotPage != "" {
		t.Errorf("page 1 should omit the page param, got %q", gotPage)
	}

	if _, err := c.Popular(context.Background(), Movie, 3); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotPage != "3" {
		t.Errorf("expected page=3, got %q", gotPage)
	}
}

// TestMovieDetails_ParsesPosterPath proves /movie/{id}'s poster_path decodes
// into MovieDetails.PosterPath — the field posterHandler serves to Discover's
// lazy per-card poster fetch.
func TestMovieDetails_ParsesPosterPath(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/movie/77" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id": 77, "title": "A Movie", "poster_path": "/poster77.jpg"}`))
	})

	d, err := c.MovieDetails(context.Background(), 77)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if d.PosterPath != "/poster77.jpg" {
		t.Errorf("expected poster path, got %q", d.PosterPath)
	}
}

// TestTVDetails_ParsesPosterPath is MovieDetails' sibling for /tv/{id}.
func TestTVDetails_ParsesPosterPath(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/tv/88" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id": 88, "name": "A Show", "poster_path": "/poster88.jpg"}`))
	})

	d, err := c.TVDetails(context.Background(), 88)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if d.PosterPath != "/poster88.jpg" {
		t.Errorf("expected poster path, got %q", d.PosterPath)
	}
}

func TestSearchMovies_NormalizesAndSendsQuery(t *testing.T) {
	var gotPath, gotQuery string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.Query().Get("query")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(movieFixture))
	})

	items, err := c.SearchMovies(context.Background(), "Some Movie")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotPath != "/search/movie" {
		t.Errorf("unexpected path: %s", gotPath)
	}
	if gotQuery != "Some Movie" {
		t.Errorf("expected query param %q, got %q", "Some Movie", gotQuery)
	}
	if len(items) != 1 || items[0].Title != "Some Movie" || items[0].MediaType != Movie {
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

func TestSearchTV_NormalizesAndSendsQuery(t *testing.T) {
	var gotPath, gotQuery string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.Query().Get("query")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(tvFixture))
	})

	items, err := c.SearchTV(context.Background(), "Some Show")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotPath != "/search/tv" {
		t.Errorf("unexpected path: %s", gotPath)
	}
	if gotQuery != "Some Show" {
		t.Errorf("expected query param %q, got %q", "Some Show", gotQuery)
	}
	if len(items) != 1 || items[0].Title != "Some Show" || items[0].MediaType != TV {
		t.Errorf("unexpected items: %+v", items)
	}
}

func TestMovieDetails_ParsesIMDBRuntimeGenres(t *testing.T) {
	var gotPath string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id": 550, "title": "Fight Club", "imdb_id": "tt0137523", "runtime": 139,
		  "genres": [{"id": 18, "name": "Drama"}, {"id": 53, "name": "Thriller"}]}`))
	})

	details, err := c.MovieDetails(context.Background(), 550)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotPath != "/movie/550" {
		t.Errorf("unexpected path: %s", gotPath)
	}
	if details.ID != 550 || details.Title != "Fight Club" || details.IMDBID != "tt0137523" || details.Runtime != 139 {
		t.Errorf("unexpected details: %+v", details)
	}
	if len(details.Genres) != 2 || details.Genres[0] != "Drama" || details.Genres[1] != "Thriller" {
		t.Errorf("unexpected genres: %+v", details.Genres)
	}
}

func TestMovieDetails_HandlesNullOptionalFields(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// TMDB returns null for a not-yet-known runtime and an absent imdb_id.
		w.Write([]byte(`{"id": 999, "title": "Unreleased", "imdb_id": null, "runtime": null, "genres": null}`))
	})

	details, err := c.MovieDetails(context.Background(), 999)
	if err != nil {
		t.Fatalf("unexpected error decoding null fields: %v", err)
	}
	if details.ID != 999 || details.Title != "Unreleased" {
		t.Errorf("unexpected details: %+v", details)
	}
	if details.IMDBID != "" || details.Runtime != 0 {
		t.Errorf("expected zero-valued optional fields, got %+v", details)
	}
	if len(details.Genres) != 0 {
		t.Errorf("expected no genres for null, got %+v", details.Genres)
	}
}

func TestTVDetails_NormalizesNameToTitle(t *testing.T) {
	var gotPath string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id": 1396, "name": "Breaking Bad",
		  "genres": [{"id": 18, "name": "Drama"}]}`))
	})

	details, err := c.TVDetails(context.Background(), 1396)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotPath != "/tv/1396" {
		t.Errorf("unexpected path: %s", gotPath)
	}
	if details.ID != 1396 || details.Title != "Breaking Bad" {
		t.Errorf("unexpected details: %+v", details)
	}
	if len(details.Genres) != 1 || details.Genres[0] != "Drama" {
		t.Errorf("unexpected genres: %+v", details.Genres)
	}
}

func TestTVDetails_HandlesNullGenres(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id": 1, "name": "Sparse Show", "genres": null}`))
	})

	details, err := c.TVDetails(context.Background(), 1)
	if err != nil {
		t.Fatalf("unexpected error decoding null genres: %v", err)
	}
	if details.Title != "Sparse Show" || len(details.Genres) != 0 {
		t.Errorf("unexpected details: %+v", details)
	}
}

func TestSeasonDetails_NormalizesEpisodes(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/tv/2/season/1" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"episodes": [
		  {"episode_number": 1, "name": "Pilot", "air_date": "2022-01-01", "runtime": 58},
		  {"episode_number": 2, "name": "Second", "air_date": "2022-01-08", "runtime": 47}
		]}`))
	})

	episodes, err := c.SeasonDetails(context.Background(), 2, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(episodes) != 2 || episodes[0].EpisodeNumber != 1 || episodes[0].Name != "Pilot" || episodes[0].AirDate != "2022-01-01" {
		t.Errorf("unexpected episodes: %+v", episodes)
	}
	if episodes[0].Runtime != 58 || episodes[1].Runtime != 47 {
		t.Errorf("unexpected per-episode runtimes: %+v", episodes)
	}
}

// TestSeasonDetails_HandlesNullRuntime confirms a null per-episode runtime
// (TMDB reports it for not-yet-aired or sparse episodes) decodes to 0 without
// erroring — the auto-grab scorer treats 0 as unknown/neutral.
func TestSeasonDetails_HandlesNullRuntime(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"episodes": [
		  {"episode_number": 1, "name": "Pilot", "air_date": "2022-01-01", "runtime": null}
		]}`))
	})

	episodes, err := c.SeasonDetails(context.Background(), 2, 1)
	if err != nil {
		t.Fatalf("unexpected error decoding null runtime: %v", err)
	}
	if len(episodes) != 1 || episodes[0].Runtime != 0 {
		t.Errorf("expected zero runtime for null, got %+v", episodes)
	}
}
