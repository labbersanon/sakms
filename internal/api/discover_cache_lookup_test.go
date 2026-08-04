package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/labbersanon/sakms/internal/db"
	"github.com/labbersanon/sakms/internal/discoverrefresh"
)

// TestLookupDiscoverCache_NilStoreIsMiss confirms a nil *discoverrefresh.Store
// (today's behaviour before any cache row has ever been written, and every
// non-cache-exercising NewMux call site) is a miss with no opinion on the
// upstream page — never a panic, never an error.
func TestLookupDiscoverCache_NilStoreIsMiss(t *testing.T) {
	items, hit, liveRawPage := lookupDiscoverCache(context.Background(), nil, "tmdb", "movies:trending", 1)
	if hit {
		t.Errorf("hit = true, want false for a nil store")
	}
	if items != nil {
		t.Errorf("items = %v, want nil for a nil store", items)
	}
	if liveRawPage != 0 {
		t.Errorf("liveRawPage = %d, want 0 (no opinion) for a nil store", liveRawPage)
	}
}

// TestLookupDiscoverCache_MissingRowIsMiss confirms a real store with no row
// for the given key is also a miss — the cold-install case.
func TestLookupDiscoverCache_MissingRowIsMiss(t *testing.T) {
	s := newTestDiscoverCache(t)
	items, hit, liveRawPage := lookupDiscoverCache(context.Background(), s, "tmdb", "movies:trending", 1)
	if hit {
		t.Errorf("hit = true, want false for a missing row")
	}
	if items != nil {
		t.Errorf("items = %v, want nil for a missing row", items)
	}
	if liveRawPage != 0 {
		t.Errorf("liveRawPage = %d, want 0 (no opinion) for a missing row", liveRawPage)
	}
}

// TestLookupDiscoverCache_HitServesStoredSlice confirms a cache hit delegates
// to Entry.Slice and returns the cached items.
func TestLookupDiscoverCache_HitServesStoredSlice(t *testing.T) {
	s := newTestDiscoverCache(t)
	ctx := context.Background()
	payload := []json.RawMessage{
		json.RawMessage(`{"id":1}`),
		json.RawMessage(`{"id":2}`),
	}
	if err := s.Put(ctx, "tmdb", "movies:trending", payload, 20, 1, true); err != nil {
		t.Fatalf("Put: %v", err)
	}

	items, hit, liveRawPage := lookupDiscoverCache(ctx, s, "tmdb", "movies:trending", 1)
	if !hit {
		t.Fatalf("hit = false, want true after Put")
	}
	if liveRawPage != 0 {
		t.Errorf("liveRawPage = %d, want 0 on a hit", liveRawPage)
	}
	if len(items) != len(payload) {
		t.Fatalf("items length = %d, want %d", len(items), len(payload))
	}
	for i := range payload {
		if string(items[i]) != string(payload[i]) {
			t.Errorf("items[%d] = %s, want %s", i, items[i], payload[i])
		}
	}
}

// newTestDiscoverCache builds a *discoverrefresh.Store over a fresh temp
// SQLite file, mirroring discoverrefresh's own newTestStore helper.
func newTestDiscoverCache(t *testing.T) *discoverrefresh.Store {
	t.Helper()
	sqlDB, err := db.Open(filepath.Join(t.TempDir(), "sakms.db"))
	if err != nil {
		t.Fatalf("opening db: %v", err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	return discoverrefresh.NewStore(sqlDB)
}

// TestWriteRawJSONArray_TrailingNewline confirms the cached writer matches
// every live encoder's json.NewEncoder(...).Encode(...), which appends a
// trailing "\n" — Critic finding H1. Omitting it would make a cached
// response one byte different from the live one it replaces.
func TestWriteRawJSONArray_TrailingNewline(t *testing.T) {
	w := httptest.NewRecorder()
	writeRawJSONArray(w, []json.RawMessage{
		json.RawMessage(`{"id":1}`),
		json.RawMessage(`{"id":2}`),
	})

	got := w.Body.String()
	want := `[{"id":1},{"id":2}]` + "\n"
	if got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
}

// TestWriteRawJSONArray_Empty confirms an empty item list still emits a
// well-formed, byte-transparent "[]\n" rather than "null" — this is the
// exhausted-and-hit case from Entry.Slice.
func TestWriteRawJSONArray_Empty(t *testing.T) {
	w := httptest.NewRecorder()
	writeRawJSONArray(w, []json.RawMessage{})

	got := w.Body.String()
	want := "[]\n"
	if got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}
