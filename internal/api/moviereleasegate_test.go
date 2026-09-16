package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/tmdb"
)

// newGateTestClient builds a tmdb.Client pointing at the given httptest server
// URL. Per-client cache so sequential tests with recycled ephemeral ports don't
// cross-contaminate (same pattern as internal/tmdb/client_test.go).
func newGateTestClient(t *testing.T, srv *httptest.Server) *tmdb.Client {
	t.Helper()
	c := tmdb.New(tmdb.Config{BaseURL: srv.URL, APIKey: "test-key"}, srv.Client())
	return c
}

// releaseDatesServer builds a minimal fake TMDB server that returns the given
// release-dates JSON body for any /movie/{id}/release_dates request, and counts
// how many times that path was hit.
func releaseDatesServer(t *testing.T, body string) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "" {
			calls.Add(1)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

// --- gateMovieGrab unit tests ---

func TestGateMovieGrab_SeriesAlwaysAllows_NoTMDBCall(t *testing.T) {
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		json.NewEncoder(w).Encode(map[string]any{})
	}))
	defer srv.Close()
	c := newGateTestClient(t, srv)

	_, blocked, _ := gateMovieGrab(context.Background(), c, mode.Series, 42)
	if blocked {
		t.Error("expected Series to allow unconditionally")
	}
	if calls.Load() != 0 {
		t.Errorf("expected zero TMDB calls for Series; got %d", calls.Load())
	}
}

func TestGateMovieGrab_AdultAlwaysAllows_NoTMDBCall(t *testing.T) {
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		json.NewEncoder(w).Encode(map[string]any{})
	}))
	defer srv.Close()
	c := newGateTestClient(t, srv)

	_, blocked, _ := gateMovieGrab(context.Background(), c, mode.Adult, 42)
	if blocked {
		t.Error("expected Adult to allow unconditionally")
	}
	if calls.Load() != 0 {
		t.Errorf("expected zero TMDB calls for Adult; got %d", calls.Load())
	}
}

// TestGateMovieGrab_ZeroTMDBID_Allows pins the named-hole policy: a Movies row
// with tmdbID <= 0 is allowed without a TMDB call. Do NOT close this without
// also updating the doc comment in gateMovieGrab.
func TestGateMovieGrab_ZeroTMDBID_Allows_NoTMDBCall(t *testing.T) {
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		json.NewEncoder(w).Encode(map[string]any{})
	}))
	defer srv.Close()
	c := newGateTestClient(t, srv)

	_, blocked, _ := gateMovieGrab(context.Background(), c, mode.Movies, 0)
	if blocked {
		t.Error("expected tmdbID=0 to allow (named hole)")
	}
	if calls.Load() != 0 {
		t.Errorf("expected zero TMDB calls for tmdbID=0; got %d", calls.Load())
	}
}

func TestGateMovieGrab_NilClient_Blocks(t *testing.T) {
	_, blocked, reason := gateMovieGrab(context.Background(), nil, mode.Movies, 42)
	if !blocked {
		t.Error("expected nil TMDB client to block")
	}
	if reason == "" {
		t.Error("expected a non-empty reason when nil client blocks")
	}
}

func TestGateMovieGrab_TheatricalOnly_Blocks_HoldIsSentinel(t *testing.T) {
	srv, _ := releaseDatesServer(t, `{"results": [
		{"iso_3166_1": "US", "release_dates": [{"type": 3, "release_date": "2020-01-01T00:00:00.000Z"}]}
	]}`)
	c := newGateTestClient(t, srv)

	rel, blocked, _ := gateMovieGrab(context.Background(), c, mode.Movies, 1)
	if !blocked {
		t.Error("expected theatrical-only to block")
	}
	if rel.HoldUntil != sentinelTime {
		t.Errorf("hold_until = %v, want sentinel %v", rel.HoldUntil, sentinelTime)
	}
}

func TestGateMovieGrab_TMDB500_Blocks_HoldIsSentinel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "server error", http.StatusInternalServerError)
	}))
	defer srv.Close()
	c := newGateTestClient(t, srv)

	rel, blocked, _ := gateMovieGrab(context.Background(), c, mode.Movies, 1)
	if !blocked {
		t.Error("expected TMDB 500 to block (fail closed)")
	}
	if rel.HoldUntil != sentinelTime {
		t.Errorf("hold_until = %v, want sentinel %v", rel.HoldUntil, sentinelTime)
	}
}

func TestGateMovieGrab_FutureTypedDate_Blocks_HoldIsDatePlus24h(t *testing.T) {
	srv, _ := releaseDatesServer(t, `{"results": [
		{"iso_3166_1": "US", "release_dates": [{"type": 4, "release_date": "2099-06-15T00:00:00.000Z"}]}
	]}`)
	c := newGateTestClient(t, srv)

	rel, blocked, _ := gateMovieGrab(context.Background(), c, mode.Movies, 1)
	if !blocked {
		t.Error("expected future typed date to block")
	}
	if rel.Known != true {
		t.Error("expected Known=true for a future typed date")
	}
	// HoldUntil should be 2099-06-15 + 24h.
	if rel.HoldUntil.Year() != 2099 || rel.HoldUntil.Month() != 6 || rel.HoldUntil.Day() != 16 {
		t.Errorf("expected HoldUntil 2099-06-16 (day after), got %v", rel.HoldUntil)
	}
}

func TestGateMovieGrab_PastTypedDate_Allows(t *testing.T) {
	srv, _ := releaseDatesServer(t, `{"results": [
		{"iso_3166_1": "US", "release_dates": [{"type": 4, "release_date": "2020-01-01T00:00:00.000Z"}]}
	]}`)
	c := newGateTestClient(t, srv)

	rel, blocked, _ := gateMovieGrab(context.Background(), c, mode.Movies, 1)
	if blocked {
		t.Error("expected past digital release to allow")
	}
	if !rel.Acquirable {
		t.Error("expected Acquirable=true")
	}
}
