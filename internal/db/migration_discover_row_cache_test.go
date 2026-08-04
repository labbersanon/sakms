package db

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite"
)

// TestMigration0058_DiscoverRowCache_UpDown proves migration 0058 applies Up,
// rolls back Down, and re-applies Up cleanly.
//
// It also pins the schema facts internal/discoverrefresh's store contract rests
// on, because each is silent-failure-shaped if a later edit changes them:
//   - page_size, raw_pages and exhausted exist BY NAME. These three were added
//     by a later revision of the plan (§2.4) and are the three most likely to be
//     silently dropped during implementation — without page_size the read path
//     cannot slice the flat list, without raw_pages a fall-through re-fetches an
//     upstream page whose survivors are already cached, and without exhausted
//     "genuine end of data" is indistinguishable from "depth target met".
//   - their defaults are 20 / 0 / 0, which is what a row written without them
//     must land on.
//   - there is NO page column, deliberately (§2.2). One flat accumulated list
//     per key is what makes the cached window internally consistent; a page
//     column would reintroduce 0045_adult_merged_row_cache.sql's shape, which
//     this design explicitly rejects.
//   - PRIMARY KEY (source, cache_key) is what makes Store.Put an upsert rather
//     than a duplicate-row writer.
//
// Pins both directions to explicit versions rather than using goose.Up/Down,
// same rationale as migration_series_season_monitored_test.go: the assertions
// are about 0058 specifically, not about whatever the highest migration happens
// to be later.
func TestMigration0058_DiscoverRowCache_UpDown(t *testing.T) {
	sqlDB, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "sakms.db"))
	if err != nil {
		t.Fatalf("opening db: %v", err)
	}
	defer sqlDB.Close()
	sqlDB.SetMaxOpenConns(1)

	goose.SetBaseFS(migrations)
	if err := goose.SetDialect("sqlite3"); err != nil {
		t.Fatalf("setting dialect: %v", err)
	}

	// Up to exactly 0057: the table must not exist yet.
	if err := goose.UpTo(sqlDB, "migrations", 57); err != nil {
		t.Fatalf("goose UpTo 57: %v", err)
	}
	if tableExists(t, sqlDB, "discover_row_cache") {
		t.Fatal("discover_row_cache should not exist before 0058")
	}

	// Up to 0058: the table appears.
	if err := goose.UpTo(sqlDB, "migrations", 58); err != nil {
		t.Fatalf("goose UpTo 58: %v", err)
	}
	if !tableExists(t, sqlDB, "discover_row_cache") {
		t.Fatal("after Up, discover_row_cache should exist")
	}

	// The exact column set, asserted by name. page_size/raw_pages/exhausted are
	// called out in the doc comment above; the rest are listed so a dropped
	// column of any kind fails here rather than in the store's own tests.
	for _, col := range []string{
		"source", "cache_key", "payload", "item_count",
		"page_size", "raw_pages", "exhausted",
		"refreshed_at", "attempted_at", "last_error",
	} {
		if !columnExists(t, sqlDB, "discover_row_cache", col) {
			t.Errorf("column %q missing from discover_row_cache", col)
		}
	}

	// DELIBERATELY NO page column (§2.2) — one flat list per key, sliced on read.
	if columnExists(t, sqlDB, "discover_row_cache", "page") {
		t.Error("discover_row_cache must NOT have a page column (§2.2: one flat list per key)")
	}

	// A row written with only the primary key lands on the schema defaults, which
	// is what the store contract reads back for a never-accumulated row.
	if _, err := sqlDB.Exec(`INSERT INTO discover_row_cache (source, cache_key) VALUES ('tmdb', 'movies:trending')`); err != nil {
		t.Fatalf("seeding row: %v", err)
	}
	var (
		payload                           string
		itemCount, pageSize, rawPages     int
		exhausted                         int
		refreshedAt, attemptedAt, lastErr string
	)
	if err := sqlDB.QueryRow(`
		SELECT payload, item_count, page_size, raw_pages, exhausted, refreshed_at, attempted_at, last_error
		FROM discover_row_cache WHERE source = 'tmdb' AND cache_key = 'movies:trending'
	`).Scan(&payload, &itemCount, &pageSize, &rawPages, &exhausted, &refreshedAt, &attemptedAt, &lastErr); err != nil {
		t.Fatalf("reading seeded row: %v", err)
	}
	if payload != "[]" {
		t.Errorf("payload default = %q, want %q", payload, "[]")
	}
	if itemCount != 0 {
		t.Errorf("item_count default = %d, want 0", itemCount)
	}
	if pageSize != 20 {
		t.Errorf("page_size default = %d, want 20", pageSize)
	}
	if rawPages != 0 {
		t.Errorf("raw_pages default = %d, want 0", rawPages)
	}
	if exhausted != 0 {
		t.Errorf("exhausted default = %d, want 0", exhausted)
	}
	if refreshedAt != "" || attemptedAt != "" || lastErr != "" {
		t.Errorf("stale-on-failure triple should default empty, got refreshed_at %q attempted_at %q last_error %q",
			refreshedAt, attemptedAt, lastErr)
	}

	// The composite primary key is what Store.Put upserts against — a second bare
	// INSERT for the same pair must be rejected, not duplicated.
	if _, err := sqlDB.Exec(`INSERT INTO discover_row_cache (source, cache_key) VALUES ('tmdb', 'movies:trending')`); err == nil {
		t.Error("expected PRIMARY KEY (source, cache_key) to reject a duplicate pair")
	}
	// The same cache_key under a different source is a different row, which is
	// what lets one table back four unrelated upstreams.
	if _, err := sqlDB.Exec(`INSERT INTO discover_row_cache (source, cache_key) VALUES ('slider', 'movies:trending')`); err != nil {
		t.Errorf("a different source with the same cache_key should be a distinct row: %v", err)
	}

	// Down to 0057: the table drops.
	if err := goose.DownTo(sqlDB, "migrations", 57); err != nil {
		t.Fatalf("goose DownTo 57: %v", err)
	}
	if tableExists(t, sqlDB, "discover_row_cache") {
		t.Fatal("after Down, discover_row_cache should be gone")
	}

	// Up again: re-applies cleanly, with no residue from the rollback.
	if err := goose.UpTo(sqlDB, "migrations", 58); err != nil {
		t.Fatalf("goose UpTo 58 (re-apply): %v", err)
	}
	if !tableExists(t, sqlDB, "discover_row_cache") {
		t.Fatal("after re-Up, discover_row_cache should exist again")
	}
}
