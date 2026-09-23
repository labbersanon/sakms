package tmdb_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/labbersanon/sakms/internal/tmdb"
	"golang.org/x/time/rate"
)

func TestSharedRateLimit_WaitsOnBurstExhaustion(t *testing.T) {
	t.Cleanup(tmdb.ResetSharedRateLimitForTest)
	// Tiny budget: 1 token, refill very slowly so the second live call blocks.
	tmdb.SetSharedRateLimitForTest(rate.Every(time.Hour), 1)

	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"results": []any{}})
	}))
	t.Cleanup(srv.Close)

	c := tmdb.New(tmdb.Config{BaseURL: srv.URL, APIKey: "k", BypassCache: true}, srv.Client())

	ctx := context.Background()
	if _, err := c.SearchMovies(ctx, "one"); err != nil {
		t.Fatal(err)
	}

	ctx2, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := c.SearchMovies(ctx2, "two")
	if err == nil {
		t.Fatal("expected second call to block and hit ctx deadline")
	}
	if hits.Load() != 1 {
		t.Fatalf("hits=%d, want 1 (second call must not reach the network)", hits.Load())
	}
}

func TestSharedRateLimit_CacheHitSkipsWait(t *testing.T) {
	t.Cleanup(tmdb.ResetSharedRateLimitForTest)
	tmdb.SetSharedRateLimitForTest(rate.Every(time.Hour), 1)

	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"results": []map[string]any{{"id": 1, "title": "X", "release_date": "2000-01-01"}},
		})
	}))
	t.Cleanup(srv.Close)

	c := tmdb.New(tmdb.Config{BaseURL: srv.URL, APIKey: "k"}, srv.Client())
	if _, err := c.SearchMovies(context.Background(), "cached"); err != nil {
		t.Fatal(err)
	}
	// Second identical call should be cache-served without consuming another token.
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := c.SearchMovies(ctx, "cached"); err != nil {
		t.Fatalf("cache hit should not wait on rate limit: %v", err)
	}
	if hits.Load() != 1 {
		t.Fatalf("hits=%d, want 1", hits.Load())
	}
}
