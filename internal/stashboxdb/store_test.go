package stashboxdb

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"testing"

	"github.com/labbersanon/sakms/internal/connections"
	"github.com/labbersanon/sakms/internal/db"
	"github.com/labbersanon/sakms/internal/secrets"
)

// newTestStore builds a Store against a real, freshly migrated SQLite file and
// a real secrets.Store — actual encryption and actual SQL, matching
// internal/connections' own test convention. It returns the paired
// connections.Store too, since the seeded rows' keys live there via
// secret_ref and half of what this package does is route between the two.
func newTestStore(t *testing.T) (*Store, *connections.Store, *sql.DB) {
	t.Helper()
	sqlDB, err := db.Open(filepath.Join(t.TempDir(), "sakms.db"))
	if err != nil {
		t.Fatalf("opening db: %v", err)
	}
	t.Cleanup(func() { sqlDB.Close() })

	secretStore, err := secrets.New(make([]byte, 32))
	if err != nil {
		t.Fatalf("building secret store: %v", err)
	}
	return New(sqlDB, secretStore), connections.New(sqlDB, secretStore), sqlDB
}

// connGetFor adapts a connections.Store into the injected ConnGet func,
// reporting a missing connection as ("", nil) — the "not configured" case.
func connGetFor(cs *connections.Store) ConnGet {
	return func(ctx context.Context, service string) (string, error) {
		conn, err := cs.Get(ctx, service)
		if errors.Is(err, connections.ErrNotFound) {
			return "", nil
		}
		if err != nil {
			return "", err
		}
		return conn.APIKey, nil
	}
}

func connSetFor(cs *connections.Store) ConnSet {
	return func(ctx context.Context, service string, apiKey *string) error {
		return cs.UpsertPreservingSecret(ctx, service, "", "", apiKey)
	}
}

func connDeleteFor(cs *connections.Store) ConnDelete {
	return func(ctx context.Context, service string) error {
		return cs.Delete(ctx, service)
	}
}

func strptr(s string) *string { return &s }

func TestList_SeedsStashDBThenFansDBInPriorityOrder(t *testing.T) {
	s, cs, _ := newTestStore(t)
	ctx := context.Background()

	got, err := s.List(ctx, connGetFor(cs))
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected the 2 seeded rows, got %d: %+v", len(got), got)
	}
	if got[0].Name != "stashdb" || got[1].Name != "fansdb" {
		t.Errorf("expected [stashdb fansdb] in priority order, got [%s %s]", got[0].Name, got[1].Name)
	}
	if got[0].Endpoint != "https://stashdb.org/graphql" || got[1].Endpoint != "https://fansdb.cc/graphql" {
		t.Errorf("unexpected seeded endpoints: %q / %q", got[0].Endpoint, got[1].Endpoint)
	}
	if got[0].FansiteOnly {
		t.Error("stashdb must not be fansite-only")
	}
	if !got[1].FansiteOnly {
		t.Error("fansdb must seed fansite_only = 1 (today's FansDB text-search gate)")
	}
	if got[0].SecretRef != "stashdb" || got[1].SecretRef != "fansdb" {
		t.Errorf("seeded rows must point at their connections secret, got %q / %q", got[0].SecretRef, got[1].SecretRef)
	}
}

// AC14: an operator with an existing stashdb key sees it resolved through the
// new registry with no re-entry — the seeded row's api_key_encrypted stays ”
// and the key is read from `connections`.
func TestList_ResolvesSeededKeyFromConnectionsWithoutMovingIt(t *testing.T) {
	s, cs, sqlDB := newTestStore(t)
	ctx := context.Background()

	if err := cs.Upsert(ctx, "stashdb", "", "already-configured-key"); err != nil {
		t.Fatalf("seeding the existing stashdb connection: %v", err)
	}

	got, err := s.List(ctx, connGetFor(cs))
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if got[0].APIKey != "already-configured-key" {
		t.Errorf("expected the key to resolve from connections, got %q", got[0].APIKey)
	}

	var inline string
	if err := sqlDB.QueryRow(`SELECT api_key_encrypted FROM stashbox_databases WHERE name = 'stashdb'`).Scan(&inline); err != nil {
		t.Fatalf("reading the seeded row's inline key column: %v", err)
	}
	if inline != "" {
		t.Errorf("no key blob may be copied into the registry for a seeded row, got %q", inline)
	}
}

func TestList_SkipsDisabledRows(t *testing.T) {
	s, cs, _ := newTestStore(t)
	ctx := context.Background()

	enabled := false
	if err := s.Update(ctx, 2, UpdateInput{Enabled: &enabled}, connSetFor(cs)); err != nil {
		t.Fatalf("disabling fansdb: %v", err)
	}
	got, err := s.List(ctx, connGetFor(cs))
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 || got[0].Name != "stashdb" {
		t.Errorf("expected only the enabled stashdb row, got %+v", got)
	}

	all, err := s.ListSummaries(ctx, connGetFor(cs))
	if err != nil {
		t.Fatalf("ListSummaries: %v", err)
	}
	if len(all) != 2 {
		t.Errorf("ListSummaries must still return the disabled row, got %d", len(all))
	}
}

func TestCreate_RoundTripsInlineKeyAndAppendsToTheCascade(t *testing.T) {
	s, cs, _ := newTestStore(t)
	ctx := context.Background()

	created, err := s.Create(ctx, "mybox", "https://box.example/graphql", "inline-key")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.Priority != 2 {
		t.Errorf("a new database must land last in the cascade (priority 2), got %d", created.Priority)
	}

	got, err := s.List(ctx, connGetFor(cs))
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 3 || got[2].Name != "mybox" {
		t.Fatalf("expected mybox last, got %+v", got)
	}
	if got[2].APIKey != "inline-key" {
		t.Errorf("expected the inline key to decrypt, got %q", got[2].APIKey)
	}
	if got[2].SecretRef != "" {
		t.Errorf("an operator-added row must store its key inline (secret_ref ''), got %q", got[2].SecretRef)
	}
}

// AC16: the cap is enforced server-side, in the store, not only by a disabled
// button.
func TestCreate_RejectsTheSixthDatabase(t *testing.T) {
	s, _, _ := newTestStore(t)
	ctx := context.Background()

	for _, name := range []string{"three", "four", "five"} {
		if _, err := s.Create(ctx, name, "https://"+name+".example/graphql", "k"); err != nil {
			t.Fatalf("Create(%s): %v", name, err)
		}
	}
	if _, err := s.Create(ctx, "six", "https://six.example/graphql", "k"); !errors.Is(err, ErrCapReached) {
		t.Fatalf("expected ErrCapReached on the 6th, got %v", err)
	}

	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM stashbox_databases`).Scan(&count); err != nil {
		t.Fatalf("counting: %v", err)
	}
	if count != MaxDatabases {
		t.Errorf("the rejected create must write no row, got %d rows", count)
	}
}

// Run with -race: two goroutines both attempting the 5→6 create. Exactly one
// may pass. Honest scope (ralplan §4): internal/db opens the pool with
// SetMaxOpenConns(1), so the two Creates serialize at the POOL before either
// reaches SQLite's locking layer — this asserts the cap guarantee holds, not
// that BEGIN IMMEDIATE specifically is what upholds it.
func TestCreate_ConcurrentCreatesCannotBothPassTheCap(t *testing.T) {
	s, _, _ := newTestStore(t)
	ctx := context.Background()

	for _, name := range []string{"three", "four"} {
		if _, err := s.Create(ctx, name, "https://"+name+".example/graphql", "k"); err != nil {
			t.Fatalf("Create(%s): %v", name, err)
		}
	}

	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		okN  int
		errN int
	)
	for _, name := range []string{"racer-a", "racer-b"} {
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			_, err := s.Create(ctx, name, "https://"+name+".example/graphql", "k")
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				okN++
			} else {
				errN++
			}
		}(name)
	}
	wg.Wait()

	if okN != 1 || errN != 1 {
		t.Errorf("expected exactly one create to pass the cap, got %d ok / %d rejected", okN, errN)
	}
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM stashbox_databases`).Scan(&count); err != nil {
		t.Fatalf("counting: %v", err)
	}
	if count != MaxDatabases {
		t.Errorf("expected exactly %d rows after the race, got %d", MaxDatabases, count)
	}
}

func TestCreate_RejectsReservedDuplicateAndBadEndpointNames(t *testing.T) {
	s, _, _ := newTestStore(t)
	ctx := context.Background()

	if _, err := s.Create(ctx, "tpdb", "https://x.example/graphql", "k"); !errors.Is(err, ErrNameReserved) {
		t.Errorf(`expected ErrNameReserved for "tpdb", got %v`, err)
	}
	if _, err := s.Create(ctx, "StashDB", "https://x.example/graphql", "k"); !errors.Is(err, ErrNameTaken) {
		t.Errorf("expected ErrNameTaken for a case-insensitive duplicate, got %v", err)
	}
	if _, err := s.Create(ctx, "", "https://x.example/graphql", "k"); !errors.Is(err, ErrNameRequired) {
		t.Errorf("expected ErrNameRequired for a blank name, got %v", err)
	}
	if _, err := s.Create(ctx, "ok", "not-a-url", "k"); !errors.Is(err, ErrInvalidEndpoint) {
		t.Errorf("expected ErrInvalidEndpoint, got %v", err)
	}
	if _, err := s.Create(ctx, "ok", "https://x.example/graphql", ""); !errors.Is(err, ErrKeyRequired) {
		t.Errorf("expected ErrKeyRequired, got %v", err)
	}
}

// AC17 / §2.8: the name-reuse tombstone blocks binding a name that has tracked
// library_scenes history to a different row, on BOTH Create and rename — while
// a row keeping its OWN name on an unrelated edit stays allowed.
func TestNameReuseTombstone_BlocksRebindingAHauntedName(t *testing.T) {
	s, cs, sqlDB := newTestStore(t)
	ctx := context.Background()

	if _, err := sqlDB.Exec(`
		INSERT INTO library_scenes (box, scene_id, root_folder_path) VALUES ('ghostbox', 'abc', '/adult')
	`); err != nil {
		t.Fatalf("seeding a tracked scene: %v", err)
	}

	if _, err := s.Create(ctx, "ghostbox", "https://other.example/graphql", "k"); !errors.Is(err, ErrNameHaunted) {
		t.Errorf("expected ErrNameHaunted on Create, got %v", err)
	}
	if err := s.Update(ctx, 1, UpdateInput{Name: strptr("ghostbox")}, connSetFor(cs)); !errors.Is(err, ErrNameHaunted) {
		t.Errorf("expected ErrNameHaunted on rename, got %v", err)
	}

	// Own-name carve-out: stashdb has its own tracked scene, and an unrelated
	// edit that keeps the name must still be allowed.
	if _, err := sqlDB.Exec(`
		INSERT INTO library_scenes (box, scene_id, root_folder_path) VALUES ('stashdb', 'def', '/adult')
	`); err != nil {
		t.Fatalf("seeding stashdb's own tracked scene: %v", err)
	}
	if err := s.Update(ctx, 1, UpdateInput{Endpoint: strptr("https://mirror.example/graphql")}, connSetFor(cs)); err != nil {
		t.Errorf("an unrelated edit that keeps the row's own name must be allowed, got %v", err)
	}
}

// AC3b: a seeded row is a peer — renameable, re-endpointable, disable-able and
// deletable through the same paths as an operator-added one. And the load-
// bearing half: a rename moves only `name`, never `secret_ref`, so the key
// stays resolvable from connections["stashdb"].
func TestUpdate_SeededRowIsFullyEditableAndKeepsItsSecretRef(t *testing.T) {
	s, cs, _ := newTestStore(t)
	ctx := context.Background()

	if err := cs.Upsert(ctx, "stashdb", "", "seeded-key"); err != nil {
		t.Fatalf("seeding the stashdb connection: %v", err)
	}
	if err := s.Update(ctx, 1, UpdateInput{
		Name:     strptr("MyStash"),
		Endpoint: strptr("https://mystash.example/graphql"),
	}, connSetFor(cs)); err != nil {
		t.Fatalf("renaming + re-endpointing the seeded row: %v", err)
	}

	got, err := s.List(ctx, connGetFor(cs))
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if got[0].Name != "MyStash" || got[0].Endpoint != "https://mystash.example/graphql" {
		t.Errorf("expected the rename + re-endpoint to stick, got %+v", got[0])
	}
	if got[0].SecretRef != "stashdb" {
		t.Errorf("a rename must not touch secret_ref, got %q", got[0].SecretRef)
	}
	if got[0].APIKey != "seeded-key" {
		t.Errorf("the seeded key must still resolve from connections after a rename, got %q", got[0].APIKey)
	}
}

// The three-state secret rule, routed by secret_ref: a seeded row's key write
// lands in `connections`, an inline row's in this table, and nil preserves.
func TestUpdate_ThreeStateKeyRoutesBySecretRef(t *testing.T) {
	s, cs, sqlDB := newTestStore(t)
	ctx := context.Background()

	if err := cs.Upsert(ctx, "stashdb", "", "old-key"); err != nil {
		t.Fatalf("seeding: %v", err)
	}

	// nil = preserve.
	if err := s.Update(ctx, 1, UpdateInput{Priority: intptr(0)}, connSetFor(cs)); err != nil {
		t.Fatalf("Update (preserve): %v", err)
	}
	conn, err := cs.Get(ctx, "stashdb")
	if err != nil || conn.APIKey != "old-key" {
		t.Fatalf("a nil key must preserve the stored secret, got %+v / %v", conn, err)
	}

	// non-empty = replace, written back to connections (not inline).
	if err := s.Update(ctx, 1, UpdateInput{APIKey: strptr("new-key")}, connSetFor(cs)); err != nil {
		t.Fatalf("Update (replace): %v", err)
	}
	conn, err = cs.Get(ctx, "stashdb")
	if err != nil || conn.APIKey != "new-key" {
		t.Fatalf("expected the new key in connections, got %+v / %v", conn, err)
	}
	var inline string
	if err := sqlDB.QueryRow(`SELECT api_key_encrypted FROM stashbox_databases WHERE id = 1`).Scan(&inline); err != nil {
		t.Fatalf("reading the inline column: %v", err)
	}
	if inline != "" {
		t.Errorf("a secret_ref row's key must never be written inline, got %q", inline)
	}

	// An inline row's key writes to this table instead.
	created, err := s.Create(ctx, "mybox", "https://box.example/graphql", "first")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := s.Update(ctx, created.ID, UpdateInput{APIKey: strptr("second")}, connSetFor(cs)); err != nil {
		t.Fatalf("Update (inline): %v", err)
	}
	got, err := s.Get(ctx, created.ID, connGetFor(cs))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.APIKey != "second" {
		t.Errorf("expected the replaced inline key, got %q", got.APIKey)
	}
}

func TestDelete_SeededRowAlsoClearsThePairedConnectionsSecret(t *testing.T) {
	s, cs, _ := newTestStore(t)
	ctx := context.Background()

	if err := cs.Upsert(ctx, "fansdb", "", "fans-key"); err != nil {
		t.Fatalf("seeding: %v", err)
	}
	if err := s.Delete(ctx, 2, connDeleteFor(cs)); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := cs.Get(ctx, "fansdb"); !errors.Is(err, connections.ErrNotFound) {
		t.Errorf("deleting a secret_ref row must clear its paired connections secret, got %v", err)
	}
	got, err := s.List(ctx, connGetFor(cs))
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 {
		t.Errorf("expected 1 remaining database, got %d", len(got))
	}
}

func TestDelete_UnknownIDIsNotFound(t *testing.T) {
	s, cs, _ := newTestStore(t)
	if err := s.Delete(context.Background(), 999, connDeleteFor(cs)); !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestReorder_RewritesPriorityAndRejectsPartialLists(t *testing.T) {
	s, cs, _ := newTestStore(t)
	ctx := context.Background()

	if err := s.Reorder(ctx, []int64{2, 1}); err != nil {
		t.Fatalf("Reorder: %v", err)
	}
	got, err := s.List(ctx, connGetFor(cs))
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if got[0].Name != "fansdb" || got[1].Name != "stashdb" {
		t.Errorf("expected the reversed cascade, got [%s %s]", got[0].Name, got[1].Name)
	}

	if err := s.Reorder(ctx, []int64{1}); err == nil {
		t.Error("a partial reorder must be rejected rather than leaving a stale priority")
	}
	if err := s.Reorder(ctx, []int64{1, 1}); err == nil {
		t.Error("a duplicated id must be rejected")
	}
}

func intptr(i int) *int { return &i }
