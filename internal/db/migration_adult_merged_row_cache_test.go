package db

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite"
)

// tableExists reports whether a table named tbl exists, via sqlite_master.
func tableExists(t *testing.T, sqlDB *sql.DB, tbl string) bool {
	t.Helper()
	var n int
	if err := sqlDB.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, tbl,
	).Scan(&n); err != nil {
		t.Fatalf("querying sqlite_master for %s: %v", tbl, err)
	}
	return n > 0
}

// TestMigration0045_AdultMergedRowCache_UpDown proves migration 0045 applies
// cleanly up AND down against a fresh DB: Up creates adult_merged_row_cache, Down
// drops it, and a re-Up re-creates it (no residue that blocks re-migration).
// Pins both Up and Down to explicit versions so the test stays about 0045
// specifically, independent of how many migrations land after it (same rationale
// as migration_cpu_cap_test.go).
func TestMigration0045_AdultMergedRowCache_UpDown(t *testing.T) {
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

	// Up to exactly 0045: the cache table exists.
	if err := goose.UpTo(sqlDB, "migrations", 45); err != nil {
		t.Fatalf("goose UpTo 45: %v", err)
	}
	if !tableExists(t, sqlDB, "adult_merged_row_cache") {
		t.Fatal("after Up, adult_merged_row_cache should exist")
	}

	// Down to 0044: 0045 rolls back, dropping the table.
	if err := goose.DownTo(sqlDB, "migrations", 44); err != nil {
		t.Fatalf("goose DownTo 44: %v", err)
	}
	if tableExists(t, sqlDB, "adult_merged_row_cache") {
		t.Fatal("after Down, adult_merged_row_cache should be dropped")
	}

	// Up again: re-applies cleanly (no leftover residue).
	if err := goose.UpTo(sqlDB, "migrations", 45); err != nil {
		t.Fatalf("goose UpTo 45 (re-apply): %v", err)
	}
	if !tableExists(t, sqlDB, "adult_merged_row_cache") {
		t.Fatal("after re-Up, adult_merged_row_cache should exist again")
	}
}
