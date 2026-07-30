package db

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite"
)

// TestMigration0047_AdultNewestGender_UpDown proves migration 0047 applies
// cleanly up AND down against a DB carrying real pre-existing
// adult_newest_releases data: Up adds a nullable gender column with no
// default, so a row seeded BEFORE the migration reads back NULL (the
// backfill work queue signal), while a row inserted AFTER the migration can
// write and read back a concrete gender value. Down drops the column, and a
// re-Up re-adds it cleanly (no residue that blocks re-migration). Pins both
// Up and Down to explicit versions, same rationale as
// migration_cpu_cap_test.go and migration_adult_merged_row_cache_test.go.
func TestMigration0047_AdultNewestGender_UpDown(t *testing.T) {
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

	// Up to exactly 0046 (before the gender column exists): seed a real
	// pre-existing row the way the browse pass would have written one prior
	// to this migration ever landing.
	if err := goose.UpTo(sqlDB, "migrations", 46); err != nil {
		t.Fatalf("goose UpTo 46: %v", err)
	}
	res, err := sqlDB.Exec(
		`INSERT INTO adult_newest_releases (row_type, entity_id, entity_source, entity_title)
		 VALUES ('performer', 'preexisting-id', 'stashdb', 'Jane Doe')`,
	)
	if err != nil {
		t.Fatalf("seeding pre-existing adult_newest_releases row: %v", err)
	}
	preexistingID, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("getting last insert id: %v", err)
	}

	// Up to 0047: gender column appears, nullable, no default.
	if err := goose.UpTo(sqlDB, "migrations", 47); err != nil {
		t.Fatalf("goose UpTo 47: %v", err)
	}
	if !columnExists(t, sqlDB, "adult_newest_releases", "gender") {
		t.Fatal("after Up, adult_newest_releases.gender should exist")
	}

	// The pre-existing row must read back gender as SQL NULL -- it predates
	// the column and was never resolved by anything.
	var preexistingGender sql.NullString
	if err := sqlDB.QueryRow(
		`SELECT gender FROM adult_newest_releases WHERE id = ?`, preexistingID,
	).Scan(&preexistingGender); err != nil {
		t.Fatalf("reading pre-existing row's gender: %v", err)
	}
	if preexistingGender.Valid {
		t.Fatalf("pre-existing row's gender should be NULL, got %q", preexistingGender.String)
	}

	// A row inserted AFTER the migration can write and read back a concrete
	// gender value.
	res, err = sqlDB.Exec(
		`INSERT INTO adult_newest_releases (row_type, entity_id, entity_source, entity_title, gender)
		 VALUES ('performer', 'new-id', 'stashdb', 'John Doe', 'male')`,
	)
	if err != nil {
		t.Fatalf("inserting new row with gender: %v", err)
	}
	newID, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("getting last insert id: %v", err)
	}
	var newGender sql.NullString
	if err := sqlDB.QueryRow(
		`SELECT gender FROM adult_newest_releases WHERE id = ?`, newID,
	).Scan(&newGender); err != nil {
		t.Fatalf("reading new row's gender: %v", err)
	}
	if !newGender.Valid || newGender.String != "male" {
		t.Fatalf("new row's gender = (%v, %q), want (true, \"male\")", newGender.Valid, newGender.String)
	}

	// Down to 0046: the gender column rolls back.
	if err := goose.DownTo(sqlDB, "migrations", 46); err != nil {
		t.Fatalf("goose DownTo 46: %v", err)
	}
	if columnExists(t, sqlDB, "adult_newest_releases", "gender") {
		t.Fatal("after Down, adult_newest_releases.gender should be dropped")
	}

	// Up again: re-applies cleanly (no leftover residue).
	if err := goose.UpTo(sqlDB, "migrations", 47); err != nil {
		t.Fatalf("goose UpTo 47 (re-apply): %v", err)
	}
	if !columnExists(t, sqlDB, "adult_newest_releases", "gender") {
		t.Fatal("after re-Up, adult_newest_releases.gender should exist again")
	}
}
