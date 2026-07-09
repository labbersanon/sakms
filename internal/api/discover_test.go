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

func TestDiscoverHandler_MoviesUsesMovieMediaType(t *testing.T) {
	var gotPath string
	fake := fakeTMDB(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"results":[{"id":1,"title":"Some Movie","poster_path":"/x.jpg","vote_average":7.5}]}`))
	})

	connStore, propStore, allowStore, settingsStore, grabsStore := testStores(t)
	ctx := context.Background()
	if err := connStore.Upsert(ctx, "radarr", "http://radarr.local", "key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := connStore.Upsert(ctx, "tmdb", fake.URL, "key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), settingsStore, grabsStore))
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

	connStore, propStore, allowStore, settingsStore, grabsStore := testStores(t)
	ctx := context.Background()
	if err := connStore.Upsert(ctx, "sonarr", "http://sonarr.local", "key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := connStore.Upsert(ctx, "tmdb", fake.URL, "key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), settingsStore, grabsStore))
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
	connStore, propStore, allowStore, settingsStore, grabsStore := testStores(t)
	if err := connStore.Upsert(context.Background(), "radarr", "http://radarr.local", "key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), settingsStore, grabsStore))
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

func TestResolveTVDBIDHandler_ResolvesID(t *testing.T) {
	fake := fakeTMDB(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/tv/2/external_ids" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"tvdb_id":12345}`))
	})

	connStore, propStore, allowStore, settingsStore, grabsStore := testStores(t)
	ctx := context.Background()
	if err := connStore.Upsert(ctx, "sonarr", "http://sonarr.local", "key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := connStore.Upsert(ctx, "tmdb", fake.URL, "key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), settingsStore, grabsStore))
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
	connStore, propStore, allowStore, settingsStore, grabsStore := testStores(t)
	if err := connStore.Upsert(context.Background(), "sonarr", "http://sonarr.local", "key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), settingsStore, grabsStore))
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
