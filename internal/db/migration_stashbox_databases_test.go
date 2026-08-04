package db

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite"

	"github.com/labbersanon/sakms/internal/secrets"
)

// TestMigration0061StashBoxDatabases proves the registry migration is a pure
// metadata INSERT: a pre-0061 database whose operator already had encrypted
// stashdb AND fansdb connection keys still decrypts BOTH identically after
// the migration (AC14 — zero key re-entry), and the two seeded rows point at
// those untouched `connections` rows via secret_ref rather than carrying a
// copied key blob. Pins to explicit versions rather than goose.Up/Down so the
// assertions stay about 0061 specifically.
func TestMigration0061StashBoxDatabases(t *testing.T) {
	sqlDB, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "sakms.db"))
	if err != nil {
		t.Fatalf("opening db: %v", err)
	}
	defer sqlDB.Close()
	sqlDB.SetMaxOpenConns(1)

	goose.SetBaseFS(migrations)
	if err := goose.SetDialect("sqlite3"); err != nil {
		t.Fatalf("setting dialect: %v", err)
	}

	// Up to exactly 0060: the registry table must not exist yet.
	if err := goose.UpTo(sqlDB, "migrations", 60); err != nil {
		t.Fatalf("goose UpTo 60: %v", err)
	}
	if tableExists(t, sqlDB, "stashbox_databases") {
		t.Fatal("stashbox_databases should not exist before 0061")
	}

	// Seed the real-operator case: two already-configured, already-encrypted
	// stash-box keys living in `connections` exactly as they do in production.
	secretStore, err := secrets.New(make([]byte, 32))
	if err != nil {
		t.Fatalf("building secret store: %v", err)
	}
	seeded := map[string]string{"stashdb": "live-stashdb-key", "fansdb": "live-fansdb-key"}
	for service, plaintext := range seeded {
		encrypted, err := secretStore.Encrypt(plaintext)
		if err != nil {
			t.Fatalf("encrypting the %s key: %v", service, err)
		}
		if _, err := sqlDB.Exec(
			`INSERT INTO connections (service, url, username, api_key_encrypted) VALUES (?, '', '', ?)`,
			service, encrypted,
		); err != nil {
			t.Fatalf("seeding the %s connection: %v", service, err)
		}
	}

	if err := goose.UpTo(sqlDB, "migrations", 61); err != nil {
		t.Fatalf("goose UpTo 61: %v", err)
	}
	if !tableExists(t, sqlDB, "stashbox_databases") {
		t.Fatal("after Up, stashbox_databases should exist")
	}

	// (a) Both connection rows are untouched and still decrypt identically.
	for service, want := range seeded {
		var encrypted string
		if err := sqlDB.QueryRow(
			`SELECT api_key_encrypted FROM connections WHERE service = ?`, service).Scan(&encrypted); err != nil {
			t.Fatalf("reading the %s connection after the migration: %v", service, err)
		}
		got, err := secretStore.Decrypt(encrypted)
		if err != nil {
			t.Fatalf("decrypting the %s key after the migration: %v", service, err)
		}
		if got != want {
			t.Errorf("the %s key changed across the migration: got %q, want %q", service, got, want)
		}
	}

	// (b) Two seeded rows exist with no copied key blob, the right endpoints,
	//     fansdb's fansite gate, and priorities 0/1 (today's cascade order).
	rows, err := sqlDB.Query(`
		SELECT name, endpoint, api_key_encrypted, priority, enabled, fansite_only, secret_ref
		FROM stashbox_databases ORDER BY priority
	`)
	if err != nil {
		t.Fatalf("listing the seeded rows: %v", err)
	}
	defer rows.Close()

	type seedRow struct {
		name, endpoint, inlineKey, secretRef string
		priority                             int
		enabled, fansiteOnly                 bool
	}
	var got []seedRow
	for rows.Next() {
		var r seedRow
		if err := rows.Scan(&r.name, &r.endpoint, &r.inlineKey, &r.priority, &r.enabled, &r.fansiteOnly, &r.secretRef); err != nil {
			t.Fatalf("scanning a seeded row: %v", err)
		}
		got = append(got, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterating the seeded rows: %v", err)
	}

	want := []seedRow{
		{name: "stashdb", endpoint: "https://stashdb.org/graphql", priority: 0, enabled: true, fansiteOnly: false, secretRef: "stashdb"},
		{name: "fansdb", endpoint: "https://fansdb.cc/graphql", priority: 1, enabled: true, fansiteOnly: true, secretRef: "fansdb"},
	}
	if len(got) != len(want) {
		t.Fatalf("expected %d seeded rows, got %d: %+v", len(want), len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("seeded row %d: got %+v, want %+v", i, got[i], want[i])
		}
	}

	// Down to 0060 drops the table and still leaves the connections intact.
	if err := goose.DownTo(sqlDB, "migrations", 60); err != nil {
		t.Fatalf("goose DownTo 60: %v", err)
	}
	if tableExists(t, sqlDB, "stashbox_databases") {
		t.Fatal("after Down, stashbox_databases should be gone")
	}
	var remaining int
	if err := sqlDB.QueryRow(`SELECT COUNT(*) FROM connections`).Scan(&remaining); err != nil {
		t.Fatalf("counting connections after the rollback: %v", err)
	}
	if remaining != len(seeded) {
		t.Errorf("the rollback must not touch connections, got %d rows", remaining)
	}

	// Up again re-applies cleanly with no residue from the rollback.
	if err := goose.UpTo(sqlDB, "migrations", 61); err != nil {
		t.Fatalf("goose UpTo 61 (re-apply): %v", err)
	}
	var seedCount int
	if err := sqlDB.QueryRow(`SELECT COUNT(*) FROM stashbox_databases`).Scan(&seedCount); err != nil {
		t.Fatalf("counting the re-applied seed rows: %v", err)
	}
	if seedCount != 2 {
		t.Errorf("expected exactly 2 seeded rows after the re-apply, got %d", seedCount)
	}
}

// TestMigration0061SeedsCleanlyOnAFreshInstall covers the no-prior-connection
// case: the seeded rows still appear, their keys simply resolve empty (=
// not configured), which is exactly how a never-configured stash-box behaves
// today.
func TestMigration0061SeedsCleanlyOnAFreshInstall(t *testing.T) {
	sqlDB, err := Open(filepath.Join(t.TempDir(), "sakms.db"))
	if err != nil {
		t.Fatalf("opening db: %v", err)
	}
	defer sqlDB.Close()

	var count int
	if err := sqlDB.QueryRow(`SELECT COUNT(*) FROM stashbox_databases WHERE api_key_encrypted = ''`).Scan(&count); err != nil {
		t.Fatalf("counting the fresh-install seed rows: %v", err)
	}
	if count != 2 {
		t.Errorf("expected 2 keyless seeded rows on a fresh install, got %d", count)
	}
	if err := sqlDB.QueryRow(`SELECT COUNT(*) FROM connections`).Scan(&count); err != nil {
		t.Fatalf("counting connections: %v", err)
	}
	if count != 0 {
		t.Errorf("the migration must create no connections rows, got %d", count)
	}
}
