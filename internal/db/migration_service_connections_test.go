package db

import "testing"

// Claude 2026-08-04: SQLite migration test retired for Postgres-only baseline.
// Decision: fold: connection seed/backfill was one-shot SQLite; live ETL carries data
// Archive: internal/db/migrations_sqlite_archive/
// Review if: baseline regenerates and this assertion still belongs in app code.

func TestRetired_service_connections(t *testing.T) {
	t.Skip("retired: fold: connection seed/backfill was one-shot SQLite; live ETL carries data")
}
