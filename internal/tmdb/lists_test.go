package tmdb

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
)

func TestAccountAndWatchlists(t *testing.T) {
	var paths []string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if r.URL.Query().Get("session_id") != "sess" {
			t.Errorf("missing session_id on %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/account":
			json.NewEncoder(w).Encode(map[string]any{"id": 9})
		case "/account/9/watchlist/movies":
			json.NewEncoder(w).Encode(map[string]any{
				"page": 1, "total_pages": 1,
				"results": []map[string]any{{"id": 100, "title": "Some Movie", "release_date": "2020-01-01"}},
			})
		case "/account/9/watchlist/tv":
			json.NewEncoder(w).Encode(map[string]any{
				"page": 1, "total_pages": 1,
				"results": []map[string]any{{"id": 200, "name": "Some Show", "first_air_date": "2021-01-01"}},
			})
		default:
			http.NotFound(w, r)
		}
	})
	id, err := c.Account(context.Background(), "sess")
	if err != nil || id != 9 {
		t.Fatalf("Account = %d, %v", id, err)
	}
	movies, err := c.WatchlistMovies(context.Background(), 9, "sess")
	if err != nil || len(movies) != 1 || movies[0].Title != "Some Movie" || movies[0].MediaType != Movie {
		t.Fatalf("WatchlistMovies = %+v, %v", movies, err)
	}
	shows, err := c.WatchlistTV(context.Background(), 9, "sess")
	if err != nil || len(shows) != 1 || shows[0].Title != "Some Show" || shows[0].MediaType != TV {
		t.Fatalf("WatchlistTV = %+v, %v", shows, err)
	}
}

func TestListItems(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/list/123" {
			t.Errorf("path = %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"items": []map[string]any{
				{"id": 100, "title": "Some Movie", "media_type": "movie"},
				{"id": 200, "name": "Some Show", "media_type": "tv"},
			},
		})
	})
	items, err := c.ListItems(context.Background(), "123")
	if err != nil {
		t.Fatalf("ListItems: %v", err)
	}
	if len(items) != 2 || items[0].MediaType != Movie || items[1].MediaType != TV {
		t.Fatalf("items = %+v", items)
	}
}

func TestFindByIMDBID(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/find/tt0111161" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if r.URL.Query().Get("external_source") != "imdb_id" {
			t.Errorf("external_source = %s", r.URL.Query().Get("external_source"))
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"movie_results": []map[string]any{{"id": 100}},
			"tv_results":    []map[string]any{{"id": 200}},
		})
	})
	movieID, tvID, err := c.FindByIMDBID(context.Background(), "tt0111161")
	if err != nil || movieID != 100 || tvID != 200 {
		t.Fatalf("FindByIMDBID = %d, %d, %v", movieID, tvID, err)
	}
	tvOnly, err := c.FindTVByIMDBID(context.Background(), "tt0111161")
	if err != nil || tvOnly != 200 {
		t.Fatalf("FindTVByIMDBID = %d, %v", tvOnly, err)
	}
}
