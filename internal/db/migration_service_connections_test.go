package db

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite"
)

// openTo opens a fresh DB migrated up to exactly version v. Pinning the version
// (rather than goose.Up) keeps these tests about migration 0053 specifically,
// independent of how many migrations land after it — the lesson
// migration_cpu_cap_test.go already learned.
func openTo(t *testing.T, v int64) *sql.DB {
	t.Helper()
	sqlDB, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "sakms.db"))
	if err != nil {
		t.Fatalf("opening db: %v", err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	sqlDB.SetMaxOpenConns(1)

	goose.SetBaseFS(migrations)
	if err := goose.SetDialect("sqlite3"); err != nil {
		t.Fatalf("setting dialect: %v", err)
	}
	if err := goose.UpTo(sqlDB, "migrations", v); err != nil {
		t.Fatalf("goose UpTo %d: %v", v, err)
	}
	return sqlDB
}

func scalarString(t *testing.T, sqlDB *sql.DB, query string, args ...any) string {
	t.Helper()
	var out string
	if err := sqlDB.QueryRow(query, args...).Scan(&out); err != nil {
		t.Fatalf("query %q: %v", query, err)
	}
	return out
}

func scalarInt(t *testing.T, sqlDB *sql.DB, query string, args ...any) int {
	t.Helper()
	var out int
	if err := sqlDB.QueryRow(query, args...).Scan(&out); err != nil {
		t.Fatalf("query %q: %v", query, err)
	}
	return out
}

// TestMigration0053_MovesExactlyTwoConnectionRows proves the data move: the
// nntp and jellyfin rows leave `connections` for `service_connections` with
// their ciphertext copied verbatim (same internal/secrets key, so no
// decrypt/re-encrypt), jellyfin keeps today's hardcoded Movies/Series scoping,
// and every OTHER singleton service is left completely untouched.
func TestMigration0053_MovesExactlyTwoConnectionRows(t *testing.T) {
	sqlDB := openTo(t, 52)

	for _, c := range []struct{ service, url, username, secret string }{
		{"nntp", "nntps://news.example.com:563", "usenetuser", "ct-nntp"},
		{"jellyfin", "http://jellyfin:8096", "", "ct-jellyfin"},
		{"tmdb", "", "", "ct-tmdb"},
		{"prowlarr", "http://prowlarr:9696", "", "ct-prowlarr"},
	} {
		if _, err := sqlDB.Exec(
			`INSERT INTO connections (service, url, username, api_key_encrypted) VALUES (?, ?, ?, ?)`,
			c.service, c.url, c.username, c.secret); err != nil {
			t.Fatalf("seeding connection %q: %v", c.service, err)
		}
	}

	if err := goose.UpTo(sqlDB, "migrations", 53); err != nil {
		t.Fatalf("goose UpTo 53: %v", err)
	}

	if n := scalarInt(t, sqlDB, `SELECT COUNT(*) FROM connections WHERE service IN ('nntp','jellyfin')`); n != 0 {
		t.Errorf("nntp/jellyfin should have left connections, %d row(s) remain", n)
	}
	if n := scalarInt(t, sqlDB, `SELECT COUNT(*) FROM connections`); n != 2 {
		t.Errorf("the other singleton services must be untouched, want 2 rows got %d", n)
	}
	if n := scalarInt(t, sqlDB, `SELECT COUNT(*) FROM service_connections`); n != 2 {
		t.Fatalf("want 2 registry rows, got %d", n)
	}

	// Ciphertext copied verbatim, and the legacy url carried over for the Go
	// backfill (internal/serviceconn.BackfillUsenetURL) to normalize.
	if got := scalarString(t, sqlDB, `SELECT secret_encrypted FROM service_connections WHERE provider = 'nntp'`); got != "ct-nntp" {
		t.Errorf("nntp ciphertext should be copied verbatim, got %q", got)
	}
	if got := scalarString(t, sqlDB, `SELECT url FROM service_connections WHERE provider = 'nntp'`); got != "nntps://news.example.com:563" {
		t.Errorf("nntp url should be carried over for the Go backfill, got %q", got)
	}
	if got := scalarString(t, sqlDB, `SELECT username FROM service_connections WHERE provider = 'nntp'`); got != "usenetuser" {
		t.Errorf("nntp username should be carried over, got %q", got)
	}
	if got := scalarString(t, sqlDB, `SELECT secret_encrypted FROM service_connections WHERE provider = 'jellyfin'`); got != "ct-jellyfin" {
		t.Errorf("jellyfin ciphertext should be copied verbatim, got %q", got)
	}

	// The hardcoded Jellyfin = Movies/Series scoping is preserved exactly.
	modes := scalarString(t, sqlDB, `
		SELECT GROUP_CONCAT(mode, ',') FROM (
			SELECT mode FROM service_connection_modes m
			JOIN service_connections c ON c.id = m.connection_id
			WHERE c.provider = 'jellyfin' ORDER BY mode ASC)`)
	if modes != "movies,series" {
		t.Errorf("jellyfin should be assigned movies+series, got %q", modes)
	}
	if n := scalarInt(t, sqlDB, `
		SELECT COUNT(*) FROM service_connection_modes m
		JOIN service_connections c ON c.id = m.connection_id WHERE c.provider = 'nntp'`); n != 0 {
		t.Errorf("a usenet row is scoped by nothing, got %d mode rows", n)
	}
}

// TestMigration0053_DownIsLossyButValid is the Down-path test the plan requires.
// connections.service is a TEXT PRIMARY KEY, so an install with TWO jellyfin
// rows — the entire point of this feature — cannot be represented by the old
// schema. The Down must restore the lowest-id row per provider and DROP the
// rest rather than blowing up on a PRIMARY KEY violation.
func TestMigration0053_DownIsLossyButValid(t *testing.T) {
	sqlDB := openTo(t, 53)

	// Two jellyfin players plus one usenet subscription whose url has already
	// been normalized away by BackfillUsenetURL (host/port/tls only).
	for _, r := range []struct {
		kind, provider, label, url, host string
		port, tls                        int
		secret                           string
	}{
		{"player", "jellyfin", "Living room", "http://jf-a:8096", "", 0, 0, "ct-a"},
		{"player", "jellyfin", "Basement", "http://jf-b:8096", "", 0, 0, "ct-b"},
		{"usenet", "nntp", "Usenet", "", "news.example.com", 563, 1, "ct-nntp"},
	} {
		if _, err := sqlDB.Exec(`
			INSERT INTO service_connections (kind, provider, label, url, host, port, tls, secret_encrypted)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			r.kind, r.provider, r.label, r.url, r.host, r.port, r.tls, r.secret); err != nil {
			t.Fatalf("seeding %s row: %v", r.provider, err)
		}
	}

	if err := goose.DownTo(sqlDB, "migrations", 52); err != nil {
		t.Fatalf("goose DownTo 52 (must not violate connections' PRIMARY KEY): %v", err)
	}

	if n := scalarInt(t, sqlDB, `SELECT COUNT(*) FROM connections WHERE service = 'jellyfin'`); n != 1 {
		t.Fatalf("exactly one jellyfin row can be restored, got %d", n)
	}
	if got := scalarString(t, sqlDB, `SELECT url FROM connections WHERE service = 'jellyfin'`); got != "http://jf-a:8096" {
		t.Errorf("the lowest-id jellyfin row should win, got %q", got)
	}
	// The nntp URL must be RECONSTRUCTED from host/port/tls: usenet.ParseURL is
	// what boots the engine, and an empty url would leave a rolled-back install
	// non-functional.
	if got := scalarString(t, sqlDB, `SELECT url FROM connections WHERE service = 'nntp'`); got != "nntps://news.example.com:563" {
		t.Errorf("nntp url should be reconstructed from host/port/tls, got %q", got)
	}

	// Re-applying is clean — no residue blocks re-migration.
	if err := goose.UpTo(sqlDB, "migrations", 53); err != nil {
		t.Fatalf("goose UpTo 53 (re-apply): %v", err)
	}
	if n := scalarInt(t, sqlDB, `SELECT COUNT(*) FROM service_connections`); n != 2 {
		t.Errorf("re-Up should move the 2 restored rows back, got %d", n)
	}
}

// TestMigration0054_GrabsRetry_UpDown proves migration 0054's four retry
// columns apply, roll back, and re-apply cleanly.
func TestMigration0054_GrabsRetry_UpDown(t *testing.T) {
	sqlDB := openTo(t, 54)

	cols := []string{"download_url_encrypted", "retry_after", "retry_count", "retry_reason"}
	for _, c := range cols {
		if !columnExists(t, sqlDB, "grabs", c) {
			t.Errorf("after Up, grabs.%s should exist", c)
		}
	}

	if err := goose.DownTo(sqlDB, "migrations", 53); err != nil {
		t.Fatalf("goose DownTo 53: %v", err)
	}
	for _, c := range cols {
		if columnExists(t, sqlDB, "grabs", c) {
			t.Errorf("after Down, grabs.%s should be dropped", c)
		}
	}

	if err := goose.UpTo(sqlDB, "migrations", 54); err != nil {
		t.Fatalf("goose UpTo 54 (re-apply): %v", err)
	}
	for _, c := range cols {
		if !columnExists(t, sqlDB, "grabs", c) {
			t.Errorf("after re-Up, grabs.%s should exist again", c)
		}
	}
}
