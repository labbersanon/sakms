package db

import "testing"

// Claude 2026-08-04: SQLite migration test retired for Postgres-only baseline.
// Decision: retire: final titles seeded in baseline
// Archive: internal/db/migrations_sqlite_archive/
// Review if: baseline regenerates and this assertion still belongs in app code.

func TestRetired_rename_adult_newest_rows(t *testing.T) {
	t.Skip("retired: retire: final titles seeded in baseline")
}
