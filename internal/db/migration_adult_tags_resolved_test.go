package db_test

import (
	"context"
	"testing"

	"github.com/labbersanon/sakms/internal/dbtest"

	"github.com/pressly/goose/v3"
)

// Claude 2026-08-29: live up for migration 0016's tags_resolved backfill.
// Reason: 0016 ADD COLUMN DEFAULT 0 is cheap, but the UPDATE that marks rows
//   with real genres as already-resolved is load-bearing — NULL/''/'[]' must
//   stay 0 so the tag backfill can drain them. dbtest.New is already at HEAD,
//   so DownTo(15) → seed pre-16 rows → UpTo(16) is the only way to exercise
//   that predicate against real data.
// Troubleshooting: skips when SAKMS_TEST_DATABASE_URL is unset.
// Review if: tags_resolved is dropped or the empty-genre predicate changes.

func TestMigration0016AdultTagsResolved(t *testing.T) {
	sqlDB := dbtest.New(t)
	ctx := context.Background()

	goose.SetBaseFS(migrationsFS)
	if err := goose.SetDialect("postgres"); err != nil {
		t.Fatalf("set dialect: %v", err)
	}

	if err := goose.DownTo(sqlDB, "migrations", 15); err != nil {
		t.Fatalf("goose down to 15: %v", err)
	}
	if version := currentVersion(t, sqlDB); version != 15 {
		t.Fatalf("after Down: goose version %d, want 15", version)
	}

	if _, err := sqlDB.ExecContext(ctx, `
		INSERT INTO adult_newest_releases
			(row_type, entity_id, entity_source, entity_title, genres)
		VALUES
			('scene', 'has-tags', 'stashdb', 'With Tags', '["Anal","Blonde"]'),
			('scene', 'empty-arr', 'stashdb', 'Empty Arr', '[]'),
			('scene', 'empty-str', 'stashdb', 'Empty Str', '')
	`); err != nil {
		t.Fatalf("seed pre-16 rows: %v", err)
	}

	if err := goose.UpTo(sqlDB, "migrations", 16); err != nil {
		t.Fatalf("goose up to 16: %v", err)
	}
	if version := currentVersion(t, sqlDB); version != 16 {
		t.Fatalf("after Up: goose version %d, want 16", version)
	}

	want := map[string]int{
		"has-tags":  1,
		"empty-arr": 0,
		"empty-str": 0,
	}
	rows, err := sqlDB.QueryContext(ctx,
		`SELECT entity_id, tags_resolved FROM adult_newest_releases
		 WHERE entity_id IN ('has-tags','empty-arr','empty-str')`)
	if err != nil {
		t.Fatalf("select tags_resolved: %v", err)
	}
	defer rows.Close()
	got := map[string]int{}
	for rows.Next() {
		var id string
		var resolved int
		if err := rows.Scan(&id, &resolved); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got[id] = resolved
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	for id, wantVal := range want {
		if got[id] != wantVal {
			t.Errorf("entity %q tags_resolved=%d, want %d", id, got[id], wantVal)
		}
	}
}
