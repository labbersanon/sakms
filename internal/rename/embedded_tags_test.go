package rename_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labbersanon/sakms/internal/mediainfo"
	"github.com/labbersanon/sakms/internal/rename"
	"github.com/labbersanon/sakms/internal/tmdb"
)

func TestResolveMovieTMDBFromTags_TMDBID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/movie/603" {
			t.Fatalf("path %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": 603, "title": "The Matrix", "release_date": "1999-03-31", "poster_path": "/x.jpg",
		})
	}))
	t.Cleanup(srv.Close)
	c := tmdb.New(tmdb.Config{BaseURL: srv.URL, APIKey: "k"}, srv.Client())
	id, title, year, err := rename.ResolveMovieTMDBFromTags(context.Background(), c, mediainfo.Tags{TMDBID: 603})
	if err != nil || id != 603 || title != "The Matrix" || year != 1999 {
		t.Fatalf("got id=%d title=%q year=%d err=%v", id, title, year, err)
	}
}

func TestResolveMovieTMDBFromTags_TitleYear(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"results": []map[string]any{
				{"id": 1, "title": "Wrong Year", "release_date": "1985-01-01"},
				{"id": 2, "title": "Copacabana", "release_date": "1947-05-30"},
			},
		})
	}))
	t.Cleanup(srv.Close)
	c := tmdb.New(tmdb.Config{BaseURL: srv.URL, APIKey: "k"}, srv.Client())
	id, title, year, err := rename.ResolveMovieTMDBFromTags(context.Background(), c, mediainfo.Tags{
		Title: "Copacabana", Year: 1947,
	})
	if err != nil || id != 2 || title != "Copacabana" || year != 1947 {
		t.Fatalf("got id=%d title=%q year=%d err=%v", id, title, year, err)
	}
}

func TestResolveMovieTMDBFromTags_Empty(t *testing.T) {
	id, _, _, err := rename.ResolveMovieTMDBFromTags(context.Background(), nil, mediainfo.Tags{})
	if err != nil || id != 0 {
		t.Fatalf("got id=%d err=%v", id, err)
	}
}
