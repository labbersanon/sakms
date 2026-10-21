// Package dbtest provides isolated Postgres databases for sakms unit tests.
package dbtest

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/labbersanon/sakms/internal/db"

	_ "github.com/jackc/pgx/v5/stdlib"
)

var (
	schemaSafe   = regexp.MustCompile(`[^a-zA-Z0-9_]`)
	templateOnce sync.Once
	templateErr  error
	templateName = "sakms_goose_template"
	adminOnce    sync.Once
	adminDB      *sql.DB
	adminErr     error
	dropSem      = make(chan struct{}, 4)
)

// New returns a migrated, isolated Postgres database for one test.
//
// Claude 2026-08-04: TEMPLATE-database harness (schema-per-test was ~20s/goose).
// Reason: WS5 — replace TempDir sakms.db; SAKMS_TEST_DATABASE_URL gates skips.
// Troubleshooting: skip message names the env var; set SAKMS_TEST_DATABASE_URL_REQUIRED=1 or CI=true to fail.
// Review if: CI pin / compose Postgres major version diverge.
func New(t *testing.T) *sql.DB {
	t.Helper()
	return OpenNamed(t, uniqueName(t))
}

// SharedSchema returns a stable database name and ensures it exists (migrated
// via template). Call OpenNamed again after Close for restart-durability tests.
// Name kept for call-site compatibility with the original schema-based API.
func SharedSchema(t *testing.T) string {
	t.Helper()
	name := uniqueName(t)
	ensureClone(t, name)
	return name
}

// OpenSchema is an alias for OpenNamed (shared-name reopen path).
func OpenSchema(t *testing.T, name string) *sql.DB {
	t.Helper()
	return OpenNamed(t, name)
}

// OpenNamed connects to name (creating from the goose template if needed).
func OpenNamed(t *testing.T, name string) *sql.DB {
	t.Helper()
	ensureClone(t, name)
	// Claude 2026-10-21: clones skip goose.Up and the 15×2s Open ping loop.
	// Reason: TEMPLATE already migrated; api's 1679 tests each called db.Open
	//   and a fresh admin ping+DROP. go test ./internal/api hit 60s/90s
	//   timeouts on whichever test was next (TypedDateOutsideWindow).
	// Troubleshooting: clone missing a new migration → remake sakms_goose_template
	//   (ensureTemplate still db.Open's it). Review if: tests need goose on clones.
	sqlDB, err := sql.Open("pgx-qmark", withDBName(testDSN(t), name))
	if err != nil {
		t.Fatalf("dbtest open %q: %v", name, err)
	}
	sqlDB.SetMaxOpenConns(4)
	sqlDB.SetMaxIdleConns(1)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := sqlDB.PingContext(ctx); err != nil {
		_ = sqlDB.Close()
		t.Fatalf("dbtest ping %q: %v", name, err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	return sqlDB
}

func testDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("SAKMS_TEST_DATABASE_URL")
	if dsn == "" {
		if os.Getenv("SAKMS_TEST_DATABASE_URL_REQUIRED") == "1" || os.Getenv("CI") == "true" {
			t.Fatal("SAKMS_TEST_DATABASE_URL is required in CI — start postgres and export it")
		}
		t.Skip("SAKMS_TEST_DATABASE_URL not set; e.g. docker run -d -e POSTGRES_PASSWORD=sakms -e POSTGRES_USER=sakms -e POSTGRES_DB=sakms -p 5433:5432 postgres:16-alpine")
	}
	return dsn
}

func uniqueName(t *testing.T) string {
	name := schemaSafe.ReplaceAllString(t.Name(), "_")
	if len(name) > 40 {
		name = name[:40]
	}
	return fmt.Sprintf("t_%s_%d", strings.ToLower(name), time.Now().UnixNano())
}

func sharedAdmin(t *testing.T) *sql.DB {
	t.Helper()
	dsn := testDSN(t)
	adminOnce.Do(func() {
		d, err := sql.Open("pgx", withDBName(dsn, "postgres"))
		if err != nil {
			adminErr = err
			return
		}
		d.SetMaxOpenConns(8)
		d.SetMaxIdleConns(2)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := d.PingContext(ctx); err != nil {
			_ = d.Close()
			adminErr = err
			return
		}
		adminDB = d
	})
	if adminErr != nil {
		t.Fatalf("dbtest admin: %v", adminErr)
	}
	return adminDB
}

func adminOpen(t *testing.T, dsn string) *sql.DB {
	t.Helper()
	admin, err := sql.Open("pgx", withDBName(dsn, "postgres"))
	if err != nil {
		t.Fatalf("dbtest admin open: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := admin.PingContext(ctx); err != nil {
		_ = admin.Close()
		t.Fatalf("dbtest admin ping: %v", err)
	}
	return admin
}

func ensureTemplate(t *testing.T, dsn string) {
	t.Helper()
	templateOnce.Do(func() {
		admin := adminOpen(t, dsn)
		defer admin.Close()
		ctx := context.Background()
		if _, err := admin.ExecContext(ctx, "SELECT pg_advisory_lock(742001)"); err != nil {
			templateErr = fmt.Errorf("advisory lock: %w", err)
			return
		}
		defer func() {
			_, _ = admin.ExecContext(context.Background(), "SELECT pg_advisory_unlock(742001)")
		}()

		var exists bool
		if err := admin.QueryRowContext(ctx,
			`SELECT EXISTS(SELECT 1 FROM pg_database WHERE datname = $1)`, templateName,
		).Scan(&exists); err != nil {
			templateErr = err
			return
		}
		if !exists {
			if _, err := admin.ExecContext(ctx, "CREATE DATABASE "+quoteIdent(templateName)); err != nil {
				// Parallel test processes may race past the EXISTS check.
				if !strings.Contains(err.Error(), "already exists") &&
					!strings.Contains(err.Error(), "duplicate key") &&
					!strings.Contains(err.Error(), "23505") {
					templateErr = fmt.Errorf("create template db: %w", err)
					return
				}
			}
		}
		mig, err := db.Open(ctx, withDBName(dsn, templateName), db.DefaultPoolOptions())
		if err != nil {
			templateErr = fmt.Errorf("migrate template db: %w", err)
			return
		}
		_ = mig.Close()
		if _, err := admin.ExecContext(ctx,
			"ALTER DATABASE "+quoteIdent(templateName)+" IS_TEMPLATE true",
		); err != nil {
			templateErr = fmt.Errorf("mark template: %w", err)
			return
		}
	})
	if templateErr != nil {
		t.Fatalf("dbtest template: %v", templateErr)
	}
}

func ensureClone(t *testing.T, name string) {
	t.Helper()
	dsn := testDSN(t)
	ensureTemplate(t, dsn)
	admin := sharedAdmin(t)
	ctx := context.Background()

	var exists bool
	if err := admin.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM pg_database WHERE datname = $1)`, name,
	).Scan(&exists); err != nil {
		t.Fatalf("dbtest exists check: %v", err)
	}
	if !exists {
		if _, err := admin.ExecContext(ctx,
			"CREATE DATABASE "+quoteIdent(name)+" TEMPLATE "+quoteIdent(templateName),
		); err != nil {
			t.Fatalf("create clone %s: %v", name, err)
		}
	}
	t.Cleanup(func() {
		// Claude 2026-10-21: DROP in the background so the next test's
		// CREATE DATABASE is not gated on ~40ms drop. Sem-limited so a
		// 1679-test package does not stampede Postgres.
		go dropClone(admin, name)
	})
}

func dropClone(admin *sql.DB, name string) {
	dropSem <- struct{}{}
	defer func() { <-dropSem }()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, _ = admin.ExecContext(ctx,
		`SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = $1 AND pid <> pg_backend_pid()`,
		name,
	)
	_, _ = admin.ExecContext(ctx, "DROP DATABASE IF EXISTS "+quoteIdent(name))
}

func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

func withDBName(dsn, dbName string) string {
	u, err := url.Parse(dsn)
	if err != nil || u.Scheme == "" {
		// key=value form
		parts := strings.Fields(dsn)
		out := make([]string, 0, len(parts)+1)
		replaced := false
		for _, p := range parts {
			if strings.HasPrefix(p, "dbname=") {
				out = append(out, "dbname="+dbName)
				replaced = true
			} else {
				out = append(out, p)
			}
		}
		if !replaced {
			out = append(out, "dbname="+dbName)
		}
		return strings.Join(out, " ")
	}
	u.Path = "/" + dbName
	return u.String()
}
