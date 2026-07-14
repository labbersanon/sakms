package trakt

import (
	"context"
	"net/http"
	"testing"
)

const watchlistFixture = `[
  {"type":"movie","movie":{"title":"Some Movie","year":2023,"ids":{"trakt":1,"slug":"some-movie-2023","imdb":"tt1234567","tmdb":100}}},
  {"type":"show","show":{"title":"Some Show","year":2021,"ids":{"trakt":2,"slug":"some-show","imdb":"tt7654321","tmdb":200}}},
  {"type":"movie","movie":{"title":"No TMDB Match","year":2020,"ids":{"trakt":3,"slug":"no-tmdb-match"}}},
  {"type":"episode","episode":{"season":1,"number":1}}
]`

func TestWatchlist_NormalizesAndSkipsUnusableEntries(t *testing.T) {
	var gotAuth, gotAPIKey, gotVersion string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/sync/watchlist" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		gotAuth = r.Header.Get("Authorization")
		gotAPIKey = r.Header.Get("trakt-api-key")
		gotVersion = r.Header.Get("trakt-api-version")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(watchlistFixture))
	})

	items, err := c.Watchlist(context.Background(), "user-access-token")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotAuth != "Bearer user-access-token" {
		t.Errorf("expected bearer auth header, got %q", gotAuth)
	}
	if gotAPIKey != "test-client-id" {
		t.Errorf("expected trakt-api-key header, got %q", gotAPIKey)
	}
	if gotVersion != "2" {
		t.Errorf("expected trakt-api-version 2, got %q", gotVersion)
	}

	// The no-tmdb-match movie and the episode entry must both be skipped.
	if len(items) != 2 {
		t.Fatalf("expected 2 usable items, got %d: %+v", len(items), items)
	}
	if items[0].Type != "movie" || items[0].Title != "Some Movie" || items[0].TMDBID != 100 || items[0].Year != 2023 {
		t.Errorf("unexpected movie item: %+v", items[0])
	}
	if items[1].Type != "show" || items[1].Title != "Some Show" || items[1].TMDBID != 200 {
		t.Errorf("unexpected show item: %+v", items[1])
	}
}

func TestWatchlist_PropagatesHTTPError(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})
	_, err := c.Watchlist(context.Background(), "expired-token")
	if err == nil {
		t.Fatal("expected an error for a 401 response")
	}
}

func TestPing_ValidClientIDGets200(t *testing.T) {
	var gotAPIKey string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/movies/trending" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		gotAPIKey = r.Header.Get("trakt-api-key")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[]`))
	})

	if err := c.Ping(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotAPIKey != "test-client-id" {
		t.Errorf("expected trakt-api-key header, got %q", gotAPIKey)
	}
}

func TestPing_InvalidClientIDGets401(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})
	if err := c.Ping(context.Background()); err == nil {
		t.Fatal("expected an error for a 401 response")
	}
}

func TestPing_EmptyClientIDFailsWithoutARequest(t *testing.T) {
	requested := false
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		requested = true
	})
	c.cfg.ClientID = ""
	if err := c.Ping(context.Background()); err == nil {
		t.Fatal("expected an error for an empty client_id")
	}
	if requested {
		t.Error("expected no HTTP request for an empty client_id")
	}
}
