package tvdb

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSearchEpisodes(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v4/login", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"status":"success","data":{"token":"tok-ep"}}`)
	})
	mux.HandleFunc("GET /v4/search", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("type") != "episode" {
			t.Errorf("expected type=episode, got %q", r.URL.Query().Get("type"))
		}
		fmt.Fprint(w, `{"status":"success","data":[{
			"id":1001,"name":"Duck Soup","seasonNumber":3,"number":1,"seriesId":73910
		}]}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := New(Config{BaseURL: srv.URL, APIKey: "k"}, srv.Client())
	results, err := c.SearchEpisodes(context.Background(), "duck soup")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Name != "Duck Soup" || results[0].SeriesID != 73910 {
		t.Errorf("unexpected result: %+v", results[0])
	}
	if results[0].SeasonNumber != 3 || results[0].EpisodeNumber != 1 {
		t.Errorf("slot: S%d E%d", results[0].SeasonNumber, results[0].EpisodeNumber)
	}
}

func TestSeriesBrief(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v4/login", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"status":"success","data":{"token":"tok-series"}}`)
	})
	mux.HandleFunc("GET /v4/series/73910", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"status":"success","data":{"name":"Laurel & Hardy","year":"1921"}}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := New(Config{BaseURL: srv.URL, APIKey: "k"}, srv.Client())
	name, year, err := c.SeriesBrief(context.Background(), 73910)
	if err != nil {
		t.Fatal(err)
	}
	if name != "Laurel & Hardy" || year != 1921 {
		t.Errorf("got %q %d", name, year)
	}
}
