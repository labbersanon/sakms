package db

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite"
)

// TestMigration0049_DropAdultMergedCaches_UpDown proves migration 0049 drops
// adult_merged_row_cache and adult_image_cache even when they carry real,
// non-empty data; that Down recreates empty (schema-only) tables with no
// attempt to restore the dropped data; and that a re-applied Up is a safe
// no-op thanks to DROP TABLE IF EXISTS.
func TestMigration0049_DropAdultMergedCaches_UpDown(t *testing.T) {
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

	// Up to exactly 0048 (both cache tables exist, pre-drop): seed real rows
	// into both, matching their actual schemas from 0045/0046.
	if err := goose.UpTo(sqlDB, "migrations", 48); err != nil {
		t.Fatalf("goose UpTo 48: %v", err)
	}
	if _, err := sqlDB.Exec(
		`INSERT INTO adult_merged_row_cache (row_type, page, per_page, payload, has_more, computed_at)
		 VALUES ('performers', 1, 20, '[{"id":"abc"}]', 1, '2026-07-20T00:00:00Z')`,
	); err != nil {
		t.Fatalf("seeding adult_merged_row_cache: %v", err)
	}
	if _, err := sqlDB.Exec(
		`INSERT INTO adult_image_cache (url_sha256, source_url, content_type, byte_size, row_type, fetched_at)
		 VALUES ('deadbeef', 'https://example.com/img.jpg', 'image/jpeg', 12345, 'performers', '2026-07-20T00:00:00Z')`,
	); err != nil {
		t.Fatalf("seeding adult_image_cache: %v", err)
	}

	// Sanity: the seeded rows are really there before the drop.
	if n := rowCount(t, sqlDB, "adult_merged_row_cache"); n != 1 {
		t.Fatalf("adult_merged_row_cache row count before drop = %d, want 1", n)
	}
	if n := rowCount(t, sqlDB, "adult_image_cache"); n != 1 {
		t.Fatalf("adult_image_cache row count before drop = %d, want 1", n)
	}

	// Up to 0049: both tables are gone, seeded rows and all.
	if err := goose.UpTo(sqlDB, "migrations", 49); err != nil {
		t.Fatalf("goose UpTo 49: %v", err)
	}
	if tableExists(t, sqlDB, "adult_merged_row_cache") {
		t.Fatal("after Up, adult_merged_row_cache should no longer exist")
	}
	if tableExists(t, sqlDB, "adult_image_cache") {
		t.Fatal("after Up, adult_image_cache should no longer exist")
	}

	// Down to 0048: empty (schema-only) tables are recreated -- no attempt
	// to restore the seeded data.
	if err := goose.DownTo(sqlDB, "migrations", 48); err != nil {
		t.Fatalf("goose DownTo 48: %v", err)
	}
	if !tableExists(t, sqlDB, "adult_merged_row_cache") {
		t.Fatal("after Down, adult_merged_row_cache should be recreated")
	}
	if !tableExists(t, sqlDB, "adult_image_cache") {
		t.Fatal("after Down, adult_image_cache should be recreated")
	}
	if n := rowCount(t, sqlDB, "adult_merged_row_cache"); n != 0 {
		t.Fatalf("adult_merged_row_cache row count after Down = %d, want 0 (no data restore)", n)
	}
	if n := rowCount(t, sqlDB, "adult_image_cache"); n != 0 {
		t.Fatalf("adult_image_cache row count after Down = %d, want 0 (no data restore)", n)
	}

	// Re-apply Up: DROP TABLE IF EXISTS makes this a safe no-op even though
	// the recreated tables are once again empty (not missing).
	if err := goose.UpTo(sqlDB, "migrations", 49); err != nil {
		t.Fatalf("goose UpTo 49 (re-apply) should be a safe no-op: %v", err)
	}
	if tableExists(t, sqlDB, "adult_merged_row_cache") {
		t.Fatal("after re-applied Up, adult_merged_row_cache should be dropped again")
	}
	if tableExists(t, sqlDB, "adult_image_cache") {
		t.Fatal("after re-applied Up, adult_image_cache should be dropped again")
	}
}

// rowCount returns the row count of tbl.
func rowCount(t *testing.T, sqlDB *sql.DB, tbl string) int {
	t.Helper()
	var n int
	if err := sqlDB.QueryRow(`SELECT COUNT(*) FROM ` + tbl).Scan(&n); err != nil {
		t.Fatalf("counting rows in %s: %v", tbl, err)
	}
	return n
}
