package tvdb

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSeriesPosterURL_PrefersPosterType(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v4/login", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "success",
			"data":   map[string]string{"token": "tok"},
		})
	})
	mux.HandleFunc("/v4/series/42/artworks", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{"type": 3, "image": "/banners/fanart/x.jpg"},
				{"type": 2, "image": "/banners/posters/p.jpg"},
			},
		})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	c := New(Config{BaseURL: srv.URL, APIKey: "k"}, srv.Client())
	got, err := c.SeriesPosterURL(context.Background(), 42)
	if err != nil {
		t.Fatalf("SeriesPosterURL: %v", err)
	}
	want := ArtworkBaseURL + "/banners/posters/p.jpg"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestAbsolutizeArtwork_HTTPSPassthrough(t *testing.T) {
	in := "https://cdn.example/p.jpg"
	if got := absolutizeArtwork(in); got != in {
		t.Fatalf("got %q", got)
	}
	if got := absolutizeArtwork("http://cdn.example/p.jpg"); got != "" {
		t.Fatalf("http must be rejected, got %q", got)
	}
}
