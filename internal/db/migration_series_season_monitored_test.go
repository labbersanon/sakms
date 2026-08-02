package db

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite"
)

// TestMigration0056_SeriesSeasonMonitored_UpDown proves migration 0056 applies
// Up, rolls back Down, and re-applies Up cleanly.
//
// It also pins the two schema facts the feature's semantics rest on, because
// both are silent-failure-shaped if a later edit changes them:
//   - monitored DEFAULTs to 0, so a row inserted without it is unmonitored —
//     the same answer an ABSENT row gives (§1: absent means unmonitored, there
//     is no tri-state).
//   - PRIMARY KEY (series_id, season_number) is what makes SetSeasonMonitored
//     an upsert rather than a duplicate-row writer.
//
// Pins both directions to explicit versions rather than using goose.Up/Down,
// same rationale as migration_library_size_tier_test.go: the assertions are
// about 0056 specifically, not about whatever the highest migration happens to
// be later.
func TestMigration0056_SeriesSeasonMonitored_UpDown(t *testing.T) {
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

	// Up to exactly 0055: the table must not exist yet.
	if err := goose.UpTo(sqlDB, "migrations", 55); err != nil {
		t.Fatalf("goose UpTo 55: %v", err)
	}
	if tableExists(t, sqlDB, "library_season_monitored") {
		t.Fatal("library_season_monitored should not exist before 0056")
	}

	// Up to 0056: the table appears.
	if err := goose.UpTo(sqlDB, "migrations", 56); err != nil {
		t.Fatalf("goose UpTo 56: %v", err)
	}
	if !tableExists(t, sqlDB, "library_season_monitored") {
		t.Fatal("after Up, library_season_monitored should exist")
	}

	// A row written without an explicit monitored value lands unmonitored, and
	// both timestamps populate from the schema defaults.
	if _, err := sqlDB.Exec(`INSERT INTO library_season_monitored (series_id, season_number) VALUES (7, 3)`); err != nil {
		t.Fatalf("seeding row: %v", err)
	}
	var (
		monitored            int
		createdAt, updatedAt string
	)
	if err := sqlDB.QueryRow(`
		SELECT monitored, created_at, updated_at FROM library_season_monitored WHERE series_id = 7 AND season_number = 3
	`).Scan(&monitored, &createdAt, &updatedAt); err != nil {
		t.Fatalf("reading seeded row: %v", err)
	}
	if monitored != 0 {
		t.Errorf("monitored default = %d, want 0 (a row written without it is unmonitored)", monitored)
	}
	if createdAt == "" || updatedAt == "" {
		t.Errorf("timestamps should default-populate, got created_at %q updated_at %q", createdAt, updatedAt)
	}

	// The composite primary key is what SetSeasonMonitored upserts against — a
	// second bare INSERT for the same pair must be rejected, not duplicated.
	if _, err := sqlDB.Exec(`INSERT INTO library_season_monitored (series_id, season_number) VALUES (7, 3)`); err == nil {
		t.Error("expected PRIMARY KEY (series_id, season_number) to reject a duplicate pair")
	}

	// Down to 0055: the table drops.
	if err := goose.DownTo(sqlDB, "migrations", 55); err != nil {
		t.Fatalf("goose DownTo 55: %v", err)
	}
	if tableExists(t, sqlDB, "library_season_monitored") {
		t.Fatal("after Down, library_season_monitored should be gone")
	}

	// Up again: re-applies cleanly, with no residue from the rollback.
	if err := goose.UpTo(sqlDB, "migrations", 56); err != nil {
		t.Fatalf("goose UpTo 56 (re-apply): %v", err)
	}
	if !tableExists(t, sqlDB, "library_season_monitored") {
		t.Fatal("after re-Up, library_season_monitored should exist again")
	}
}
