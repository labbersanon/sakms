package db_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/labbersanon/sakms/internal/dbtest"

	"github.com/pressly/goose/v3"
)

// Claude 2026-08-14: live up for migration 0015.
// Reason: 0015 collapses 0014 one-contains-per-tag clusters into one
//   contains+any values[] list (BDSM five chips) and does not flatten mixed
//   contains+notContains or notContains clusters. dbtest.New clones an
//   already-migrated template, so the rewrite against REAL rows only runs
//   if we DownTo(14), seed 0014-shape JSON, then UpTo(15).
// Troubleshooting: skips when SAKMS_TEST_DATABASE_URL is unset.
// Review if: drag-and-drop rule ordering ships.

func TestMigration0015PruningTagMatchMode(t *testing.T) {
	sqlDB := dbtest.New(t)
	ctx := context.Background()

	goose.SetBaseFS(migrationsFS)
	if err := goose.SetDialect("postgres"); err != nil {
		t.Fatalf("set dialect: %v", err)
	}

	if err := goose.DownTo(sqlDB, "migrations", 14); err != nil {
		t.Fatalf("goose down to 14: %v", err)
	}
	if version := currentVersion(t, sqlDB); version != 14 {
		t.Fatalf("after Down: goose version %d, want 14", version)
	}

	if _, err := sqlDB.ExecContext(ctx, `
		INSERT INTO pruning_rules (name, mode, age_days, size_bytes, quality_tier_floor, tags, min_rating, criteria, enabled)
		VALUES
			('BDSM', 'adult', 0, 0, '', '[]', 0,
			 '[{"field":"tag","op":"contains","value":"Bondage"},{"field":"tag","op":"contains","value":"Bound"},{"field":"tag","op":"contains","value":"Dungeon"},{"field":"tag","op":"contains","value":"Pee"},{"field":"tag","op":"contains","value":"Peeing"}]',
			 true),
			('Mixed', 'adult', 0, 0, '', '[]', 0,
			 '[{"field":"age","op":"gt","value":"30","unit":"days"},{"field":"tag","op":"contains","value":"BDSM"},{"field":"tag","op":"notContains","value":"Pee"},{"field":"rating","op":"lt","value":"3","unit":"stars"}]',
			 true),
			('Exclude two', 'adult', 0, 0, '', '[]', 0,
			 '[{"field":"tag","op":"notContains","value":"Pee"},{"field":"tag","op":"notContains","value":"Peeing"}]',
			 true)
	`); err != nil {
		t.Fatalf("seed 0014-shape rules: %v", err)
	}

	if err := goose.UpTo(sqlDB, "migrations", 15); err != nil {
		t.Fatalf("goose up to 15: %v", err)
	}
	if version := currentVersion(t, sqlDB); version != 15 {
		t.Fatalf("after Up: goose version %d, want 15", version)
	}

	got := map[string]string{}
	rows, err := sqlDB.QueryContext(ctx, `SELECT name, criteria FROM pruning_rules ORDER BY name`)
	if err != nil {
		t.Fatalf("select criteria: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var name, criteria string
		if err := rows.Scan(&name, &criteria); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got[name] = criteria
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

	assertJSON("BDSM", `[{"field":"tag","op":"contains","matchMode":"any","values":["Bondage","Bound","Dungeon","Pee","Peeing"]}]`)
	assertJSON("Mixed", `[{"field":"age","op":"gt","value":"30","unit":"days"},{"field":"tag","op":"contains","matchMode":"any","values":["BDSM"]},{"field":"tag","op":"notContains","matchMode":"any","values":["Pee"]},{"field":"rating","op":"lt","value":"3","unit":"stars"}]`)
	assertJSON("Exclude two", `[{"field":"tag","op":"notContains","matchMode":"any","values":["Pee"]},{"field":"tag","op":"notContains","matchMode":"any","values":["Peeing"]}]`)
}
