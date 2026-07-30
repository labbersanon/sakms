package db

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite"
)

// TestMigration0050_AdultNewestSceneMatches_UpDown proves migration 0050
// applies cleanly up AND down: Up creates the download_url-keyed release-level
// cache table and a seeded row round-trips (including the JSON-encoded
// genres/performers arrays and the box/duration fields the drill-down's page>1
// path reconstructs an enriched item from); Down drops it, and a re-Up re-adds
// it cleanly (no residue that blocks re-migration). Pins both Up and Down to
// explicit versions, same rationale as migration_adult_newest_gender_test.go.
func TestMigration0050_AdultNewestSceneMatches_UpDown(t *testing.T) {
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

	// Up to exactly 0049 (before the scene-match cache exists): the table must
	// not exist yet.
	if err := goose.UpTo(sqlDB, "migrations", 49); err != nil {
		t.Fatalf("goose UpTo 49: %v", err)
	}
	if tableExists(t, sqlDB, "adult_newest_scene_matches") {
		t.Fatal("adult_newest_scene_matches should not exist before 0050")
	}

	// Up to 0050: the cache table appears.
	if err := goose.UpTo(sqlDB, "migrations", 50); err != nil {
		t.Fatalf("goose UpTo 50: %v", err)
	}
	if !tableExists(t, sqlDB, "adult_newest_scene_matches") {
		t.Fatal("after Up, adult_newest_scene_matches should exist")
	}

	// Seed a row and confirm every column round-trips, including the JSON arrays.
	if _, err := sqlDB.Exec(`
		INSERT INTO adult_newest_scene_matches
			(download_url, title, studio, image, date, genres, performers, duration_seconds, box)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"magnet:?xt=urn:btih:ABC", "Clean Scene Title", "Brazzers", "http://cdn/x.jpg",
		"2023-05-01", `["Anal","Blonde"]`, `["Riley Reid","Jane Doe"]`, 1500, "tpdb",
	); err != nil {
		t.Fatalf("seeding scene-match row: %v", err)
	}

	var (
		title, studio, image, date, genres, performers, box string
		duration                                            int
	)
	if err := sqlDB.QueryRow(`
		SELECT title, studio, image, date, genres, performers, duration_seconds, box
		FROM adult_newest_scene_matches WHERE download_url = ?`, "magnet:?xt=urn:btih:ABC",
	).Scan(&title, &studio, &image, &date, &genres, &performers, &duration, &box); err != nil {
		t.Fatalf("reading seeded row: %v", err)
	}
	if title != "Clean Scene Title" || studio != "Brazzers" || image != "http://cdn/x.jpg" ||
		date != "2023-05-01" || genres != `["Anal","Blonde"]` || performers != `["Riley Reid","Jane Doe"]` ||
		duration != 1500 || box != "tpdb" {
		t.Fatalf("row did not round-trip: title=%q studio=%q image=%q date=%q genres=%q performers=%q duration=%d box=%q",
			title, studio, image, date, genres, performers, duration, box)
	}

	// A default matched_at is written even though the INSERT omitted it.
	var matchedAt string
	if err := sqlDB.QueryRow(`SELECT matched_at FROM adult_newest_scene_matches WHERE download_url = ?`,
		"magnet:?xt=urn:btih:ABC").Scan(&matchedAt); err != nil {
		t.Fatalf("reading matched_at: %v", err)
	}
	if matchedAt == "" {
		t.Fatal("expected matched_at to default to a non-empty timestamp")
	}

	// Down to 0049: the table rolls back.
	if err := goose.DownTo(sqlDB, "migrations", 49); err != nil {
		t.Fatalf("goose DownTo 49: %v", err)
	}
	if tableExists(t, sqlDB, "adult_newest_scene_matches") {
		t.Fatal("after Down, adult_newest_scene_matches should be dropped")
	}

	// Up again: re-applies cleanly (no leftover residue).
	if err := goose.UpTo(sqlDB, "migrations", 50); err != nil {
		t.Fatalf("goose UpTo 50 (re-apply): %v", err)
	}
	if !tableExists(t, sqlDB, "adult_newest_scene_matches") {
		t.Fatal("after re-Up, adult_newest_scene_matches should exist again")
	}
}
