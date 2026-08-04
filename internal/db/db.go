// Package db opens SAK's PostgreSQL database and applies pending migrations.
package db

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"time"

	"github.com/pressly/goose/v3"
)

//go:embed migrations/*.sql
var migrations embed.FS

// PoolOptions controls the database/sql connection pool.
type PoolOptions struct {
	MaxOpen int
	MaxIdle int
}

// DefaultPoolOptions matches the Postgres migration plan defaults.
func DefaultPoolOptions() PoolOptions {
	return PoolOptions{MaxOpen: 20, MaxIdle: 5}
}

// Open connects to PostgreSQL at dsn, waits briefly for readiness, applies
// pending goose migrations, and returns a pooled *sql.DB.
//
// Claude 2026-08-04: SQLite path + SetMaxOpenConns(1) removed for Postgres-only.
// Reason: sole-conn starvation of apply-batch / background jobs (deep-interview
// sakms-postgres-migration).
// Troubleshooting: if Open fails at boot, check SAKMS_DATABASE_URL(_FILE) and
// sakms-db health; Ping retries ~30s for slow initdb.
// Review if: pool defaults change or dual-driver returns.
// Related files: internal/config/config.go, cmd/sakms/main.go
func Open(ctx context.Context, dsn string, opts PoolOptions) (*sql.DB, error) {
	if dsn == "" {
		return nil, fmt.Errorf("database DSN is empty — set SAKMS_DATABASE_URL or SAKMS_DATABASE_URL_FILE")
	}
	if opts.MaxOpen <= 0 {
		opts.MaxOpen = 20
	}
	if opts.MaxIdle <= 0 {
		opts.MaxIdle = 5
	}

	sqlDB, err := sql.Open("pgx-qmark", dsn)
	if err != nil {
		return nil, fmt.Errorf("opening postgres: %w", err)
	}
	sqlDB.SetMaxOpenConns(opts.MaxOpen)
	sqlDB.SetMaxIdleConns(opts.MaxIdle)
	sqlDB.SetConnMaxIdleTime(5 * time.Minute)
	sqlDB.SetConnMaxLifetime(1 * time.Hour)

	var pingErr error
	for attempt := 0; attempt < 15; attempt++ {
		pingErr = sqlDB.PingContext(ctx)
		if pingErr == nil {
			break
		}
		select {
		case <-ctx.Done():
			sqlDB.Close()
			return nil, fmt.Errorf("waiting for postgres: %w", ctx.Err())
		case <-time.After(2 * time.Second):
		}
	}
	if pingErr != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("postgres not ready after retries: %w", pingErr)
	}

	goose.SetBaseFS(migrations)
	if err := goose.SetDialect("postgres"); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("setting migration dialect: %w", err)
	}
	if err := goose.Up(sqlDB, "migrations"); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("running migrations: %w", err)
	}
	return sqlDB, nil
}
