package db_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/labbersanon/sakms/internal/dbtest"

	"github.com/pressly/goose/v3"
)

// Claude 2026-08-14: live up/down round-trip for migration 0014.
// Reason: 0014 backfills pruning_rules.criteria from the five scalars. Tag
// rows become AND (one contains per tag). Quality lossless is omitted so
// Match keeps the legacy path. dbtest.New clones an already-migrated
// template, so the backfill against REAL rows only runs if we DownTo(13),
// seed, then UpTo(14).
// Troubleshooting: skips when SAKMS_TEST_DATABASE_URL is unset.
// Review if: gte/lte operators are added, or tags regain OR-within-rule.

func TestMigration0014PruningRuleCriteria(t *testing.T) {
	sqlDB := dbtest.New(t)
	ctx := context.Background()

	goose.SetBaseFS(migrationsFS)
	if err := goose.SetDialect("postgres"); err != nil {
		t.Fatalf("set dialect: %v", err)
	}

	if err := goose.DownTo(sqlDB, "migrations", 13); err != nil {
		t.Fatalf("goose down to 13: %v", err)
	}
	if version := currentVersion(t, sqlDB); version != 13 {
		t.Fatalf("after Down: goose version %d, want 13", version)
	}

	if _, err := sqlDB.ExecContext(ctx, `
		INSERT INTO pruning_rules (name, mode, age_days, size_bytes, quality_tier_floor, tags, min_rating, enabled)
		VALUES
			('Stale', 'movies', 365, 0, '', '[]', 0, true),
			('Big', 'movies', 0, 2147483648, '', '[]', 0, true),
			('Low', 'movies', 0, 0, 'low', '[]', 0, true),
			('Tagged', 'adult', 0, 0, '', '["BDSM","Pee"]', 0, true),
			('Junk', 'movies', 0, 0, '', '[]', 3, true),
			('Lossless floor', 'movies', 0, 0, 'lossless', '[]', 0, true)
	`); err != nil {
		t.Fatalf("seed pre-0014 rules: %v", err)
	}

	if err := goose.UpTo(sqlDB, "migrations", 14); err != nil {
		t.Fatalf("goose up to 14: %v", err)
	}
	if version := currentVersion(t, sqlDB); version != 14 {
		t.Fatalf("after Up: goose version %d, want 14", version)
	}

	type row struct {
		Name     string
		Criteria string
	}
	rows, err := sqlDB.QueryContext(ctx, `SELECT name, criteria FROM pruning_rules ORDER BY name`)
	if err != nil {
		t.Fatalf("select criteria: %v", err)
	}
	defer rows.Close()
	got := map[string]string{}
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.Name, &r.Criteria); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got[r.Name] = r.Criteria
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}

	assertJSON := func(name, want string) {
		t.Helper()
		raw, ok := got[name]
		if !ok {
			t.Fatalf("missing rule %q", name)
		}
		var gotVal, wantVal any
		if err := json.Unmarshal([]byte(raw), &gotVal); err != nil {
			t.Fatalf("%s criteria %q: %v", name, raw, err)
		}
		if err := json.Unmarshal([]byte(want), &wantVal); err != nil {
			t.Fatalf("want JSON: %v", err)
		}
		gotB, _ := json.Marshal(gotVal)
		wantB, _ := json.Marshal(wantVal)
		if string(gotB) != string(wantB) {
			t.Errorf("%s criteria = %s, want %s", name, gotB, wantB)
		}
	}

	assertJSON("Stale", `[{"field":"age","op":"gt","value":"365","unit":"days"}]`)
	assertJSON("Big", `[{"field":"size","op":"gt","value":"2","unit":"gb"}]`)
	assertJSON("Low", `[{"field":"quality","op":"eq","value":"low"}]`)
	assertJSON("Tagged", `[{"field":"tag","op":"contains","value":"BDSM"},{"field":"tag","op":"contains","value":"Pee"}]`)
	assertJSON("Junk", `[{"field":"rating","op":"lt","value":"3","unit":"stars"}]`)
	assertJSON("Lossless floor", `[]`)
}
