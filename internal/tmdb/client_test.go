package tmdb

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
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

func TestUpcomingMovies(t *testing.T) {
	var gotPath, gotPage string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotPage = r.URL.Query().Get("page")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(movieFixture))
	})

	items, err := c.UpcomingMovies(context.Background(), 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotPath != "/movie/upcoming" {
		t.Errorf("unexpected path: %s", gotPath)
	}
	if gotPage != "" {
		t.Errorf("page 1 should omit the page param, got %q", gotPage)
	}
	if len(items) != 1 || items[0].MediaType != Movie {
		t.Errorf("unexpected items: %+v", items)
	}

	if _, err := c.UpcomingMovies(context.Background(), 2); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotPage != "2" {
		t.Errorf("expected page=2, got %q", gotPage)
	}
}

func TestUpcomingTV(t *testing.T) {
	var gotPath string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(tvFixture))
	})

	items, err := c.UpcomingTV(context.Background(), 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotPath != "/tv/on_the_air" {
		t.Errorf("unexpected path: %s", gotPath)
	}
	if len(items) != 1 || items[0].MediaType != TV {
		t.Errorf("unexpected items: %+v", items)
	}
}

func TestMovieGenres_ParsesList(t *testing.T) {
	var gotPath string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"genres": [{"id": 28, "name": "Action"}, {"id": 35, "name": "Comedy"}]}`))
	})

	genres, err := c.MovieGenres(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotPath != "/genre/movie/list" {
		t.Errorf("unexpected path: %s", gotPath)
	}
	if len(genres) != 2 || genres[0].ID != 28 || genres[0].Name != "Action" || genres[1].Name != "Comedy" {
		t.Errorf("unexpected genres: %+v", genres)
	}
}

// TestTVGenres_ParsesList is MovieGenres' direct sibling for /genre/tv/list.
func TestTVGenres_ParsesList(t *testing.T) {
	var gotPath string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"genres": [{"id": 10759, "name": "Action & Adventure"}]}`))
	})

	genres, err := c.TVGenres(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotPath != "/genre/tv/list" {
		t.Errorf("unexpected path: %s", gotPath)
	}
	if len(genres) != 1 || genres[0].ID != 10759 || genres[0].Name != "Action & Adventure" {
		t.Errorf("unexpected genres: %+v", genres)
	}
}

func TestDiscoverMoviesByGenre_SendsGenreAndPage(t *testing.T) {
	var gotPath, gotGenre, gotPage string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotGenre = r.URL.Query().Get("with_genres")
		gotPage = r.URL.Query().Get("page")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(movieFixture))
	})

	items, err := c.DiscoverMoviesByGenre(context.Background(), 28, 2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotPath != "/discover/movie" {
		t.Errorf("unexpected path: %s", gotPath)
	}
	if gotGenre != "28" {
		t.Errorf("expected with_genres=28, got %q", gotGenre)
	}
	if gotPage != "2" {
		t.Errorf("expected page=2, got %q", gotPage)
	}
	if len(items) != 1 || items[0].MediaType != Movie {
		t.Errorf("unexpected items: %+v", items)
	}
}

// TestDiscoverTVByGenre is DiscoverMoviesByGenre's direct sibling for the TV
// catalog.
func TestDiscoverTVByGenre_SendsGenreAndPage(t *testing.T) {
	var gotPath, gotGenre string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotGenre = r.URL.Query().Get("with_genres")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(tvFixture))
	})

	items, err := c.DiscoverTVByGenre(context.Background(), 10759, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotPath != "/discover/tv" {
		t.Errorf("unexpected path: %s", gotPath)
	}
	if gotGenre != "10759" {
		t.Errorf("expected with_genres=10759, got %q", gotGenre)
	}
	if len(items) != 1 || items[0].MediaType != TV {
		t.Errorf("unexpected items: %+v", items)
	}
}

func TestDiscoverMoviesByStudio_SendsCompanyID(t *testing.T) {
	var gotPath, gotCompany string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotCompany = r.URL.Query().Get("with_companies")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(movieFixture))
	})

	items, err := c.DiscoverMoviesByStudio(context.Background(), 420, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotPath != "/discover/movie" {
		t.Errorf("unexpected path: %s", gotPath)
	}
	if gotCompany != "420" {
		t.Errorf("expected with_companies=420, got %q", gotCompany)
	}
	if len(items) != 1 || items[0].MediaType != Movie {
		t.Errorf("unexpected items: %+v", items)
	}
}

func TestDiscoverTVByNetwork_SendsNetworkID(t *testing.T) {
	var gotPath, gotNetwork string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotNetwork = r.URL.Query().Get("with_networks")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(tvFixture))
	})

	items, err := c.DiscoverTVByNetwork(context.Background(), 213, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotPath != "/discover/tv" {
		t.Errorf("unexpected path: %s", gotPath)
	}
	if gotNetwork != "213" {
		t.Errorf("expected with_networks=213, got %q", gotNetwork)
	}
	if len(items) != 1 || items[0].MediaType != TV {
		t.Errorf("unexpected items: %+v", items)
	}
}

func TestSearchKeywords_ParsesResults(t *testing.T) {
	var gotPath, gotQuery string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.Query().Get("query")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"results": [{"id": 818, "name": "based on novel"}]}`))
	})

	keywords, err := c.SearchKeywords(context.Background(), "based on novel")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotPath != "/search/keyword" {
		t.Errorf("unexpected path: %s", gotPath)
	}
	if gotQuery != "based on novel" {
		t.Errorf("expected query param %q, got %q", "based on novel", gotQuery)
	}
	if len(keywords) != 1 || keywords[0].ID != 818 || keywords[0].Name != "based on novel" {
		t.Errorf("unexpected keywords: %+v", keywords)
	}
}

func TestDiscoverMoviesByKeyword_SendsKeywordID(t *testing.T) {
	var gotPath, gotKeyword string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotKeyword = r.URL.Query().Get("with_keywords")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(movieFixture))
	})

	items, err := c.DiscoverMoviesByKeyword(context.Background(), 818, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotPath != "/discover/movie" {
		t.Errorf("unexpected path: %s", gotPath)
	}
	if gotKeyword != "818" {
		t.Errorf("expected with_keywords=818, got %q", gotKeyword)
	}
	if len(items) != 1 || items[0].MediaType != Movie {
		t.Errorf("unexpected items: %+v", items)
	}
}

// TestDiscoverTVByKeyword is DiscoverMoviesByKeyword's direct sibling for
// the TV catalog.
func TestDiscoverTVByKeyword_SendsKeywordID(t *testing.T) {
	var gotPath, gotKeyword string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotKeyword = r.URL.Query().Get("with_keywords")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(tvFixture))
	})

	items, err := c.DiscoverTVByKeyword(context.Background(), 818, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotPath != "/discover/tv" {
		t.Errorf("unexpected path: %s", gotPath)
	}
	if gotKeyword != "818" {
		t.Errorf("expected with_keywords=818, got %q", gotKeyword)
	}
	if len(items) != 1 || items[0].MediaType != TV {
		t.Errorf("unexpected items: %+v", items)
	}
}

// TestDiscoverFilterQuery_GenreORJoin proves multiple genre ids are pipe-
// joined (OR), not comma-joined (AND) — the deliberate choice so a
// multi-genre filter returns "any of these," not "all of these."
func TestDiscoverFilterQuery_GenreORJoin(t *testing.T) {
	q := discoverFilterQuery(1, "primary_release_year", "primary_release_date", FilterOptions{GenreIDs: []int{28, 12, 16}})
	if got := q.Get("with_genres"); got != "28|12|16" {
		t.Errorf("expected pipe-joined with_genres=28|12|16, got %q", got)
	}
}

// TestDiscoverFilterQuery_MovieVsTVFieldSplit proves the caller-supplied
// yearField/dateField names are what actually reach the query — movies use
// primary_release_*, tv uses first_air_date*.
func TestDiscoverFilterQuery_MovieVsTVFieldSplit(t *testing.T) {
	movie := discoverFilterQuery(1, "primary_release_year", "primary_release_date", FilterOptions{Year: 2023})
	if movie.Get("primary_release_year") != "2023" || movie.Has("first_air_date_year") {
		t.Errorf("movie query should set primary_release_year only, got %v", movie)
	}
	tv := discoverFilterQuery(1, "first_air_date_year", "first_air_date", FilterOptions{Year: 2023})
	if tv.Get("first_air_date_year") != "2023" || tv.Has("primary_release_year") {
		t.Errorf("tv query should set first_air_date_year only, got %v", tv)
	}
}

// TestDiscoverFilterQuery_VoteCountFloorOnRatingSort proves the vote_count.gte
// floor is applied for a "Highest Rated" sort even when no explicit MinRating
// is set — a lone low-vote title must not dominate the top of the grid.
func TestDiscoverFilterQuery_VoteCountFloorOnRatingSort(t *testing.T) {
	q := discoverFilterQuery(1, "primary_release_year", "primary_release_date", FilterOptions{SortBy: "vote_average.desc"})
	if got := q.Get("vote_count.gte"); got != strconv.Itoa(filterVoteCountFloor) {
		t.Errorf("expected vote_count.gte=%d for a rating sort with no min-rating, got %q", filterVoteCountFloor, got)
	}
}

// TestDiscoverFilterQuery_VoteCountFloorOnMinRating proves the floor is also
// applied whenever an explicit MinRating is set, and that MinRating is
// formatted to one decimal place.
func TestDiscoverFilterQuery_VoteCountFloorOnMinRating(t *testing.T) {
	q := discoverFilterQuery(1, "primary_release_year", "primary_release_date", FilterOptions{MinRating: 7})
	if got := q.Get("vote_average.gte"); got != "7.0" {
		t.Errorf("expected vote_average.gte=7.0 (one decimal), got %q", got)
	}
	if got := q.Get("vote_count.gte"); got != strconv.Itoa(filterVoteCountFloor) {
		t.Errorf("expected vote_count.gte floor when a min-rating is set, got %q", got)
	}
}

// TestDiscoverFilterQuery_NoVoteCountFloorWhenNeitherSet proves the floor is
// absent when there's neither a min-rating nor a rating sort — a plain
// popularity or newest browse shouldn't exclude low-vote titles.
func TestDiscoverFilterQuery_NoVoteCountFloorWhenNeitherSet(t *testing.T) {
	q := discoverFilterQuery(1, "primary_release_year", "primary_release_date", FilterOptions{SortBy: "popularity.desc"})
	if q.Has("vote_count.gte") {
		t.Errorf("expected no vote_count.gte for a non-rating browse, got %q", q.Get("vote_count.gte"))
	}
}

// TestDiscoverFilterQuery_LteBoundOnlyOnNewestSort proves the {dateField}.lte
// cap is applied only on the newest (date-desc) sort — with nowFn overridden
// for a deterministic date assertion — and never on other sorts.
func TestDiscoverFilterQuery_LteBoundOnlyOnNewestSort(t *testing.T) {
	orig := nowFn
	nowFn = func() time.Time { return time.Date(2023, 6, 15, 0, 0, 0, 0, time.UTC) }
	defer func() { nowFn = orig }()

	newest := discoverFilterQuery(1, "primary_release_year", "primary_release_date", FilterOptions{SortBy: "primary_release_date.desc"})
	if got := newest.Get("primary_release_date.lte"); got != "2023-06-15" {
		t.Errorf("expected primary_release_date.lte=2023-06-15 on the newest sort, got %q", got)
	}

	// A rating sort must not carry any .lte date bound.
	rating := discoverFilterQuery(1, "primary_release_year", "primary_release_date", FilterOptions{SortBy: "vote_average.desc"})
	if rating.Has("primary_release_date.lte") {
		t.Errorf("expected no .lte bound on a non-newest sort, got %q", rating.Get("primary_release_date.lte"))
	}

	// The tv newest sort uses first_air_date, not primary_release_date.
	tvNewest := discoverFilterQuery(1, "first_air_date_year", "first_air_date", FilterOptions{SortBy: "first_air_date.desc"})
	if got := tvNewest.Get("first_air_date.lte"); got != "2023-06-15" {
		t.Errorf("expected first_air_date.lte=2023-06-15 on the tv newest sort, got %q", got)
	}
}

// TestDiscoverMoviesFiltered_HitsDiscoverMovie proves the movie filter path
// hits /discover/movie and forwards the built query (pipe-joined genres, sort).
func TestDiscoverMoviesFiltered_HitsDiscoverMovie(t *testing.T) {
	var gotPath, gotGenres, gotSort string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotGenres = r.URL.Query().Get("with_genres")
		gotSort = r.URL.Query().Get("sort_by")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(movieFixture))
	})

	items, err := c.DiscoverMoviesFiltered(context.Background(), FilterOptions{GenreIDs: []int{28, 12}, SortBy: "popularity.desc"}, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotPath != "/discover/movie" {
		t.Errorf("unexpected path: %s", gotPath)
	}
	if gotGenres != "28|12" || gotSort != "popularity.desc" {
		t.Errorf("expected with_genres=28|12 sort_by=popularity.desc, got with_genres=%q sort_by=%q", gotGenres, gotSort)
	}
	if len(items) != 1 || items[0].MediaType != Movie {
		t.Errorf("unexpected items: %+v", items)
	}
}

// TestDiscoverTVFiltered_HitsDiscoverTV is DiscoverMoviesFiltered's sibling for
// the TV catalog.
func TestDiscoverTVFiltered_HitsDiscoverTV(t *testing.T) {
	var gotPath, gotNetwork string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotNetwork = r.URL.Query().Get("with_networks")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(tvFixture))
	})

	items, err := c.DiscoverTVFiltered(context.Background(), FilterOptions{NetworkID: 213}, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotPath != "/discover/tv" {
		t.Errorf("unexpected path: %s", gotPath)
	}
	if gotNetwork != "213" {
		t.Errorf("expected with_networks=213, got %q", gotNetwork)
	}
	if len(items) != 1 || items[0].MediaType != TV {
		t.Errorf("unexpected items: %+v", items)
	}
}

func TestKnownStudiosAndNetworks_NotEmpty(t *testing.T) {
	if len(KnownStudios) == 0 {
		t.Error("expected a non-empty KnownStudios seed list")
	}
	if len(KnownNetworks) == 0 {
		t.Error("expected a non-empty KnownNetworks seed list")
	}
}

func TestTrailerURL_PrefersOfficialYouTubeTrailer(t *testing.T) {
	var gotPath string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"results": [
			{"key": "teaser1", "site": "YouTube", "type": "Teaser", "official": true},
			{"key": "unofficial1", "site": "YouTube", "type": "Trailer", "official": false},
			{"key": "official1", "site": "YouTube", "type": "Trailer", "official": true},
			{"key": "vimeo1", "site": "Vimeo", "type": "Trailer", "official": true}
		]}`))
	})

	url, err := c.TrailerURL(context.Background(), Movie, 42)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotPath != "/movie/42/videos" {
		t.Errorf("unexpected path: %s", gotPath)
	}
	if url != "https://www.youtube.com/watch?v=official1" {
		t.Errorf("expected the official YouTube trailer, got %q", url)
	}
}

func TestTrailerURL_TVUsesTVPath(t *testing.T) {
	var gotPath string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"results": [{"key": "abc", "site": "YouTube", "type": "Trailer", "official": true}]}`))
	})

	url, err := c.TrailerURL(context.Background(), TV, 7)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotPath != "/tv/7/videos" {
		t.Errorf("unexpected path: %s", gotPath)
	}
	if url != "https://www.youtube.com/watch?v=abc" {
		t.Errorf("unexpected url: %q", url)
	}
}

func TestTrailerURL_FallsBackToUnofficialThenAnyYouTubeVideo(t *testing.T) {
	// No official Trailer exists — falls back to the unofficial Trailer over
	// the Teaser, since Trailer beats Teaser regardless of official status.
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"results": [
			{"key": "teaser1", "site": "YouTube", "type": "Teaser", "official": true},
			{"key": "unofficial1", "site": "YouTube", "type": "Trailer", "official": false}
		]}`))
	})
	url, err := c.TrailerURL(context.Background(), Movie, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if url != "https://www.youtube.com/watch?v=unofficial1" {
		t.Errorf("expected the unofficial trailer as fallback, got %q", url)
	}
}

func TestTrailerURL_NoYouTubeVideoReturnsEmptyNotError(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"results": [{"key": "x", "site": "Vimeo", "type": "Trailer", "official": true}]}`))
	})
	url, err := c.TrailerURL(context.Background(), Movie, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if url != "" {
		t.Errorf("expected empty url when TMDB has no YouTube video on file, got %q", url)
	}
}

func TestHasUSRelease_TrueForPastUSDigitalRelease(t *testing.T) {
	var gotPath string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"results": [
			{"iso_3166_1": "GB", "release_dates": [{"type": 3, "release_date": "2020-01-01T00:00:00.000Z"}]},
			{"iso_3166_1": "US", "release_dates": [
				{"type": 3, "release_date": "2020-01-01T00:00:00.000Z"},
				{"type": 4, "release_date": "2020-02-01T00:00:00.000Z"}
			]}
		]}`))
	})

	has, err := c.HasUSRelease(context.Background(), 99)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotPath != "/movie/99/release_dates" {
		t.Errorf("unexpected path: %s", gotPath)
	}
	if !has {
		t.Error("expected true for a past US digital release")
	}
}

func TestHasUSRelease_FalseForTheatricalOnly(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"results": [
			{"iso_3166_1": "US", "release_dates": [{"type": 3, "release_date": "2020-01-01T00:00:00.000Z"}]}
		]}`))
	})

	has, err := c.HasUSRelease(context.Background(), 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if has {
		t.Error("expected false — theatrical-only is not yet acquirable")
	}
}

func TestHasUSRelease_FalseForFutureDigitalRelease(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"results": [
			{"iso_3166_1": "US", "release_dates": [{"type": 4, "release_date": "2099-01-01T00:00:00.000Z"}]}
		]}`))
	})

	has, err := c.HasUSRelease(context.Background(), 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if has {
		t.Error("expected false — the digital release date is in the future")
	}
}

func TestHasUSRelease_FalseWithNoUSEntry(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"results": [
			{"iso_3166_1": "GB", "release_dates": [{"type": 4, "release_date": "2020-01-01T00:00:00.000Z"}]}
		]}`))
	})

	has, err := c.HasUSRelease(context.Background(), 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if has {
		t.Error("expected false — no US entry at all")
	}
}
