package db

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite"
)

// columnExists reports whether table has a column named col, via SQLite's
// table_info pragma.
func columnExists(t *testing.T, sqlDB *sql.DB, table, col string) bool {
	t.Helper()
	rows, err := sqlDB.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		t.Fatalf("PRAGMA table_info(%s): %v", table, err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			cid         int
			name, ctype string
			notnull, pk int
			dflt        sql.NullString
		)
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			t.Fatalf("scanning pragma row: %v", err)
		}
		if name == col {
			return true
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterating pragma rows: %v", err)
	}
	return false
}

// TestMigration0041_NodeCPUCap_UpDown proves migration 0041 applies cleanly up
// AND down against a fresh DB: Up adds node_max_jobs.cpu_cap_percent, Down drops
// it, and a re-Up re-adds it (no residue that blocks re-migration).
func TestMigration0041_NodeCPUCap_UpDown(t *testing.T) {
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

	// Up: migrate fully (through 0041).
	if err := goose.Up(sqlDB, "migrations"); err != nil {
		t.Fatalf("goose Up: %v", err)
	}
	if !columnExists(t, sqlDB, "node_max_jobs", "cpu_cap_percent") {
		t.Fatal("after Up, node_max_jobs.cpu_cap_percent should exist")
	}

	// Down one step: 0041 rolls back, dropping the column.
	if err := goose.Down(sqlDB, "migrations"); err != nil {
		t.Fatalf("goose Down: %v", err)
	}
	if columnExists(t, sqlDB, "node_max_jobs", "cpu_cap_percent") {
		t.Fatal("after Down, node_max_jobs.cpu_cap_percent should be dropped")
	}

	// Up again: re-applies cleanly.
	if err := goose.Up(sqlDB, "migrations"); err != nil {
		t.Fatalf("goose Up (re-apply): %v", err)
	}
	if !columnExists(t, sqlDB, "node_max_jobs", "cpu_cap_percent") {
		t.Fatal("after re-Up, node_max_jobs.cpu_cap_percent should exist again")
	}
}
