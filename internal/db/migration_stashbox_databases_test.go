package db

import "testing"

// Claude 2026-08-04: SQLite migration test retired for Postgres-only baseline.
// Decision: fold: seed INSERTs moved into 0001_baseline; keep shape via Open/goose
// Archive: internal/db/migrations_sqlite_archive/
// Review if: baseline regenerates and this assertion still belongs in app code.

func TestRetired_stashbox_databases(t *testing.T) {
	t.Skip("retired: fold: seed INSERTs moved into 0001_baseline; keep shape via Open/goose")
}
