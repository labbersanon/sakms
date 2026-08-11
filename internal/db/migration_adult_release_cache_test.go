package db_test

import (
	"context"
	"testing"

	"github.com/labbersanon/sakms/internal/dbtest"
)

// Claude 2026-08-11: existence assertion for migration 0009 tables and indexes.
// Reason: 0009 is a pure CREATE (no data migration), so asserting both tables and
// both indexes exist after dbtest.New(t) is sufficient — it proves the migration
// ran to completion in the template database. A full up/down round-trip is reserved
// for migrations with data-conversion logic that cannot be verified any other way
// (see migration_pruning_rule_tags_test.go for that pattern).
// Troubleshooting: skips when SAKMS_TEST_DATABASE_URL is unset (dbtest.New's gate).
// Review if: 0009 is superseded by a migration that alters these tables.

func TestMigration0009_AdultReleaseCache(t *testing.T) {
	sqlDB := dbtest.New(t)
	ctx := context.Background()

	for _, table := range []string{"adult_release_cache", "adult_release_scene_links"} {
		var exists bool
		if err := sqlDB.QueryRowContext(ctx,
			`SELECT to_regclass('public.'||$1) IS NOT NULL`, table,
		).Scan(&exists); err != nil {
			t.Fatalf("checking table %q existence: %v", table, err)
		}
		if !exists {
			t.Errorf("table %q does not exist after migration 0009", table)
		}
	}

	for _, idx := range []string{
		"idx_adult_release_cache_protocol_persisted",
		"idx_adult_release_scene_links_url",
	} {
		var exists bool
		if err := sqlDB.QueryRowContext(ctx,
			`SELECT EXISTS(SELECT 1 FROM pg_indexes WHERE indexname = $1)`, idx,
		).Scan(&exists); err != nil {
			t.Fatalf("checking index %q existence: %v", idx, err)
		}
		if !exists {
			t.Errorf("index %q does not exist after migration 0009", idx)
		}
	}

	// FK cascade: inserting a cache row and a link row, then deleting the cache
	// row, must cascade-delete the link row.
	if _, err := sqlDB.ExecContext(ctx,
		`INSERT INTO adult_release_cache (download_url, protocol) VALUES ($1, $2)`,
		"https://example.com/test.nzb", "usenet",
	); err != nil {
		t.Fatalf("inserting test cache row: %v", err)
	}
	if _, err := sqlDB.ExecContext(ctx,
		`INSERT INTO adult_release_scene_links (scene_key, download_url) VALUES ($1, $2)`,
		"stashdb:test-uuid", "https://example.com/test.nzb",
	); err != nil {
		t.Fatalf("inserting test link row: %v", err)
	}
	if _, err := sqlDB.ExecContext(ctx,
		`DELETE FROM adult_release_cache WHERE download_url = $1`,
		"https://example.com/test.nzb",
	); err != nil {
		t.Fatalf("deleting cache row: %v", err)
	}
	var linkCount int
	if err := sqlDB.QueryRowContext(ctx,
		`SELECT count(*) FROM adult_release_scene_links WHERE scene_key = $1`,
		"stashdb:test-uuid",
	).Scan(&linkCount); err != nil {
		t.Fatalf("counting surviving link rows: %v", err)
	}
	if linkCount != 0 {
		t.Errorf("FK cascade failed: %d link row(s) survive after cache row deleted, want 0", linkCount)
	}
}
