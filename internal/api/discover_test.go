package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/curtiswtaylorjr/sakms/internal/tmdb"
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
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"results":[{"id":1,"title":"Some Movie","poster_path":"/x.jpg","vote_average":7.5}]}`))
	})

	connStore, propStore, allowStore, settingsStore, grabsStore, libStore := testStores(t)
	ctx := context.Background()
	if err := connStore.Upsert(ctx, "radarr", "http://radarr.local", "key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := connStore.Upsert(ctx, "tmdb", fake.URL, "key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore))
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

	connStore, propStore, allowStore, settingsStore, grabsStore, libStore := testStores(t)
	ctx := context.Background()
	if err := connStore.Upsert(ctx, "sonarr", "http://sonarr.local", "key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := connStore.Upsert(ctx, "tmdb", fake.URL, "key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore))
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
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore := testStores(t)
	if err := connStore.Upsert(context.Background(), "radarr", "http://radarr.local", "key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore))
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
		gotPage = r.URL.Query().Get("page")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"results":[{"id":1,"title":"Some Movie"}]}`))
	})

	connStore, propStore, allowStore, settingsStore, grabsStore, libStore := testStores(t)
	if err := connStore.Upsert(context.Background(), "tmdb", fake.URL, "key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore))
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

	connStore, propStore, allowStore, settingsStore, grabsStore, libStore := testStores(t)
	if err := connStore.Upsert(context.Background(), "tmdb", fake.URL, "key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore))
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
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore := testStores(t)
	if err := connStore.Upsert(context.Background(), "tmdb", "http://tmdb.local", "key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore))
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
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore := testStores(t)
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore))
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

	connStore, propStore, allowStore, settingsStore, grabsStore, libStore := testStores(t)
	ctx := context.Background()
	if err := connStore.Upsert(ctx, "sonarr", "http://sonarr.local", "key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := connStore.Upsert(ctx, "tmdb", fake.URL, "key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore))
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
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore := testStores(t)
	if err := connStore.Upsert(context.Background(), "sonarr", "http://sonarr.local", "key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore))
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
