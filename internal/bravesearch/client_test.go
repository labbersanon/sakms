package bravesearch

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestSearch_ParsesResultsAndSendsHeaders(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("q"); got != "some query" {
			t.Errorf("expected q=some query, got %q", got)
		}
		if got := r.URL.Query().Get("count"); got != "5" {
			t.Errorf("expected count=5, got %q", got)
		}
		if got := r.Header.Get("X-Subscription-Token"); got != "testkey" {
			t.Errorf("expected X-Subscription-Token header, got %q", got)
		}
		if got := r.Header.Get("Accept"); got != "application/json" {
			t.Errorf("expected Accept: application/json, got %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"web":{"results":[{"title":"T1","description":"D1","url":"https://example.com/1"}]}}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "testkey", &http.Client{Timeout: 5 * time.Second})
	out, err := c.Search(context.Background(), "some query", 5)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 1 || out[0].Title != "T1" || out[0].URL != "https://example.com/1" {
		t.Fatalf("got %+v", out)
	}
}

func TestSearch_NoResults(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"web":{"results":[]}}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "testkey", &http.Client{Timeout: 5 * time.Second})
	out, err := c.Search(context.Background(), "nothing", 5)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 0 {
		t.Fatalf("expected no results, got %+v", out)
	}
}

func TestSearch_NonOKStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	c := New(srv.URL, "testkey", &http.Client{Timeout: 5 * time.Second})
	_, err := c.Search(context.Background(), "query", 5)
	if err == nil {
		t.Fatal("expected an error on non-200 status (e.g. rate limit)")
	}
}
