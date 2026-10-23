package db_test

import (
	"context"
	"testing"

	"github.com/labbersanon/sakms/internal/dbtest"

	"github.com/pressly/goose/v3"
)

// Claude 2026-10-15: live down/up for migration 0044 on a seeded index.
// Reason: dbtest clones an already-migrated template, so the release-table
//
//	seed and the watermarks.low backfill only run against REAL rows if we
//	DownTo(43), insert pre-0044-shape headers, then UpTo(44).
//
// Troubleshooting: skips when SAKMS_TEST_DATABASE_URL is unset.
// Review if: 0044's seed statements change.
func TestMigration0044UsenetNNTPGapFill(t *testing.T) {
	sqlDB := dbtest.New(t)
	ctx := context.Background()

	goose.SetBaseFS(migrationsFS)
	if err := goose.SetDialect("postgres"); err != nil {
		t.Fatalf("set dialect: %v", err)
	}
	if err := goose.DownTo(sqlDB, "migrations", 43); err != nil {
		t.Fatalf("goose down to 43: %v", err)
	}
	if v := currentVersion(t, sqlDB); v != 43 {
		t.Fatalf("after Down: goose version %d, want 43", v)
	}
	for _, tbl := range []string{"usenet_nntp_releases", "usenet_nntp_missed_ranges", "usenet_nntp_predb"} {
		var exists bool
		if err := sqlDB.QueryRowContext(ctx, `SELECT to_regclass($1) IS NOT NULL`, tbl).Scan(&exists); err != nil || exists {
			t.Fatalf("%s exists=%v err=%v after Down, want dropped", tbl, exists, err)
		}
	}

	if _, err := sqlDB.ExecContext(ctx, `
		INSERT INTO usenet_nntp_headers (group_name, msg_num, msgid, subject, release_name, part_n, part_m, filename, obfuscated, posted_at, ingested_at)
		VALUES
		 ('g1', 10, 'a@x', 's', 'Rel.A.2020', 1, 2, 'a.mkv', FALSE, 100, 1000),
		 ('g1', 11, 'b@x', 's', 'Rel.A.2020', 2, 2, 'a.mkv', FALSE, 101, 1001),
		 ('g1', 12, 'c@x', 's', 'Rel.B.2021', 1, 1, 'b.mkv', FALSE, 102, 1002),
		 ('g1', 13, 'd@x', 's', '',           1, 1, 'z.rar', TRUE,  103, 1003)`); err != nil {
		t.Fatalf("seed headers: %v", err)
	}
	if _, err := sqlDB.ExecContext(ctx, `INSERT INTO usenet_nntp_watermarks (group_name, high, updated_at) VALUES ('g1', 500, 1), ('g2', 900, 1)`); err != nil {
		t.Fatalf("seed watermarks: %v", err)
	}

	if err := goose.UpTo(sqlDB, "migrations", 44); err != nil {
		t.Fatalf("goose up to 44: %v", err)
	}
	if v := currentVersion(t, sqlDB); v != 44 {
		t.Fatalf("after Up: goose version %d, want 44", v)
	}

	var releases int
	if err := sqlDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM usenet_nntp_releases WHERE dirty AND state='named' AND display_name=release_key`).Scan(&releases); err != nil || releases != 2 {
		t.Fatalf("seeded releases=%d err=%v, want 2 (obfuscated/empty rows excluded)", releases, err)
	}
	var first, last int
	if err := sqlDB.QueryRowContext(ctx, `SELECT first_msg, last_msg FROM usenet_nntp_releases WHERE release_key='Rel.A.2020'`).Scan(&first, &last); err != nil || first != 10 || last != 11 {
		t.Fatalf("Rel.A span=%d-%d err=%v, want 10-11", first, last, err)
	}
	lows := map[string]int{}
	rows, err := sqlDB.QueryContext(ctx, `SELECT group_name, low FROM usenet_nntp_watermarks`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var g string
		var low int
		if err := rows.Scan(&g, &low); err != nil {
			t.Fatal(err)
		}
		lows[g] = low
	}
	if lows["g1"] != 10 || lows["g2"] != 900 {
		t.Fatalf("low=%v, want g1=10 (MIN msg_num) g2=900 (high, no headers)", lows)
	}
}

func TestMigration0052DropsPredb(t *testing.T) {
	sqlDB := dbtest.New(t)
	ctx := context.Background()
	goose.SetBaseFS(migrationsFS)
	if err := goose.SetDialect("postgres"); err != nil {
		t.Fatalf("set dialect: %v", err)
	}
	if err := goose.UpTo(sqlDB, "migrations", 52); err != nil {
		t.Fatalf("goose up to 52: %v", err)
	}
	var exists bool
	if err := sqlDB.QueryRowContext(ctx, `SELECT to_regclass('usenet_nntp_predb') IS NOT NULL`).Scan(&exists); err != nil || exists {
		t.Fatalf("usenet_nntp_predb exists=%v err=%v after 0052, want dropped", exists, err)
	}
	var n int
	if err := sqlDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM settings WHERE key LIKE 'usenet_nntp_predb%'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("predb settings leftover=%d err=%v", n, err)
	}
}
