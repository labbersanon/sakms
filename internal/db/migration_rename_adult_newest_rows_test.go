package db

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite"
)

// rowTitle reads back the title of a single adult_newest_rows row by id.
func rowTitle(t *testing.T, sqlDB *sql.DB, id int64) string {
	t.Helper()
	var title string
	if err := sqlDB.QueryRow(
		`SELECT title FROM adult_newest_rows WHERE id = ?`, id,
	).Scan(&title); err != nil {
		t.Fatalf("reading title for row %d: %v", id, err)
	}
	return title
}

// TestMigration0048_RenameAdultNewestRows_GuardBothDirections proves the
// guarded rename in migration 0048 only ever touches rows still at the exact
// seeded default title, in BOTH directions (Up and Down) -- an
// operator-customized title, whether customized before Up ran or re-customized
// after Up ran (i.e. before Down runs), is never clobbered.
func TestMigration0048_RenameAdultNewestRows_GuardBothDirections(t *testing.T) {
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

	// Up to exactly 0047 (before the rename): migration 0024 already seeded
	// the default 'New Performers'/'New Studios' rows. Capture their ids,
	// then seed two operator-customized rows of the same row_types to prove
	// the guard leaves already-customized titles alone.
	if err := goose.UpTo(sqlDB, "migrations", 47); err != nil {
		t.Fatalf("goose UpTo 47: %v", err)
	}

	var defaultPerformerID, defaultStudioID int64
	if err := sqlDB.QueryRow(
		`SELECT id FROM adult_newest_rows WHERE row_type = 'performer' AND title = 'New Performers'`,
	).Scan(&defaultPerformerID); err != nil {
		t.Fatalf("finding seeded default performer row: %v", err)
	}
	if err := sqlDB.QueryRow(
		`SELECT id FROM adult_newest_rows WHERE row_type = 'studio' AND title = 'New Studios'`,
	).Scan(&defaultStudioID); err != nil {
		t.Fatalf("finding seeded default studio row: %v", err)
	}

	customPerformerID := insertAdultNewestRow(t, sqlDB, "My Favorite Performers", "performer")
	customStudioID := insertAdultNewestRow(t, sqlDB, "My Custom Studio Row", "studio")

	// Up to 0048: the default-titled rows rename; the customized rows don't.
	if err := goose.UpTo(sqlDB, "migrations", 48); err != nil {
		t.Fatalf("goose UpTo 48: %v", err)
	}

	if got := rowTitle(t, sqlDB, defaultPerformerID); got != "Performers" {
		t.Fatalf("default-titled performer row: title = %q, want %q", got, "Performers")
	}
	if got := rowTitle(t, sqlDB, customPerformerID); got != "My Favorite Performers" {
		t.Fatalf("custom-titled performer row was clobbered: title = %q, want unchanged %q", got, "My Favorite Performers")
	}
	if got := rowTitle(t, sqlDB, defaultStudioID); got != "Studios" {
		t.Fatalf("default-titled studio row: title = %q, want %q", got, "Studios")
	}
	if got := rowTitle(t, sqlDB, customStudioID); got != "My Custom Studio Row" {
		t.Fatalf("custom-titled studio row was clobbered: title = %q, want unchanged %q", got, "My Custom Studio Row")
	}

	// Simulate an operator re-customizing the just-renamed performer row
	// AFTER Up ran (its title is no longer the new default 'Performers').
	// Down's guard must leave it alone, since its current title no longer
	// matches what Down looks for.
	if _, err := sqlDB.Exec(
		`UPDATE adult_newest_rows SET title = 'Top Performers' WHERE id = ?`, defaultPerformerID,
	); err != nil {
		t.Fatalf("simulating post-Up operator rename: %v", err)
	}

	// Down to 0047: the studio row (still at 'Studios') reverts; the
	// re-customized performer row ('Top Performers') is left untouched, and
	// the already-customized rows remain untouched throughout.
	if err := goose.DownTo(sqlDB, "migrations", 47); err != nil {
		t.Fatalf("goose DownTo 47: %v", err)
	}

	if got := rowTitle(t, sqlDB, defaultPerformerID); got != "Top Performers" {
		t.Fatalf("post-Up-re-customized performer row was clobbered by Down: title = %q, want unchanged %q", got, "Top Performers")
	}
	if got := rowTitle(t, sqlDB, defaultStudioID); got != "New Studios" {
		t.Fatalf("default-titled studio row did not revert on Down: title = %q, want %q", got, "New Studios")
	}
	if got := rowTitle(t, sqlDB, customPerformerID); got != "My Favorite Performers" {
		t.Fatalf("custom-titled performer row was clobbered by Down: title = %q, want unchanged %q", got, "My Favorite Performers")
	}
	if got := rowTitle(t, sqlDB, customStudioID); got != "My Custom Studio Row" {
		t.Fatalf("custom-titled studio row was clobbered by Down: title = %q, want unchanged %q", got, "My Custom Studio Row")
	}
}

// insertAdultNewestRow inserts a minimal adult_newest_rows row with the
// given title/row_type (relying on the table's other columns' defaults) and
// returns its id.
func insertAdultNewestRow(t *testing.T, sqlDB *sql.DB, title, rowType string) int64 {
	t.Helper()
	res, err := sqlDB.Exec(
		`INSERT INTO adult_newest_rows (title, row_type) VALUES (?, ?)`, title, rowType,
	)
	if err != nil {
		t.Fatalf("inserting adult_newest_rows row (title=%q, row_type=%q): %v", title, rowType, err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("getting last insert id: %v", err)
	}
	return id
}
