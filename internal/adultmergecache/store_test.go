package adultmergecache

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/labbersanon/sakms/internal/db"
)

// newTestStore opens a fresh migrated SQLite DB (so the adult_merged_row_cache
// table from migration 0045 exists) and returns a Store over it plus the raw
// *sql.DB (for row-count assertions the Store's own API deliberately doesn't
// expose).
func newTestStore(t *testing.T) (*Store, *sql.DB) {
	t.Helper()
	sqlDB, err := db.Open(filepath.Join(t.TempDir(), "sakms.db"))
	if err != nil {
		t.Fatalf("opening db: %v", err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	return New(sqlDB), sqlDB
}

// rowCount returns how many cache rows exist for the exact key — used to prove
// an upsert overwrites in place rather than inserting a duplicate.
func rowCount(t *testing.T, sqlDB *sql.DB, rowType string, page, perPage int) int {
	t.Helper()
	var n int
	if err := sqlDB.QueryRow(
		`SELECT COUNT(*) FROM adult_merged_row_cache WHERE row_type = ? AND page = ? AND per_page = ?`,
		rowType, page, perPage,
	).Scan(&n); err != nil {
		t.Fatalf("counting rows: %v", err)
	}
	return n
}

func TestStore_PutGetRoundTrip(t *testing.T) {
	store, _ := newTestStore(t)
	ctx := context.Background()

	payload := json.RawMessage(`[{"name":"Riley Reid","source":"tpdb"}]`)
	if err := store.Put(ctx, "performers", 1, 20, payload, true, "2026-07-28T00:00:00Z"); err != nil {
		t.Fatalf("Put: %v", err)
	}

	got, found, err := store.Get(ctx, "performers", 1, 20)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !found {
		t.Fatal("expected the row to be found after Put")
	}
	if string(got.Items) != string(payload) {
		t.Errorf("payload round-trip mismatch: got %q, want %q", got.Items, payload)
	}
	if !got.HasMore {
		t.Errorf("HasMore round-trip mismatch: got false, want true")
	}
	if got.ComputedAt != "2026-07-28T00:00:00Z" {
		t.Errorf("ComputedAt round-trip mismatch: got %q", got.ComputedAt)
	}
}

func TestStore_GetMissReturnsFalseNoError(t *testing.T) {
	store, _ := newTestStore(t)
	got, found, err := store.Get(context.Background(), "performers", 1, 20)
	if err != nil {
		t.Fatalf("a miss must not error, got %v", err)
	}
	if found {
		t.Errorf("expected found=false on an empty store, got true")
	}
	if len(got.Items) != 0 || got.HasMore || got.ComputedAt != "" {
		t.Errorf("expected a zero CachedPage on a miss, got %+v", got)
	}
}

func TestStore_PutUpsertOverwritesInPlace(t *testing.T) {
	store, sqlDB := newTestStore(t)
	ctx := context.Background()

	first := json.RawMessage(`[{"name":"First"}]`)
	if err := store.Put(ctx, "studios", 2, 20, first, false, "2026-07-28T00:00:00Z"); err != nil {
		t.Fatalf("first Put: %v", err)
	}
	second := json.RawMessage(`[{"name":"Second"},{"name":"Third"}]`)
	if err := store.Put(ctx, "studios", 2, 20, second, true, "2026-07-29T00:00:00Z"); err != nil {
		t.Fatalf("second Put (upsert): %v", err)
	}

	if n := rowCount(t, sqlDB, "studios", 2, 20); n != 1 {
		t.Fatalf("expected exactly 1 row after an upsert (not a duplicate), got %d", n)
	}
	got, found, err := store.Get(ctx, "studios", 2, 20)
	if err != nil || !found {
		t.Fatalf("Get after upsert: found=%v err=%v", found, err)
	}
	if string(got.Items) != string(second) {
		t.Errorf("upsert should return the NEW payload, got %q want %q", got.Items, second)
	}
	if !got.HasMore || got.ComputedAt != "2026-07-29T00:00:00Z" {
		t.Errorf("upsert should overwrite hasMore/computedAt too, got HasMore=%v ComputedAt=%q", got.HasMore, got.ComputedAt)
	}
}

func TestStore_RowsIsolatedByTypeAndPage(t *testing.T) {
	store, _ := newTestStore(t)
	ctx := context.Background()

	// Same page number, different row_type — must not collide.
	if err := store.Put(ctx, "performers", 1, 20, json.RawMessage(`["perf-p1"]`), true, "t"); err != nil {
		t.Fatalf("Put performers p1: %v", err)
	}
	if err := store.Put(ctx, "studios", 1, 20, json.RawMessage(`["studio-p1"]`), false, "t"); err != nil {
		t.Fatalf("Put studios p1: %v", err)
	}
	// Same row_type, different page — must not collide.
	if err := store.Put(ctx, "performers", 2, 20, json.RawMessage(`["perf-p2"]`), false, "t"); err != nil {
		t.Fatalf("Put performers p2: %v", err)
	}

	cases := []struct {
		rowType     string
		page        int
		wantPayload string
		wantHasMore bool
	}{
		{"performers", 1, `["perf-p1"]`, true},
		{"studios", 1, `["studio-p1"]`, false},
		{"performers", 2, `["perf-p2"]`, false},
	}
	for _, c := range cases {
		got, found, err := store.Get(ctx, c.rowType, c.page, 20)
		if err != nil || !found {
			t.Fatalf("Get(%s,%d): found=%v err=%v", c.rowType, c.page, found, err)
		}
		if string(got.Items) != c.wantPayload {
			t.Errorf("Get(%s,%d) payload = %q, want %q", c.rowType, c.page, got.Items, c.wantPayload)
		}
		if got.HasMore != c.wantHasMore {
			t.Errorf("Get(%s,%d) HasMore = %v, want %v", c.rowType, c.page, got.HasMore, c.wantHasMore)
		}
	}

	// A distinct per_page is a distinct key too (the row for perPage 10 does not exist).
	if _, found, _ := store.Get(ctx, "performers", 1, 10); found {
		t.Errorf("expected no row at perPage=10 (distinct key), got found")
	}
}
