package db

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite"
)

// TestMigration0060ProposalPHashSimilarityRoundTrips proves migration 0060
// applies Up, rolls back Down, and re-applies Up cleanly on `proposals` — same
// round-trip discipline as migration_grabs_hold_until_test.go. Pins to
// explicit versions rather than goose.Up/Down so the assertions stay about
// 0060 specifically, not whatever the highest migration happens to be later.
func TestMigration0060ProposalPHashSimilarityRoundTrips(t *testing.T) {
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

	// Up to exactly 0059: the column must not exist yet.
	if err := goose.UpTo(sqlDB, "migrations", 59); err != nil {
		t.Fatalf("goose UpTo 59: %v", err)
	}
	if columnExists(t, sqlDB, "proposals", "phash_similarity") {
		t.Fatal("proposals.phash_similarity should not exist before 0060")
	}

	// Seed a pre-existing proposal (the legacy-row case: a Rename/Purge/Adult
	// proposal that never had a similarity score) before the Up, so the
	// backfill default is proven against a populated table, not an empty one.
	if _, err := sqlDB.Exec(`
		INSERT INTO proposals (mode, workflow, status, source_name, source_path, root_folder_path, title)
		VALUES ('movies', 'rename', 'pending', 'Some Movie', '/media/Movies/Some Movie', '/media/Movies', 'Some Movie')
	`); err != nil {
		t.Fatalf("seeding a pre-migration proposal: %v", err)
	}

	// Up to 0060: the column appears.
	if err := goose.UpTo(sqlDB, "migrations", 60); err != nil {
		t.Fatalf("goose UpTo 60: %v", err)
	}
	if !columnExists(t, sqlDB, "proposals", "phash_similarity") {
		t.Fatal("after Up, proposals.phash_similarity should exist")
	}

	// The pre-existing row survives, backfilled to the 0.0 sentinel — the same
	// value apidto/dto.go's omitempty JSON tag already treats as "no score".
	var similarity float64
	if err := sqlDB.QueryRow(`SELECT phash_similarity FROM proposals WHERE title = 'Some Movie'`).Scan(&similarity); err != nil {
		t.Fatalf("reading the backfilled row: %v", err)
	}
	if similarity != 0 {
		t.Errorf("expected the backfilled row to read phash_similarity = 0, got %v", similarity)
	}

	// A row inserted with a real similarity value round-trips it exactly.
	if _, err := sqlDB.Exec(`
		INSERT INTO proposals (mode, workflow, status, source_name, source_path, root_folder_path, title, phash_similarity)
		VALUES ('movies', 'dedup', 'pending', 'Dup Movie', '/media/Movies/Dup Movie', '/media/Movies', 'Dup Movie', 0.87)
	`); err != nil {
		t.Fatalf("seeding a post-migration proposal with a similarity score: %v", err)
	}
	if err := sqlDB.QueryRow(`SELECT phash_similarity FROM proposals WHERE title = 'Dup Movie'`).Scan(&similarity); err != nil {
		t.Fatalf("reading the post-migration row: %v", err)
	}
	if similarity != 0.87 {
		t.Errorf("expected phash_similarity = 0.87, got %v", similarity)
	}

	// Down to 0059: the column drops.
	if err := goose.DownTo(sqlDB, "migrations", 59); err != nil {
		t.Fatalf("goose DownTo 59: %v", err)
	}
	if columnExists(t, sqlDB, "proposals", "phash_similarity") {
		t.Fatal("after Down, proposals.phash_similarity should be gone")
	}

	// Up again: re-applies cleanly, with no residue from the rollback.
	if err := goose.UpTo(sqlDB, "migrations", 60); err != nil {
		t.Fatalf("goose UpTo 60 (re-apply): %v", err)
	}
	if !columnExists(t, sqlDB, "proposals", "phash_similarity") {
		t.Fatal("after re-Up, proposals.phash_similarity should exist again")
	}
}
