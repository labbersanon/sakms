package db

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite"
)

// TestMigration0055RoundTrips proves migration 0055 applies Up, rolls back
// Down, and re-applies Up cleanly, across all three tracked tables.
//
// The reason this needs asserting in code rather than by analogy: SQLite's
// ALTER TABLE ... DROP COLUMN has real restrictions (a column that is
// indexed, generated, part of the primary key, or referenced by a
// constraint/view/trigger cannot be dropped), so a Down that looks obviously
// fine can fail at runtime. None of those restrictions apply to these six
// plain, unindexed columns — 0033_genres_cast.sql already relies on the same
// property — but nothing in the repo actually exercised it until now. A
// future edit that indexes quality_tier (tempting, and explicitly rejected in
// the plan: five distinct values is far too low-cardinality for the planner
// to use on a full-table GROUP BY) would break Down, and this test is what
// should catch it.
//
// Pins both directions to explicit versions rather than using goose.Up/Down,
// same rationale as migration_adult_newest_gender_test.go: the assertions are
// about 0055 specifically, not about whatever the highest migration happens
// to be later.
func TestMigration0055RoundTrips(t *testing.T) {
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

	tables := []string{"library_items", "library_episodes", "library_scenes"}
	cols := []string{"size", "quality_tier"}

	assertColumns := func(stage string, want bool) {
		t.Helper()
		for _, table := range tables {
			for _, col := range cols {
				if got := columnExists(t, sqlDB, table, col); got != want {
					t.Fatalf("%s: %s.%s present = %v, want %v", stage, table, col, got, want)
				}
			}
		}
	}

	// Up to exactly 0054: none of the six columns exist yet.
	if err := goose.UpTo(sqlDB, "migrations", 54); err != nil {
		t.Fatalf("goose UpTo 54: %v", err)
	}
	assertColumns("before 0055", false)

	// Up to 0055: all six appear.
	if err := goose.UpTo(sqlDB, "migrations", 55); err != nil {
		t.Fatalf("goose UpTo 55: %v", err)
	}
	assertColumns("after Up", true)

	// The defaults are the sentinels the backfill and the aggregation both
	// depend on: size 0 means "not captured", quality_tier "" means "never
	// processed". A row inserted without them must land on exactly those.
	if _, err := sqlDB.Exec(`
		INSERT INTO library_items (mode, tmdb_id, title, file_path, root_folder_path)
		VALUES ('movies', 1, 'Some Movie', '/movies/x.mkv', '/movies')
	`); err != nil {
		t.Fatalf("seeding library_items row: %v", err)
	}
	var (
		size int64
		tier string
	)
	if err := sqlDB.QueryRow(`SELECT size, quality_tier FROM library_items WHERE tmdb_id = 1`).Scan(&size, &tier); err != nil {
		t.Fatalf("reading seeded row: %v", err)
	}
	if size != 0 || tier != "" {
		t.Fatalf("defaults = (size %d, quality_tier %q), want (0, empty string)", size, tier)
	}

	// Down to 0054: all six drop. This is the step SQLite could plausibly
	// refuse and the reason this test exists.
	if err := goose.DownTo(sqlDB, "migrations", 54); err != nil {
		t.Fatalf("goose DownTo 54: %v", err)
	}
	assertColumns("after Down", false)

	// Up again: re-applies cleanly, with no residue from the rollback.
	if err := goose.UpTo(sqlDB, "migrations", 55); err != nil {
		t.Fatalf("goose UpTo 55 (re-apply): %v", err)
	}
	assertColumns("after re-Up", true)
}
