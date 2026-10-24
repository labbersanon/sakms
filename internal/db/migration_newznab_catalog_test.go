package db_test

import (
	"context"
	"testing"

	"github.com/labbersanon/sakms/internal/dbtest"

	"github.com/pressly/goose/v3"
)

func TestMigration0055NewznabCatalog(t *testing.T) {
	sqlDB := dbtest.New(t)
	ctx := context.Background()
	goose.SetBaseFS(migrationsFS)
	if err := goose.SetDialect("postgres"); err != nil {
		t.Fatalf("set dialect: %v", err)
	}
	if err := goose.DownTo(sqlDB, "migrations", 54); err != nil {
		t.Fatalf("goose down to 54: %v", err)
	}
	for _, tbl := range []string{"newznab_catalog", "newznab_catalog_cursors"} {
		var exists bool
		if err := sqlDB.QueryRowContext(ctx, `SELECT to_regclass($1) IS NOT NULL`, tbl).Scan(&exists); err != nil || exists {
			t.Fatalf("%s exists=%v err=%v after down to 54, want dropped", tbl, exists, err)
		}
	}
	if err := goose.UpTo(sqlDB, "migrations", 55); err != nil {
		t.Fatalf("goose up to 55: %v", err)
	}
	for _, tbl := range []string{"newznab_catalog", "newznab_catalog_cursors"} {
		var exists bool
		if err := sqlDB.QueryRowContext(ctx, `SELECT to_regclass($1) IS NOT NULL`, tbl).Scan(&exists); err != nil || !exists {
			t.Fatalf("%s exists=%v err=%v after 0055, want present", tbl, exists, err)
		}
	}
	rows, err := sqlDB.QueryContext(ctx, `
		SELECT column_name FROM information_schema.columns
		WHERE table_name = 'newznab_catalog'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var col string
		if err := rows.Scan(&col); err != nil {
			t.Fatal(err)
		}
		switch col {
		case "url", "enclosure", "link", "download_url", "apikey", "api_key":
			t.Fatalf("newznab_catalog must not store %s", col)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if _, err := sqlDB.ExecContext(ctx, `
		INSERT INTO newznab_catalog (indexer, guid, title, kind, overlap_key)
		VALUES ('newznab', 'g1', 'A', 'movies', 'imdb:tt1')`); err != nil {
		t.Fatalf("insert: %v", err)
	}
}

func TestMigration0058NewznabCatalogOverlap(t *testing.T) {
	sqlDB := dbtest.New(t)
	ctx := context.Background()
	goose.SetBaseFS(migrationsFS)
	if err := goose.SetDialect("postgres"); err != nil {
		t.Fatalf("set dialect: %v", err)
	}
	if err := goose.DownTo(sqlDB, "migrations", 57); err != nil {
		t.Fatalf("goose down to 57: %v", err)
	}
	var exists bool
	if err := sqlDB.QueryRowContext(ctx, `SELECT to_regclass('newznab_catalog_overlap') IS NOT NULL`).Scan(&exists); err != nil || exists {
		t.Fatalf("overlap exists=%v err=%v after down to 57", exists, err)
	}
	if err := goose.UpTo(sqlDB, "migrations", 58); err != nil {
		t.Fatalf("goose up to 58: %v", err)
	}
	if err := sqlDB.QueryRowContext(ctx, `SELECT to_regclass('newznab_catalog_overlap') IS NOT NULL`).Scan(&exists); err != nil || !exists {
		t.Fatalf("overlap exists=%v err=%v after 0058", exists, err)
	}
	rows, err := sqlDB.QueryContext(ctx, `
		SELECT column_name FROM information_schema.columns
		WHERE table_name = 'newznab_catalog_overlap'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var col string
		if err := rows.Scan(&col); err != nil {
			t.Fatal(err)
		}
		switch col {
		case "url", "enclosure", "link", "download_url", "apikey", "api_key":
			t.Fatalf("newznab_catalog_overlap must not store %s", col)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}
