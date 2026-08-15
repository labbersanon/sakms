package omdb

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newTestClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return New(Config{BaseURL: srv.URL, APIKey: "test-key"}, srv.Client())
}

func TestByIMDBID_ParsesIMDb(t *testing.T) {
	var gotQuery string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
		  "imdbRating":"8.1",
		  "imdbVotes":"1,234,567",
		  "tomatoMeter":"93",
		  "tomatoUserMeter":"66",
		  "Ratings":[{"Source":"Rotten Tomatoes","Value":"93%"}],
		  "Response":"True"
		}`))
	})
	got, err := c.ByIMDBID(context.Background(), "tt0137523")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(gotQuery, "apikey=test-key") ||
		!strings.Contains(gotQuery, "i=tt0137523") {
		t.Errorf("unexpected query: %s", gotQuery)
	}
	if strings.Contains(gotQuery, "tomatoes=") {
		t.Errorf("tomatoes param must not be sent: %s", gotQuery)
	}
	if got.IMDbRating != 8.1 || got.IMDbVotes != 1234567 {
		t.Errorf("unexpected imdb: %+v", got)
	}
}

func TestByIMDBID_NAVotesAreZero(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
		  "imdbRating":"7.5",
		  "imdbVotes":"N/A",
		  "Response":"True"
		}`))
	})
	got, err := c.ByIMDBID(context.Background(), "tt1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.IMDbRating != 7.5 || got.IMDbVotes != 0 {
		t.Errorf("unexpected imdb: %+v", got)
	}
}

func TestByIMDBID_MissingTitleErrors(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"Response":"False","Error":"Movie not found!"}`))
	})
	if _, err := c.ByIMDBID(context.Background(), "tt0"); err == nil {
		t.Fatal("expected an error for a missing title")
	}
}

func TestPing_InvalidKeyErrors(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"Response":"False","Error":"Invalid API key!"}`))
	})
	if err := c.Ping(context.Background()); err == nil {
		t.Fatal("expected an error for an invalid key")
	}
}

func TestPing_ValidKeyWithNoIDSucceeds(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"Response":"False","Error":"Incorrect IMDb ID."}`))
	})
	if err := c.Ping(context.Background()); err != nil {
		t.Fatalf("valid key should ping OK: %v", err)
	}
}
