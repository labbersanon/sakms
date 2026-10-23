package db_test

import (
	"context"
	"testing"

	"github.com/labbersanon/sakms/internal/dbtest"

	"github.com/pressly/goose/v3"
)

func TestMigration0056NewznabOndemand(t *testing.T) {
	sqlDB := dbtest.New(t)
	ctx := context.Background()
	goose.SetBaseFS(migrationsFS)
	if err := goose.SetDialect("postgres"); err != nil {
		t.Fatalf("set dialect: %v", err)
	}
	if err := goose.DownTo(sqlDB, "migrations", 55); err != nil {
		t.Fatalf("goose down to 55: %v", err)
	}
	var exists bool
	if err := sqlDB.QueryRowContext(ctx, `SELECT to_regclass('newznab_ondemand') IS NOT NULL`).Scan(&exists); err != nil || exists {
		t.Fatalf("ondemand exists=%v err=%v after down to 55", exists, err)
	}
	if err := goose.UpTo(sqlDB, "migrations", 56); err != nil {
		t.Fatalf("goose up to 56: %v", err)
	}
	if err := sqlDB.QueryRowContext(ctx, `SELECT to_regclass('newznab_ondemand') IS NOT NULL`).Scan(&exists); err != nil || !exists {
		t.Fatalf("ondemand exists=%v err=%v after 0056", exists, err)
	}
	rows, err := sqlDB.QueryContext(ctx, `
		SELECT column_name FROM information_schema.columns
		WHERE table_name = 'newznab_ondemand'`)
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
			t.Fatalf("newznab_ondemand must not store %s", col)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}

func TestMigration0057OndemandBitrate(t *testing.T) {
	sqlDB := dbtest.New(t)
	ctx := context.Background()
	goose.SetBaseFS(migrationsFS)
	if err := goose.SetDialect("postgres"); err != nil {
		t.Fatalf("set dialect: %v", err)
	}
	if err := goose.DownTo(sqlDB, "migrations", 56); err != nil {
		t.Fatalf("goose down to 56: %v", err)
	}
	var exists bool
	if err := sqlDB.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.columns
			WHERE table_name = 'newznab_ondemand' AND column_name = 'bitrate_mbps'
		)`).Scan(&exists); err != nil || exists {
		t.Fatalf("bitrate_mbps exists=%v err=%v after down to 56", exists, err)
	}
	if err := goose.UpTo(sqlDB, "migrations", 57); err != nil {
		t.Fatalf("goose up to 57: %v", err)
	}
	if err := sqlDB.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.columns
			WHERE table_name = 'newznab_ondemand' AND column_name = 'bitrate_mbps'
		)`).Scan(&exists); err != nil || !exists {
		t.Fatalf("bitrate_mbps exists=%v err=%v after 0057", exists, err)
	}
}
