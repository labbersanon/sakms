package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labbersanon/sakms/internal/tmdb"
)

func fakeTMDB(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv
}

// fakeTMDBServer serves the two TMDB endpoints multiple handlers' tests need
// together: /movie/{id} (with a top-level imdb_id) and /tv/{id}/external_ids
// (tvdb_id). Originally defined alongside the now-removed availabilityHandler
// tests; kept here since autograb_handler_test.go's Series picker-gated
// fallback test also relies on it.
func fakeTMDBServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/movie/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":42,"title":"Some Movie","imdb_id":"tt1234567"}`))
	})
	mux.HandleFunc("/tv/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"tvdb_id":789}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestDiscoverHandler_MoviesUsesMovieMediaType(t *testing.T) {
	var gotPath string
	fake := fakeTMDB(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Trending/Popular Movies now also filters through HasUSRelease (see
		// filterReleasedMovies) — this handler must serve a qualifying US
		// release for /movie/1/release_dates or the fixture's one item would
		// filter out, breaking this test's unrelated media-type assertion.
		// gotPath is only recorded for the trending call itself, matching the
		// established sequential happens-before pattern this test already
		// relies on (see TestDiscoverHandler_PageParamForwarded's identical
		// convention).
		if strings.Contains(r.URL.Path, "/release_dates") {
			w.Write([]byte(`{"results":[{"iso_3166_1":"US","release_dates":[{"type":4,"release_date":"2020-01-01T00:00:00.000Z"}]}]}`))
			return
		}
		gotPath = r.URL.Path
		w.Write([]byte(`{"results":[{"id":1,"title":"Some Movie","poster_path":"/x.jpg","vote_average":7.5}]}`))
	})

	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	ctx := context.Background()
	if err := connStore.Upsert(ctx, "radarr", "http://radarr.local", "key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	overrideFixedURL(t, "tmdb", fake.URL)
	if err := connStore.Upsert(ctx, "tmdb", fake.URL, "key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore, nil, nil, nil, nil, nil))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/modes/movies/discover")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if gotPath != "/trending/movie/week" {
		t.Errorf("expected the movie trending path, got %s", gotPath)
	}
	var items []tmdb.Item
	if err := json.NewDecoder(resp.Body).Decode(&items); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if len(items) != 1 || items[0].Title != "Some Movie" {
		t.Errorf("unexpected items: %+v", items)
	}
}

func TestDiscoverHandler_SeriesUsesTVMediaType(t *testing.T) {
	var gotPath string
	fake := fakeTMDB(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"results":[{"id":2,"name":"Some Show","poster_path":"/y.jpg","vote_average":8.0}]}`))
	})

	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	ctx := context.Background()
	if err := connStore.Upsert(ctx, "sonarr", "http://sonarr.local", "key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	overrideFixedURL(t, "tmdb", fake.URL)
	if err := connStore.Upsert(ctx, "tmdb", fake.URL, "key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore, nil, nil, nil, nil, nil))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/modes/series/discover?category=popular")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	if gotPath != "/tv/popular" {
		t.Errorf("expected the tv popular path, got %s", gotPath)
	}
	var items []tmdb.Item
	if err := json.NewDecoder(resp.Body).Decode(&items); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if len(items) != 1 || items[0].Title != "Some Show" || items[0].MediaType != tmdb.TV {
		t.Errorf("unexpected items: %+v", items)
	}
}

func TestDiscoverHandler_TMDBNotConfigured(t *testing.T) {
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	if err := connStore.Upsert(context.Background(), "radarr", "http://radarr.local", "key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore, nil, nil, nil, nil, nil))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/modes/movies/discover")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 when tmdb isn't configured, got %d", resp.StatusCode)
	}
}

// TestDiscoverHandler_PageParamForwarded proves the ?page cursor threads
// through to TMDB: a default request omits the page param (first page), and
// ?page=2 forwards page=2 — the two are distinguishable upstream, which is
// what Discover's "Show more" append depends on.
func TestDiscoverHandler_PageParamForwarded(t *testing.T) {
	var gotPage string
	fake := fakeTMDB(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// See TestDiscoverHandler_MoviesUsesMovieMediaType's comment: Movies
		// trending/popular now also calls /movie/{id}/release_dates per item,
		// which carries no ?page param at all — that must not be allowed to
		// overwrite gotPage after the real trending/popular call already set
		// it.
		if strings.Contains(r.URL.Path, "/release_dates") {
			w.Write([]byte(`{"results":[{"iso_3166_1":"US","release_dates":[{"type":4,"release_date":"2020-01-01T00:00:00.000Z"}]}]}`))
			return
		}
		gotPage = r.URL.Query().Get("page")
		w.Write([]byte(`{"results":[{"id":1,"title":"Some Movie"}]}`))
	})

	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	overrideFixedURL(t, "tmdb", fake.URL)
	if err := connStore.Upsert(context.Background(), "tmdb", fake.URL, "key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore, nil, nil, nil, nil, nil))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/modes/movies/discover")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	resp.Body.Close()
	if gotPage != "" {
		t.Errorf("default request should omit page, got %q", gotPage)
	}

	resp, err = http.Get(srv.URL + "/api/modes/movies/discover?category=popular&page=2")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	resp.Body.Close()
	if gotPage != "2" {
		t.Errorf("expected page=2 forwarded to TMDB, got %q", gotPage)
	}
}

// TestPosterHandler_ReturnsPosterPath proves the lazy per-card poster lookup
// resolves a Movies tmdbId to its TMDB poster_path via /movie/{id}.
func TestPosterHandler_ReturnsPosterPath(t *testing.T) {
	fake := fakeTMDB(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/movie/99" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":99,"title":"A Movie","poster_path":"/p99.jpg"}`))
	})

	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	overrideFixedURL(t, "tmdb", fake.URL)
	if err := connStore.Upsert(context.Background(), "tmdb", fake.URL, "key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore, nil, nil, nil, nil, nil))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/modes/movies/poster?tmdbId=99")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var got map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if got["posterPath"] != "/p99.jpg" {
		t.Errorf("expected poster path, got %+v", got)
	}
}

// TestPosterHandler_RequiresTmdbID proves a missing/invalid tmdbId is a 400,
// not a silent zero-id upstream lookup.
func TestPosterHandler_RequiresTmdbID(t *testing.T) {
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	if err := connStore.Upsert(context.Background(), "tmdb", "http://tmdb.local", "key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore, nil, nil, nil, nil, nil))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/modes/movies/poster")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 without a tmdbId param, got %d", resp.StatusCode)
	}
}

// TestPosterHandler_RejectsAdultMode proves poster lookup is a 400 for Adult
// — Adult scenes carry their own image inline from TPDB, they have no tmdbId
// to look up a poster by (see posterHandler's doc comment).
func TestPosterHandler_RejectsAdultMode(t *testing.T) {
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore, nil, nil, nil, nil, nil))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/modes/adult/poster?tmdbId=99")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for adult mode, got %d", resp.StatusCode)
	}
}

func TestResolveTVDBIDHandler_ResolvesID(t *testing.T) {
	fake := fakeTMDB(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/tv/2/external_ids" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"tvdb_id":12345}`))
	})

	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	ctx := context.Background()
	if err := connStore.Upsert(ctx, "sonarr", "http://sonarr.local", "key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	overrideFixedURL(t, "tmdb", fake.URL)
	if err := connStore.Upsert(ctx, "tmdb", fake.URL, "key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore, nil, nil, nil, nil, nil))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/modes/series/discover/tvdb-id?tmdbId=2")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var result map[string]int
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if result["tvdbId"] != 12345 {
		t.Errorf("expected tvdbId 12345, got %+v", result)
	}
}

func TestResolveTVDBIDHandler_RequiresTmdbIDParam(t *testing.T) {
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	if err := connStore.Upsert(context.Background(), "sonarr", "http://sonarr.local", "key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore, nil, nil, nil, nil, nil))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/modes/series/discover/tvdb-id")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 without a tmdbId param, got %d", resp.StatusCode)
	}
}

// TestDiscoverHandler_UpcomingUsesOnTheAirForSeries proves category=upcoming
// dispatches to /movie/upcoming for Movies and /tv/on_the_air for Series
// (tmdb.UpcomingTV's doc comment explains why on_the_air, not
// airing_today/upcoming, is the TV analog).
func TestDiscoverHandler_UpcomingUsesOnTheAirForSeries(t *testing.T) {
	var gotPath string
	fake := fakeTMDB(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"results":[{"id":1,"name":"Some Show"}]}`))
	})

	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	overrideFixedURL(t, "tmdb", fake.URL)
	if err := connStore.Upsert(context.Background(), "tmdb", fake.URL, "key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore, nil, nil, nil, nil, nil))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/modes/series/discover?category=upcoming")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if gotPath != "/tv/on_the_air" {
		t.Errorf("expected /tv/on_the_air, got %s", gotPath)
	}
}

// TestDiscoverHandler_GenreRequiresGenreID proves category=genre without a
// genreId query param is a 400, not a zero-id upstream lookup.
func TestDiscoverHandler_GenreRequiresGenreID(t *testing.T) {
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	if err := connStore.Upsert(context.Background(), "tmdb", "http://tmdb.local", "key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore, nil, nil, nil, nil, nil))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/modes/movies/discover?category=genre")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 without genreId, got %d", resp.StatusCode)
	}
}

// TestDiscoverHandler_GenreSendsWithGenres proves category=genre&genreId=N
// forwards with_genres=N to /discover/{movie,tv} depending on {mode}.
func TestDiscoverHandler_GenreSendsWithGenres(t *testing.T) {
	var gotPath, gotGenre string
	fake := fakeTMDB(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotGenre = r.URL.Query().Get("with_genres")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"results":[{"id":1,"title":"Some Movie"}]}`))
	})

	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	overrideFixedURL(t, "tmdb", fake.URL)
	if err := connStore.Upsert(context.Background(), "tmdb", fake.URL, "key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore, nil, nil, nil, nil, nil))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/modes/movies/discover?category=genre&genreId=28")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if gotPath != "/discover/movie" || gotGenre != "28" {
		t.Errorf("expected /discover/movie?with_genres=28, got path=%s with_genres=%s", gotPath, gotGenre)
	}
}

// TestDiscoverHandler_StudioRejectsSeries proves category=studio is a 400
// for Series — TMDB companies are a movie-only concept (tmdb.Studio's doc
// comment).
func TestDiscoverHandler_StudioRejectsSeries(t *testing.T) {
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	if err := connStore.Upsert(context.Background(), "tmdb", "http://tmdb.local", "key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore, nil, nil, nil, nil, nil))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/modes/series/discover?category=studio&studioId=420")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for series studio browsing, got %d", resp.StatusCode)
	}
}

// TestDiscoverHandler_NetworkRequiresSeriesMode is studio's symmetric
// sibling: category=network is only valid for Series.
func TestDiscoverHandler_NetworkRequiresSeriesMode(t *testing.T) {
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	if err := connStore.Upsert(context.Background(), "tmdb", "http://tmdb.local", "key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore, nil, nil, nil, nil, nil))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/modes/movies/discover?category=network&networkId=213")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for movies network browsing, got %d", resp.StatusCode)
	}
}

// TestDiscoverGenresHandler_MoviesUsesMovieGenreList proves
// GET /api/modes/{mode}/discover/genres dispatches to /genre/movie/list for
// Movies and /genre/tv/list for Series.
func TestDiscoverGenresHandler_MoviesUsesMovieGenreList(t *testing.T) {
	var gotPath string
	fake := fakeTMDB(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"genres":[{"id":28,"name":"Action"}]}`))
	})

	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	overrideFixedURL(t, "tmdb", fake.URL)
	if err := connStore.Upsert(context.Background(), "tmdb", fake.URL, "key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore, nil, nil, nil, nil, nil))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/modes/movies/discover/genres")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if gotPath != "/genre/movie/list" {
		t.Errorf("expected /genre/movie/list, got %s", gotPath)
	}
	var genres []tmdb.Genre
	if err := json.NewDecoder(resp.Body).Decode(&genres); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if len(genres) != 1 || genres[0].Name != "Action" {
		t.Errorf("unexpected genres: %+v", genres)
	}
}

// TestDiscoverStudiosHandler_ReturnsKnownStudios proves the static seed list
// serves directly with no TMDB connection required.
func TestDiscoverStudiosHandler_ReturnsKnownStudios(t *testing.T) {
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore, nil, nil, nil, nil, nil))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/discover/studios")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 with no tmdb connection configured, got %d", resp.StatusCode)
	}
	var studios []tmdb.Studio
	if err := json.NewDecoder(resp.Body).Decode(&studios); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if len(studios) != len(tmdb.KnownStudios) {
		t.Errorf("expected %d studios, got %d", len(tmdb.KnownStudios), len(studios))
	}
}

// TestDiscoverNetworksHandler_ReturnsKnownNetworks is
// TestDiscoverStudiosHandler_ReturnsKnownStudios' direct sibling.
func TestDiscoverNetworksHandler_ReturnsKnownNetworks(t *testing.T) {
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore, nil, nil, nil, nil, nil))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/discover/networks")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	var networks []tmdb.Network
	if err := json.NewDecoder(resp.Body).Decode(&networks); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if len(networks) != len(tmdb.KnownNetworks) {
		t.Errorf("expected %d networks, got %d", len(tmdb.KnownNetworks), len(networks))
	}
}

// TestDiscoverKeywordsHandler_SearchesTMDB proves GET /api/discover/keywords
// proxies TMDB's /search/keyword using the shared "tmdb" connection
// (mode-independent, same reasoning as tmdbSearchHandler).
func TestDiscoverKeywordsHandler_SearchesTMDB(t *testing.T) {
	var gotPath, gotQuery string
	fake := fakeTMDB(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.Query().Get("query")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"results":[{"id":818,"name":"heist"}]}`))
	})

	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	overrideFixedURL(t, "tmdb", fake.URL)
	if err := connStore.Upsert(context.Background(), "tmdb", fake.URL, "key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore, nil, nil, nil, nil, nil))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/discover/keywords?q=heist")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if gotPath != "/search/keyword" || gotQuery != "heist" {
		t.Errorf("expected /search/keyword?query=heist, got path=%s query=%s", gotPath, gotQuery)
	}
	var keywords []tmdb.Keyword
	if err := json.NewDecoder(resp.Body).Decode(&keywords); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if len(keywords) != 1 || keywords[0].Name != "heist" {
		t.Errorf("unexpected keywords: %+v", keywords)
	}
}

// usReleaseFixture is a valid /movie/{id}/release_dates response with a past
// US digital release — HasUSRelease returns true for it.
const usReleaseFixture = `{"results": [{"iso_3166_1": "US", "release_dates": [{"type": 4, "release_date": "2020-01-01T00:00:00.000Z"}]}]}`

// noUSReleaseFixture has no US entry at all — HasUSRelease returns false.
const noUSReleaseFixture = `{"results": [{"iso_3166_1": "GB", "release_dates": [{"type": 3, "release_date": "2020-01-01T00:00:00.000Z"}]}]}`

// TestDiscoverHandler_FiltersUnreleasedMoviesFromTrending proves Movies
// trending/popular now filters out titles with no US release yet — of two
// TMDB items returned, only the one with a qualifying release_dates entry
// survives.
func TestDiscoverHandler_FiltersUnreleasedMoviesFromTrending(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/trending/movie/week", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"results":[{"id":1,"title":"Released Movie"},{"id":2,"title":"Unreleased Movie"}]}`))
	})
	mux.HandleFunc("/movie/1/release_dates", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(usReleaseFixture))
	})
	mux.HandleFunc("/movie/2/release_dates", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(noUSReleaseFixture))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	overrideFixedURL(t, "tmdb", srv.URL)
	if err := connStore.Upsert(context.Background(), "tmdb", srv.URL, "key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	apiSrv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore, nil, nil, nil, nil, nil))
	defer apiSrv.Close()

	resp, err := http.Get(apiSrv.URL + "/api/modes/movies/discover")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var items []tmdb.Item
	if err := json.NewDecoder(resp.Body).Decode(&items); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if len(items) != 1 || items[0].Title != "Released Movie" {
		t.Errorf("expected only the released movie to survive, got %+v", items)
	}
}

// TestDiscoverHandler_UpcomingMoviesNotFiltered proves the Upcoming category
// is deliberately exempt from the release-date filter — showing not-yet-
// released titles is that row's entire purpose.
func TestDiscoverHandler_UpcomingMoviesNotFiltered(t *testing.T) {
	var releaseDatesHit bool
	mux := http.NewServeMux()
	mux.HandleFunc("/movie/upcoming", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"results":[{"id":3,"title":"Future Movie"}]}`))
	})
	mux.HandleFunc("/movie/3/release_dates", func(w http.ResponseWriter, r *http.Request) {
		releaseDatesHit = true
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(noUSReleaseFixture))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	overrideFixedURL(t, "tmdb", srv.URL)
	if err := connStore.Upsert(context.Background(), "tmdb", srv.URL, "key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	apiSrv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore, nil, nil, nil, nil, nil))
	defer apiSrv.Close()

	resp, err := http.Get(apiSrv.URL + "/api/modes/movies/discover?category=upcoming")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	var items []tmdb.Item
	if err := json.NewDecoder(resp.Body).Decode(&items); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if len(items) != 1 || items[0].Title != "Future Movie" {
		t.Errorf("expected the unreleased movie to survive Upcoming unfiltered, got %+v", items)
	}
	if releaseDatesHit {
		t.Error("expected the release-date filter to never run for category=upcoming")
	}
}

// TestDiscoverHandler_SeriesNeverFiltered proves the release-date filter is
// Movies-only — Series items pass through untouched, and release_dates is
// never even called (there is no TV equivalent endpoint).
func TestDiscoverHandler_SeriesNeverFiltered(t *testing.T) {
	var releaseDatesHit bool
	mux := http.NewServeMux()
	mux.HandleFunc("/tv/popular", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"results":[{"id":4,"name":"Some Show"}]}`))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "release_dates") {
			releaseDatesHit = true
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(noUSReleaseFixture))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	overrideFixedURL(t, "tmdb", srv.URL)
	if err := connStore.Upsert(context.Background(), "tmdb", srv.URL, "key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	apiSrv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore, nil, nil, nil, nil, nil))
	defer apiSrv.Close()

	resp, err := http.Get(apiSrv.URL + "/api/modes/series/discover?category=popular")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	var items []tmdb.Item
	if err := json.NewDecoder(resp.Body).Decode(&items); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if len(items) != 1 || items[0].Title != "Some Show" {
		t.Errorf("expected the series item to pass through unfiltered, got %+v", items)
	}
	if releaseDatesHit {
		t.Error("expected release_dates to never be called for Series")
	}
}

// TestDiscoverHandler_RetriesNextPageWhenWholePageFilteredOut proves
// filterReleasedMovies' bounded retry: page 1 has one movie and it's
// unreleased (filters to empty), so the handler transparently tries page 2,
// which has a released movie — the frontend's "Show more" must not falsely
// see this as an exhausted row.
func TestDiscoverHandler_RetriesNextPageWhenWholePageFilteredOut(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/trending/movie/week", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("page") == "2" {
			w.Write([]byte(`{"results":[{"id":20,"title":"Released On Page 2"}]}`))
			return
		}
		w.Write([]byte(`{"results":[{"id":10,"title":"Unreleased On Page 1"}]}`))
	})
	mux.HandleFunc("/movie/10/release_dates", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(noUSReleaseFixture))
	})
	mux.HandleFunc("/movie/20/release_dates", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(usReleaseFixture))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	overrideFixedURL(t, "tmdb", srv.URL)
	if err := connStore.Upsert(context.Background(), "tmdb", srv.URL, "key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	apiSrv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore, nil, nil, nil, nil, nil))
	defer apiSrv.Close()

	resp, err := http.Get(apiSrv.URL + "/api/modes/movies/discover")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var items []tmdb.Item
	if err := json.NewDecoder(resp.Body).Decode(&items); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if len(items) != 1 || items[0].Title != "Released On Page 2" {
		t.Errorf("expected the retry to surface page 2's released movie, got %+v", items)
	}
}

// TestDiscoverHandler_GivesUpAfterMaxRetries proves the retry loop is
// bounded — if every page within the retry budget is fully unreleased, the
// handler returns an empty (not error) result rather than looping forever.
func TestDiscoverHandler_GivesUpAfterMaxRetries(t *testing.T) {
	var pagesFetched int
	mux := http.NewServeMux()
	mux.HandleFunc("/movie/popular", func(w http.ResponseWriter, r *http.Request) {
		pagesFetched++
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"results":[{"id":30,"title":"Always Unreleased"}]}`))
	})
	mux.HandleFunc("/movie/30/release_dates", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(noUSReleaseFixture))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	overrideFixedURL(t, "tmdb", srv.URL)
	if err := connStore.Upsert(context.Background(), "tmdb", srv.URL, "key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	apiSrv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore, nil, nil, nil, nil, nil))
	defer apiSrv.Close()

	resp, err := http.Get(apiSrv.URL + "/api/modes/movies/discover?category=popular")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 (empty result), got %d", resp.StatusCode)
	}
	var items []tmdb.Item
	if err := json.NewDecoder(resp.Body).Decode(&items); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if len(items) != 0 {
		t.Errorf("expected an empty result after exhausting retries, got %+v", items)
	}
	// 1 initial fetch + maxUnreleasedFilterRetries retries.
	if pagesFetched != 1+maxUnreleasedFilterRetries {
		t.Errorf("expected %d page fetches, got %d", 1+maxUnreleasedFilterRetries, pagesFetched)
	}
}

// TestDiscoverHandler_FailsOpenOnPerItemReleaseDatesError proves
// filterByUSRelease's fail-open behavior: a transient error on one item's
// /movie/{id}/release_dates call must not blank the whole row — the failing
// item is kept (not dropped), and the request still succeeds with 200.
func TestDiscoverHandler_FailsOpenOnPerItemReleaseDatesError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/trending/movie/week", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"results":[{"id":1,"title":"Released Movie"},{"id":2,"title":"Flaky Lookup Movie"}]}`))
	})
	mux.HandleFunc("/movie/1/release_dates", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(usReleaseFixture))
	})
	mux.HandleFunc("/movie/2/release_dates", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "upstream hiccup", http.StatusInternalServerError)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	overrideFixedURL(t, "tmdb", srv.URL)
	if err := connStore.Upsert(context.Background(), "tmdb", srv.URL, "key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	apiSrv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore, nil, nil, nil, nil, nil))
	defer apiSrv.Close()

	resp, err := http.Get(apiSrv.URL + "/api/modes/movies/discover")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 despite one item's release_dates erroring (fail-open), got %d", resp.StatusCode)
	}
	var items []tmdb.Item
	if err := json.NewDecoder(resp.Body).Decode(&items); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if len(items) != 2 {
		t.Errorf("expected both items to survive (the erroring one kept, not dropped), got %+v", items)
	}
}

// TestDiscoverTrailerHandler_ReturnsURL proves the trailer endpoint proxies
// TMDB's /movie/{id}/videos into {url: "..."}.
func TestDiscoverTrailerHandler_ReturnsURL(t *testing.T) {
	var gotPath string
	fake := fakeTMDB(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"results":[{"key":"abc123","site":"YouTube","type":"Trailer","official":true}]}`))
	})

	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	overrideFixedURL(t, "tmdb", fake.URL)
	if err := connStore.Upsert(context.Background(), "tmdb", fake.URL, "key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore, nil, nil, nil, nil, nil))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/modes/movies/discover/trailer?tmdbId=42")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if gotPath != "/movie/42/videos" {
		t.Errorf("unexpected path: %s", gotPath)
	}
	var got map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if got["url"] != "https://www.youtube.com/watch?v=abc123" {
		t.Errorf("unexpected url: %+v", got)
	}
}

// TestDiscoverTrailerHandler_SeriesUsesTVPath proves Series dispatches to
// /tv/{id}/videos, not /movie/{id}/videos.
func TestDiscoverTrailerHandler_SeriesUsesTVPath(t *testing.T) {
	var gotPath string
	fake := fakeTMDB(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"results":[]}`))
	})

	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	overrideFixedURL(t, "tmdb", fake.URL)
	if err := connStore.Upsert(context.Background(), "tmdb", fake.URL, "key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore, nil, nil, nil, nil, nil))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/modes/series/discover/trailer?tmdbId=7")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if gotPath != "/tv/7/videos" {
		t.Errorf("unexpected path: %s", gotPath)
	}
	var got map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if got["url"] != "" {
		t.Errorf("expected empty url when TMDB has no trailer, got %+v", got)
	}
}

// TestDiscoverTrailerHandler_RejectsAdult proves Adult mode is a 400 —
// Adult has no TMDB id to resolve a trailer from.
func TestDiscoverTrailerHandler_RejectsAdult(t *testing.T) {
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore, nil, nil, nil, nil, nil))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/modes/adult/discover/trailer?tmdbId=1")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for adult mode, got %d", resp.StatusCode)
	}
}

// TestDiscoverTrailerHandler_RequiresTmdbID proves a missing/invalid tmdbId
// is a 400, not a zero-id upstream lookup.
func TestDiscoverTrailerHandler_RequiresTmdbID(t *testing.T) {
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	if err := connStore.Upsert(context.Background(), "tmdb", "http://tmdb.local", "key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore, nil, nil, nil, nil, nil))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/modes/movies/discover/trailer")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 without a tmdbId param, got %d", resp.StatusCode)
	}
}

// TestDiscoverTrailerHandler_RejectsNonPositiveTmdbID proves tmdbId=0 and
// negative values are also a 400, not a doomed upstream lookup — the
// page-param convention discoverHandler already uses (page must be > 0).
func TestDiscoverTrailerHandler_RejectsNonPositiveTmdbID(t *testing.T) {
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	if err := connStore.Upsert(context.Background(), "tmdb", "http://tmdb.local", "key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore, nil, nil, nil, nil, nil))
	defer srv.Close()

	for _, id := range []string{"0", "-5"} {
		resp, err := http.Get(srv.URL + "/api/modes/movies/discover/trailer?tmdbId=" + id)
		if err != nil {
			t.Fatalf("GET failed: %v", err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("tmdbId=%s: expected 400, got %d", id, resp.StatusCode)
		}
	}
}

// TestDiscoverTrailerHandler_TMDBNotConfigured proves the endpoint 400s when
// tmdb isn't configured, matching every other Discover handler's convention.
func TestDiscoverTrailerHandler_TMDBNotConfigured(t *testing.T) {
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore, nil, nil, nil, nil, nil))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/modes/movies/discover/trailer?tmdbId=1")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 when tmdb isn't configured, got %d", resp.StatusCode)
	}
}

// TestDiscoverKeywordsHandler_RequiresQParam proves a missing q is a 400.
func TestDiscoverKeywordsHandler_RequiresQParam(t *testing.T) {
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	if err := connStore.Upsert(context.Background(), "tmdb", "http://tmdb.local", "key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore, nil, nil, nil, nil, nil))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/discover/keywords")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 without a q param, got %d", resp.StatusCode)
	}
}
