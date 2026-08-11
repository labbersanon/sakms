package db_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"

	"github.com/labbersanon/sakms/internal/dbtest"

	"github.com/pressly/goose/v3"
)

// Claude 2026-08-11: live up/down round-trip for migration 0008.
// Reason: 0008 converts every purge_allowlist row into an auto-created
// per-mode "Legacy allowlist" pruning rule before dropping the table. That
// conversion IS the feature's "nothing an operator already configured
// silently stops matching" requirement, and NO store test can exercise it:
// dbtest.New(t) clones an already-fully-migrated template database, so by the
// time any store test runs, 0008's Up has already executed against an EMPTY
// purge_allowlist during template build. Nothing anywhere runs its Down
// either, so the LATERAL jsonb_array_elements_text restore, the recreated
// unique index and the DROP COLUMN ordering would all ship having never
// executed once.
// Troubleshooting: skips when SAKMS_TEST_DATABASE_URL is unset (dbtest.New's
// own gate) — a skip here means the migration is UNVERIFIED, not passing.
// Review if: internal/dbtest stops cloning a fully-migrated template database.
//
// Modelled on migration_library_episode_files_test.go, which establishes this
// pattern, with ONE deliberate divergence: that test seeds BEFORE its Down,
// because 0005 does not drop the table it seeds. 0008 does — purge_allowlist
// does not exist at version 8 — so seeding has to happen in the window
// between DownTo(7) and UpTo(8). migrationsFS and currentVersion are declared
// by that sibling file; new helpers here are prefixed to avoid collisions.

// TestMigration0008PruningRuleTags asserts the allowlist->rule conversion runs
// against REAL rows, that a mode with no tags gets no rule at all, and that
// the Down restores tag-only rules while deliberately leaving combined rules
// (tags AND another condition) in place.
func TestMigration0008PruningRuleTags(t *testing.T) {
	// No t.Parallel: goose's dialect and base FS are package globals.
	sqlDB := dbtest.New(t)
	ctx := context.Background()

	goose.SetBaseFS(migrationsFS)
	if err := goose.SetDialect("postgres"); err != nil {
		t.Fatalf("set dialect: %v", err)
	}

	// --- Down: 0008 -> 0007, recreating purge_allowlist ---------------------
	if err := goose.DownTo(sqlDB, "migrations", 7); err != nil {
		t.Fatalf("goose down to 7: %v", err)
	}
	if version := currentVersion(t, sqlDB); version != 7 {
		t.Fatalf("after Down: goose version %d, want 7", version)
	}
	if !pruningTagsTableExists(t, sqlDB, "purge_allowlist") {
		t.Fatal("after Down: purge_allowlist was not recreated")
	}
	if pruningTagsColumnExists(t, sqlDB) {
		t.Fatal("after Down: pruning_rules.tags still exists")
	}

	// --- Seed REAL allowlist rows in front of the Up ------------------------
	// Two modes with tags; adult deliberately gets NONE. "Rope" before "BDSM"
	// so the ORDER BY lower(tag) in the conversion is actually exercised
	// rather than coinciding with insertion order.
	seedAllowlistTag(t, sqlDB, "movies", "Rope")
	seedAllowlistTag(t, sqlDB, "movies", "BDSM")
	seedAllowlistTag(t, sqlDB, "series", "Trailer")
	// A whitespace-only row: pruning.ErrBlankTag is stricter than the retired
	// addAllowlistTagHandler guard (which rejected only exactly ""), so the
	// conversion's btrim filter must drop this or the migrated rule would fail
	// validation on the operator's next edit.
	seedAllowlistTag(t, sqlDB, "series", "   ")

	// --- Up: 0007 -> 0008, running the conversion ---------------------------
	if err := goose.UpTo(sqlDB, "migrations", 8); err != nil {
		t.Fatalf("goose up to 8: %v", err)
	}
	if version := currentVersion(t, sqlDB); version != 8 {
		t.Fatalf("after Up: goose version %d, want 8", version)
	}
	if pruningTagsTableExists(t, sqlDB, "purge_allowlist") {
		t.Fatal("after Up: purge_allowlist should have been dropped")
	}

	// Exactly one Legacy allowlist rule per seeded mode, enabled, tags in
	// lower(tag) order.
	for _, tc := range []struct {
		mode string
		want []string
	}{
		{"movies", []string{"BDSM", "Rope"}},
		{"series", []string{"Trailer"}},
	} {
		rules := legacyRulesFor(t, sqlDB, tc.mode)
		if len(rules) != 1 {
			t.Fatalf("mode %q: got %d Legacy allowlist rules, want exactly 1", tc.mode, len(rules))
		}
		if !rules[0].enabled {
			t.Errorf("mode %q: migrated rule is disabled, want enabled", tc.mode)
		}
		if len(rules[0].tags) != len(tc.want) {
			t.Fatalf("mode %q: tags = %v, want %v", tc.mode, rules[0].tags, tc.want)
		}
		for i, want := range tc.want {
			if rules[0].tags[i] != want {
				t.Errorf("mode %q: tags = %v, want %v (lower(tag) order)", tc.mode, rules[0].tags, tc.want)
			}
		}
	}

	// The zero-tag mode gets NO rule at all — an empty-tags rule would be
	// conditionless, would fail Validate on the next edit, and would clutter
	// the Rules card with a row that matches nothing.
	if n := countRulesFor(t, sqlDB, "adult"); n != 0 {
		t.Errorf("mode adult had no allowlist tags but got %d rule(s), want 0", n)
	}

	// A rule combining tags WITH another condition — the Down's documented
	// lossiness case.
	if _, err := sqlDB.ExecContext(ctx,
		`INSERT INTO pruning_rules (name, mode, age_days, size_bytes, quality_tier_floor, tags, enabled)
		 VALUES (?, ?, ?, ?, ?, ?, true)`,
		"Old and tagged", "movies", 365, 0, "", `["Trailer"]`,
	); err != nil {
		t.Fatalf("insert combined rule: %v", err)
	}

	// --- Down again: 0008 -> 0007, restoring the allowlist ------------------
	if err := goose.DownTo(sqlDB, "migrations", 7); err != nil {
		t.Fatalf("second goose down to 7: %v", err)
	}
	if pruningTagsColumnExists(t, sqlDB) {
		t.Fatal("after second Down: pruning_rules.tags column still exists")
	}

	// Tag-only rules are restored into purge_allowlist and then deleted.
	for _, tc := range []struct {
		mode string
		want []string
	}{
		{"movies", []string{"BDSM", "Rope"}},
		{"series", []string{"Trailer"}},
	} {
		got := allowlistTagsFor(t, sqlDB, tc.mode)
		if len(got) != len(tc.want) {
			t.Fatalf("mode %q: restored allowlist = %v, want %v", tc.mode, got, tc.want)
		}
		for i, want := range tc.want {
			if got[i] != want {
				t.Errorf("mode %q: restored allowlist = %v, want %v", tc.mode, got, tc.want)
			}
		}
	}
	if n := countRulesFor(t, sqlDB, "series"); n != 0 {
		t.Errorf("after Down: %d series rule(s) survive, want 0 (tag-only rules are deleted)", n)
	}

	// The combined rule SURVIVES with its other condition intact — it loses
	// only its tags, which is the migration's documented, intended lossiness
	// (purge_allowlist has nowhere to put a tag that is only meaningful
	// alongside another condition). Pinning it here makes that intended, not
	// accidental.
	var name string
	var ageDays int
	if err := sqlDB.QueryRowContext(ctx,
		`SELECT name, age_days FROM pruning_rules WHERE mode = ?`, "movies",
	).Scan(&name, &ageDays); err != nil {
		t.Fatalf("expected exactly one surviving movies rule: %v", err)
	}
	if name != "Old and tagged" || ageDays != 365 {
		t.Errorf("surviving rule = (%q, %d days), want (\"Old and tagged\", 365)", name, ageDays)
	}
}

type migratedRule struct {
	tags    []string
	enabled bool
}

func seedAllowlistTag(t *testing.T, sqlDB *sql.DB, mode, tag string) {
	t.Helper()
	if _, err := sqlDB.ExecContext(context.Background(),
		`INSERT INTO purge_allowlist (mode, tag) VALUES (?, ?)`, mode, tag,
	); err != nil {
		t.Fatalf("seed allowlist row (%s, %q): %v", mode, tag, err)
	}
}

func legacyRulesFor(t *testing.T, sqlDB *sql.DB, mode string) []migratedRule {
	t.Helper()
	rows, err := sqlDB.QueryContext(context.Background(),
		`SELECT tags, enabled FROM pruning_rules WHERE mode = ? AND name = 'Legacy allowlist' ORDER BY id`, mode)
	if err != nil {
		t.Fatalf("query migrated rules for %s: %v", mode, err)
	}
	defer rows.Close()
	var out []migratedRule
	for rows.Next() {
		var tagsJSON string
		var enabled bool
		if err := rows.Scan(&tagsJSON, &enabled); err != nil {
			t.Fatalf("scan migrated rule: %v", err)
		}
		var tags []string
		if err := json.Unmarshal([]byte(tagsJSON), &tags); err != nil {
			t.Fatalf("decoding migrated tags %q: %v", tagsJSON, err)
		}
		out = append(out, migratedRule{tags: tags, enabled: enabled})
	}
	return out
}

func countRulesFor(t *testing.T, sqlDB *sql.DB, mode string) int {
	t.Helper()
	var n int
	if err := sqlDB.QueryRowContext(context.Background(),
		`SELECT count(*) FROM pruning_rules WHERE mode = ?`, mode,
	).Scan(&n); err != nil {
		t.Fatalf("count rules for %s: %v", mode, err)
	}
	return n
}

func allowlistTagsFor(t *testing.T, sqlDB *sql.DB, mode string) []string {
	t.Helper()
	rows, err := sqlDB.QueryContext(context.Background(),
		`SELECT tag FROM purge_allowlist WHERE mode = ? ORDER BY lower(tag)`, mode)
	if err != nil {
		t.Fatalf("query restored allowlist for %s: %v", mode, err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var tag string
		if err := rows.Scan(&tag); err != nil {
			t.Fatalf("scan restored tag: %v", err)
		}
		out = append(out, tag)
	}
	return out
}

func pruningTagsTableExists(t *testing.T, sqlDB *sql.DB, table string) bool {
	t.Helper()
	var exists bool
	if err := sqlDB.QueryRowContext(context.Background(),
		`SELECT to_regclass('public.' || ?) IS NOT NULL`, table,
	).Scan(&exists); err != nil {
		t.Fatalf("check table %q existence: %v", table, err)
	}
	return exists
}

func pruningTagsColumnExists(t *testing.T, sqlDB *sql.DB) bool {
	t.Helper()
	var exists bool
	if err := sqlDB.QueryRowContext(context.Background(),
		`SELECT EXISTS(SELECT 1 FROM information_schema.columns
		               WHERE table_name = 'pruning_rules' AND column_name = 'tags')`,
	).Scan(&exists); err != nil {
		t.Fatalf("check pruning_rules.tags existence: %v", err)
	}
	return exists
}
