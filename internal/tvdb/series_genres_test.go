package tvdb

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestSeriesGenres_Animation(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v4/login", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "success",
			"data":   map[string]string{"token": "tok"},
		})
	})
	mux.HandleFunc("/v4/series/7266", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"name": "Looney Tunes",
				"genres": []map[string]any{
					{"id": 16, "name": "Animation", "slug": "animation"},
					{"id": 4, "name": "Comedy", "slug": "comedy"},
				},
			},
		})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	c := New(Config{BaseURL: srv.URL, APIKey: "k"}, srv.Client())
	got, err := c.SeriesGenres(context.Background(), 7266)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"Animation", "Comedy"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestSeriesGenres_InvalidID(t *testing.T) {
	c := New(Config{BaseURL: "http://unused", APIKey: "k"}, http.DefaultClient)
	if _, err := c.SeriesGenres(context.Background(), 0); err == nil {
		t.Fatal("expected error for series id 0")
	}
}
