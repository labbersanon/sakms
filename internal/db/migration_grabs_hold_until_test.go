package db

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite"
)

// TestMigration0057GrabsHoldUntilRoundTrips proves migration 0057 applies Up,
// rolls back Down, and re-applies Up cleanly on `grabs` — the most
// safety-critical table in the app.
//
// Two properties beyond the bare round-trip, both silent-failure-shaped:
//
//   - An EXISTING grabs row survives the Up with hold_until = ''. The empty
//     string is the sentinel every query added by this feature keys on:
//     DueForRelease requires hold_until != '' and FindHeldRequest uses the same
//     predicate as its origin marker, so a pre-existing row that came out of the
//     migration with anything other than '' (a NULL, most plausibly, had the
//     column been declared nullable) would be misread as a Calendar pre-release
//     request it never was. A backfilled table is exactly where that would show
//     up, which is why the row is seeded BEFORE the Up rather than after.
//
//   - Down actually works, WHICH IS NO LONGER FREE. SQLite's ALTER TABLE ...
//     DROP COLUMN refuses a column that is indexed, generated, part of the primary
//     key, or referenced by a constraint/view/trigger — and that list includes a
//     column named in a PARTIAL INDEX'S WHERE CLAUSE.
//
//     CORRECTED — the superseded claim, quoted so it is findable: "hold_until is
//     a plain unindexed column so none of those apply … a future edit that
//     indexes hold_until would break Down. This test is what should catch that."
//     That future edit arrived in the same migration: 0057 now also creates
//     idx_grabs_held_request, a UNIQUE index ON grabs(mode, tmdb_id) WHERE
//     hold_until != '', so hold_until IS referenced by an index and the bare
//     DROP COLUMN this test used to prove safe would now fail outright. The Down
//     drops the index FIRST, and that ordering is exactly what the DownTo step
//     below verifies — the prediction held, the fix is the ordering, and the test
//     still earns its keep.
//
//     (0033_genres_cast.sql and 0054_grabs_retry.sql still rely on the plain
//     unindexed-column property. `grabs` also carries idx_grabs_mode_status from
//     0009_grabs.sql, so "this table has no indexes" was never the reason any of
//     this was safe.)
//
// Pins both directions to explicit versions rather than using goose.Up/Down,
// same rationale as migration_library_size_tier_test.go: the assertions are
// about 0057 specifically, not about whatever the highest migration happens to
// be later.
func TestMigration0057GrabsHoldUntilRoundTrips(t *testing.T) {
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

	// Up to exactly 0056: the column must not exist yet.
	if err := goose.UpTo(sqlDB, "migrations", 56); err != nil {
		t.Fatalf("goose UpTo 56: %v", err)
	}
	if columnExists(t, sqlDB, "grabs", "hold_until") {
		t.Fatal("grabs.hold_until should not exist before 0057")
	}

	// Seed a pre-existing grab, so the Up runs against a populated table rather
	// than an empty one — the case an upgrading install actually hits.
	if _, err := sqlDB.Exec(`
		INSERT INTO grabs (mode, title, tmdb_id, indexer, protocol, download_client, root_folder_path)
		VALUES ('movies', 'Some Movie', 42, 'SomeIndexer', 'torrent', 'qbittorrent', '/movies')
	`); err != nil {
		t.Fatalf("seeding a pre-migration grab: %v", err)
	}

	// Up to 0057: the column appears.
	if err := goose.UpTo(sqlDB, "migrations", 57); err != nil {
		t.Fatalf("goose UpTo 57: %v", err)
	}
	if !columnExists(t, sqlDB, "grabs", "hold_until") {
		t.Fatal("after Up, grabs.hold_until should exist")
	}
	if !indexExists(t, sqlDB, "idx_grabs_held_request") {
		t.Fatal("after Up, the partial unique index idx_grabs_held_request should exist — it is what stops two concurrent clicks minting two held rows for one film")
	}

	// The pre-existing row survives, unheld.
	var (
		title     string
		holdUntil string
	)
	if err := sqlDB.QueryRow(`SELECT title, hold_until FROM grabs WHERE tmdb_id = 42`).Scan(&title, &holdUntil); err != nil {
		t.Fatalf("reading the backfilled row: %v", err)
	}
	if title != "Some Movie" {
		t.Errorf("the pre-existing row should survive the migration, got title %q", title)
	}
	if holdUntil != "" {
		t.Errorf("a backfilled row must be unheld, got hold_until %q — anything but the empty string reads as a Calendar pre-release request it never was", holdUntil)
	}

	// A row inserted WITHOUT the column lands on the same sentinel, which is what
	// keeps every non-Calendar writer (every existing grab path) unheld for free.
	if _, err := sqlDB.Exec(`
		INSERT INTO grabs (mode, title, tmdb_id, indexer, protocol, download_client, root_folder_path)
		VALUES ('series', 'Some Show', 7, 'I', 'usenet', 'nzbget', '/tv')
	`); err != nil {
		t.Fatalf("seeding a post-migration grab: %v", err)
	}
	if err := sqlDB.QueryRow(`SELECT hold_until FROM grabs WHERE tmdb_id = 7`).Scan(&holdUntil); err != nil {
		t.Fatalf("reading the post-migration row: %v", err)
	}
	if holdUntil != "" {
		t.Errorf("hold_until default = %q, want the empty string", holdUntil)
	}

	// The index is PARTIAL and it must stay partial. An ordinary (unheld) grab
	// legitimately repeats a (mode, tmdb_id) pair — a re-grab, or a retry that
	// minted a fresh row — so a TOTAL unique index here would break normal grab
	// paths. A second unheld row for a pair that already exists proves the WHERE
	// clause is doing its job (tmdb_id 42 was seeded pre-migration above).
	if _, err := sqlDB.Exec(`
		INSERT INTO grabs (mode, title, tmdb_id, indexer, protocol, download_client, root_folder_path)
		VALUES ('movies', 'Some Movie', 42, 'SomeIndexer', 'torrent', 'qbittorrent', '/movies')
	`); err != nil {
		t.Fatalf("an UNHELD duplicate (mode, tmdb_id) must still be allowed: %v", err)
	}

	// A second HELD row for one pair is refused. This is the whole point of it.
	if _, err := sqlDB.Exec(`
		INSERT INTO grabs (mode, title, tmdb_id, indexer, protocol, download_client, root_folder_path, status, hold_until)
		VALUES ('movies', 'Held Movie', 555, 'I', 'torrent', 'qbittorrent', '/movies', 'pending_retry', '2030-01-01T00:00:00.000Z')
	`); err != nil {
		t.Fatalf("seeding the first held row: %v", err)
	}
	if _, err := sqlDB.Exec(`
		INSERT INTO grabs (mode, title, tmdb_id, indexer, protocol, download_client, root_folder_path, status, hold_until)
		VALUES ('movies', 'Held Movie', 555, 'I', 'torrent', 'qbittorrent', '/movies', 'pending_retry', '2030-06-01T00:00:00.000Z')
	`); err == nil {
		t.Fatal("a SECOND held row for the same (mode, tmdb_id) must be refused by idx_grabs_held_request")
	}
	// The same tmdb id held in a DIFFERENT mode is a different request, and is
	// allowed — the index is keyed on (mode, tmdb_id), not on tmdb_id alone.
	if _, err := sqlDB.Exec(`
		INSERT INTO grabs (mode, title, tmdb_id, indexer, protocol, download_client, root_folder_path, status, hold_until)
		VALUES ('series', 'Held Movie', 555, 'I', 'torrent', 'qbittorrent', '/tv', 'pending_retry', '2030-01-01T00:00:00.000Z')
	`); err != nil {
		t.Fatalf("a held row for the same tmdb id in another mode must be allowed: %v", err)
	}

	// Down to 0056: the index drops, THEN the column. Dropping the column while
	// the partial index still names it in its WHERE clause is what SQLite refuses,
	// so this step is now proving the Down's ORDERING, not just that DROP COLUMN
	// is permitted — see this test's doc.
	if err := goose.DownTo(sqlDB, "migrations", 56); err != nil {
		t.Fatalf("goose DownTo 56: %v", err)
	}
	if columnExists(t, sqlDB, "grabs", "hold_until") {
		t.Fatal("after Down, grabs.hold_until should be gone")
	}
	if indexExists(t, sqlDB, "idx_grabs_held_request") {
		t.Fatal("after Down, idx_grabs_held_request should be gone — a surviving index would block the re-Up below")
	}

	// Up again: re-applies cleanly, with no residue from the rollback.
	if err := goose.UpTo(sqlDB, "migrations", 57); err != nil {
		t.Fatalf("goose UpTo 57 (re-apply): %v", err)
	}
	if !columnExists(t, sqlDB, "grabs", "hold_until") {
		t.Fatal("after re-Up, grabs.hold_until should exist again")
	}
	if !indexExists(t, sqlDB, "idx_grabs_held_request") {
		t.Fatal("after re-Up, idx_grabs_held_request should exist again")
	}
}

// indexExists reports whether a named index is present, the index-level sibling
// of columnExists. sqlite_master is the only place a partial index's existence
// can be checked by name — PRAGMA index_list is table-scoped and would not
// distinguish a rollback that dropped the index from one that dropped the table.
func indexExists(t *testing.T, sqlDB *sql.DB, name string) bool {
	t.Helper()
	var count int
	if err := sqlDB.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = ?`, name,
	).Scan(&count); err != nil {
		t.Fatalf("checking for index %q: %v", name, err)
	}
	return count > 0
}
