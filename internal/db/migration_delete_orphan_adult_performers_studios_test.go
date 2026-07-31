package db

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite"
)

// seedAdultReleaseRow inserts one adult_newest_releases row with the columns the
// 0051 orphan-cleanup test cares about (the rest rely on their schema defaults).
func seedAdultReleaseRow(t *testing.T, sqlDB *sql.DB, rowType, entityID, studio, performersJSON string) {
	t.Helper()
	if _, err := sqlDB.Exec(`
		INSERT INTO adult_newest_releases
			(row_type, entity_id, entity_source, entity_title, entity_studio, performers)
		VALUES (?, ?, 'tpdb', ?, ?, ?)`,
		rowType, entityID, entityID, studio, performersJSON,
	); err != nil {
		t.Fatalf("seeding %s row %q: %v", rowType, entityID, err)
	}
}

// adultReleaseExists reports whether a row of the given type+entity_id survives.
func adultReleaseExists(t *testing.T, sqlDB *sql.DB, rowType, entityID string) bool {
	t.Helper()
	var n int
	if err := sqlDB.QueryRow(
		`SELECT COUNT(*) FROM adult_newest_releases WHERE row_type = ? AND entity_id = ?`,
		rowType, entityID,
	).Scan(&n); err != nil {
		t.Fatalf("counting %s row %q: %v", rowType, entityID, err)
	}
	return n > 0
}

// TestMigration0051_DeletesOrphanPerformersStudios proves the one-time cleanup:
// a performer/studio row WITH a linked scene (exact json_each / entity_studio
// match) survives Up, one with NO linked scene is deleted, and the linkage is
// exact-not-substring. Scene rows are never touched.
func TestMigration0051_DeletesOrphanPerformersStudios(t *testing.T) {
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

	// Up to exactly 0050 (before the cleanup): seed the fixture.
	if err := goose.UpTo(sqlDB, "migrations", 50); err != nil {
		t.Fatalf("goose UpTo 50: %v", err)
	}

	// One scene that credits "Linked Performer" and studio "Linked Studio".
	seedAdultReleaseRow(t, sqlDB, "scene", "sc-1", "Linked Studio", `["Linked Performer"]`)
	// Linked performer/studio (must survive).
	seedAdultReleaseRow(t, sqlDB, "performer", "Linked Performer", "", "[]")
	seedAdultReleaseRow(t, sqlDB, "studio", "Linked Studio", "", "[]")
	// Orphan performer/studio (must be deleted).
	seedAdultReleaseRow(t, sqlDB, "performer", "Orphan Performer", "", "[]")
	seedAdultReleaseRow(t, sqlDB, "studio", "Orphan Studio", "", "[]")
	// Substring near-miss (must be deleted — linkage is exact, not substring):
	// "Linked" is a substring of the scene's "Linked Performer" credit.
	seedAdultReleaseRow(t, sqlDB, "performer", "Linked", "", "[]")

	// Up to 0051: the cleanup runs.
	if err := goose.UpTo(sqlDB, "migrations", 51); err != nil {
		t.Fatalf("goose UpTo 51: %v", err)
	}

	if !adultReleaseExists(t, sqlDB, "performer", "Linked Performer") {
		t.Error("linked performer was deleted but should have survived")
	}
	if !adultReleaseExists(t, sqlDB, "studio", "Linked Studio") {
		t.Error("linked studio was deleted but should have survived")
	}
	if adultReleaseExists(t, sqlDB, "performer", "Orphan Performer") {
		t.Error("orphan performer should have been deleted")
	}
	if adultReleaseExists(t, sqlDB, "studio", "Orphan Studio") {
		t.Error("orphan studio should have been deleted")
	}
	if adultReleaseExists(t, sqlDB, "performer", "Linked") {
		t.Error("substring near-miss performer 'Linked' should have been deleted (exact match only)")
	}
	// The scene row is never touched by the cleanup.
	if !adultReleaseExists(t, sqlDB, "scene", "sc-1") {
		t.Error("scene row must be untouched by the performer/studio cleanup")
	}

	// Down is a documented no-op (irreversible data delete) — it must apply
	// cleanly and restore nothing.
	if err := goose.DownTo(sqlDB, "migrations", 50); err != nil {
		t.Fatalf("goose DownTo 50 (no-op Down): %v", err)
	}
	if adultReleaseExists(t, sqlDB, "performer", "Orphan Performer") {
		t.Error("Down must not restore deleted orphan data")
	}
}
