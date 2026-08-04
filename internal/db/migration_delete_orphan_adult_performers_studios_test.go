package db

import "testing"

// Claude 2026-08-04: SQLite migration test retired for Postgres-only baseline.
// Decision: retire: one-shot data cleanup baked into history
// Archive: internal/db/migrations_sqlite_archive/
// Review if: baseline regenerates and this assertion still belongs in app code.

func TestRetired_delete_orphan_adult_performers_studios(t *testing.T) {
	t.Skip("retired: retire: one-shot data cleanup baked into history")
}
