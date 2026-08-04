package db

import "testing"

// Claude 2026-08-04: SQLite migration test retired for Postgres-only baseline.
// Decision: retire: schema/settings in baseline
// Archive: internal/db/migrations_sqlite_archive/
// Review if: baseline regenerates and this assertion still belongs in app code.

func TestRetired_cpu_cap(t *testing.T) {
	t.Skip("retired: retire: schema/settings in baseline")
}
