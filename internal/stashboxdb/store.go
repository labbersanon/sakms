// Package stashboxdb persists the admin-configurable registry of stash-box-
// protocol databases (StashDB, FansDB, and up to three more operator-added
// instances speaking the same open protocol) — name + GraphQL endpoint +
// token + cascade priority + the fansite-only gate.
//
// It replaces the hardcoded ["stashdb","fansdb"] literals the identification
// pipeline used to carry. EVERY row is a peer: the two rows migration 0061
// seeds are fully editable, reorderable and deletable, exactly like an
// operator-added one. The single internal carry-over is SecretRef (see
// Database.SecretRef) — the seeded rows' encrypted keys stay in the
// `connections` table so no key blob is ever moved by the migration.
//
// This package deliberately does NOT import internal/connections: the two
// secret-source reads it needs are injected as funcs (connGet / connHasKey)
// by the caller, which keeps the dependency one-directional and lets
// internal/connections stay the app's single safety-critical secret store.
package stashboxdb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// MaxDatabases is the literal cap on configured databases. Enforced in
// Create (inside the count-check transaction), not only by a disabled button.
const MaxDatabases = 5

// ReservedName is the one name a row may never take: "tpdb" is the hardcoded
// TPDB lane appended after every stash-box database, never a row here.
// "stashdb"/"fansdb" are NOT reserved — they are ordinary seeded rows.
const ReservedName = "tpdb"

var (
	// ErrNotFound is returned by Update/Delete/Get when id has no stored row.
	ErrNotFound = errors.New("stashboxdb: no database with that id")
	// ErrCapReached is returned by Create once MaxDatabases rows exist.
	ErrCapReached = fmt.Errorf("stashboxdb: at most %d databases can be configured", MaxDatabases)
	// ErrNameRequired is returned when a name is blank.
	ErrNameRequired = errors.New("stashboxdb: name is required")
	// ErrNameReserved is returned when a name collides with ReservedName.
	ErrNameReserved = errors.New(`stashboxdb: "` + ReservedName + `" is reserved for the built-in TPDB lane`)
	// ErrNameTaken is returned when a name collides with another live row.
	ErrNameTaken = errors.New("stashboxdb: another database already uses that name")
	// ErrNameHaunted is the §2.8 name-reuse tombstone: the name has tracked
	// scenes under it from a different (renamed or deleted) database, so
	// binding it to this row would silently conflate two scene_id namespaces.
	ErrNameHaunted = errors.New("stashboxdb: that name already has tracked library scenes from a different database — choose another name")
	// ErrInvalidEndpoint is returned for a non-http(s) or unparseable endpoint.
	ErrInvalidEndpoint = errors.New("stashboxdb: endpoint must be an http(s) URL")
	// ErrKeyRequired is returned when Create is given no API key.
	ErrKeyRequired = errors.New("stashboxdb: an API key is required")
)

// Encryptor is the same two-method secret interface internal/connections
// defines; internal/secrets satisfies both.
type Encryptor interface {
	Encrypt(plaintext string) (string, error)
	Decrypt(encoded string) (string, error)
}

// Database is one configured stash-box-protocol database, with its key
// already resolved (from `connections` when SecretRef is set, from the
// inline column otherwise).
type Database struct {
	ID          int64
	Name        string // unique; throttle key, MatchResult.Box, library_scenes.box, give-back map key
	Endpoint    string // GraphQL endpoint URL (editable on every row)
	APIKey      string // decrypted; "" means not configured, same as today's missing connection
	Priority    int    // cascade order, ascending (0 = consulted first)
	Enabled     bool
	FansiteOnly bool // gate FUZZY text search behind IsFansiteHinted (seeded true for fansdb)
	// SecretRef is an internal secret-source handle, NOT a user-facing
	// "built-in" tier: it names the `connections` service this row's
	// encrypted key lives under (non-empty only for the two seeded rows).
	// Empty means the key is stored inline in api_key_encrypted. It is
	// invisible to the UI, has ZERO effect on identification, and is never
	// mutated after seed time — a rename changes only Name.
	SecretRef string
}

// Summary is the API-safe redacted shape — never the secret. HasAPIKey and
// KeySuffix reflect the RESOLVED key (connections for a SecretRef row, inline
// otherwise) so the UI masks every row identically and exposes no tier flag.
type Summary struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Endpoint    string `json:"endpoint"`
	Priority    int    `json:"priority"`
	Enabled     bool   `json:"enabled"`
	FansiteOnly bool   `json:"fansiteOnly"`
	HasAPIKey   bool   `json:"hasApiKey"`
	KeySuffix   string `json:"keySuffix,omitempty"`
	UpdatedAt   string `json:"updatedAt"`
}

// UpdateInput is Update's patch body. A nil pointer means "leave this field
// alone"; APIKey additionally carries the three-state secret rule the whole
// app relies on (nil = keep, "" = clear, "x" = set), matching
// connections.Store.UpsertPreservingSecret exactly.
type UpdateInput struct {
	Name        *string
	Endpoint    *string
	Priority    *int
	Enabled     *bool
	FansiteOnly *bool
	APIKey      *string
}

// ConnGet resolves a `connections` service's decrypted key. Injected rather
// than imported so this package never depends on internal/connections. A
// missing connection must be reported as ("", nil), not an error — that is
// the "not configured yet" case, exactly as it is today.
type ConnGet func(ctx context.Context, service string) (string, error)

// ConnSet writes a `connections` service's key with the three-state rule.
// nil preserves whatever is stored.
type ConnSet func(ctx context.Context, service string, apiKey *string) error

// ConnDelete clears a `connections` service entirely.
type ConnDelete func(ctx context.Context, service string) error

// Store persists the registry against a database, encrypting inline keys with
// secrets before they are written.
type Store struct {
	db      *sql.DB
	secrets Encryptor
}

func New(db *sql.DB, secrets Encryptor) *Store {
	return &Store{db: db, secrets: secrets}
}

// List returns ENABLED databases in priority order with their keys resolved:
// a SecretRef row's key comes from connGet, an inline row's from its own
// decrypted column. A SecretRef row whose `connections` row is absent comes
// back with APIKey == "" and the caller treats it as not-configured — the
// same behaviour a missing connection has today.
//
// connGet may be nil (tests, or a caller with no connections store), in which
// case every SecretRef row resolves to an empty key.
func (s *Store) List(ctx context.Context, connGet ConnGet) ([]Database, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, name, endpoint, api_key_encrypted, priority, enabled, fansite_only, secret_ref
		FROM stashbox_databases
		WHERE enabled = true
		ORDER BY priority ASC, id ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("listing stash-box databases: %w", err)
	}

	// Claude 2026-08-04: scan every row BEFORE resolveKey.
	// Reason: internal/db opens SQLite with SetMaxOpenConns(1). Holding
	//   rows open while resolveKey → connections.Store.Get tries to grab
	//   a second conn deadlocks the pool (seen as TestList_* hang).
	// Troubleshooting: if List hangs under -race or in tests that inject
	//   a real connections.Store against the same *sql.DB, check this
	//   two-pass shape first.
	// Review if: the pool ever allows >1 open conn.
	var stored []storedDatabase
	for rows.Next() {
		db, err := scanDatabase(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		stored = append(stored, db)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}

	out := make([]Database, 0, len(stored))
	for _, db := range stored {
		key, err := s.resolveKey(ctx, connGet, db.SecretRef, db.apiKeyEncrypted)
		if err != nil {
			return nil, err
		}
		db.APIKey = key
		out = append(out, db.Database)
	}
	return out, nil
}

// ListSummaries returns ALL databases (including disabled ones) in priority
// order, redacted for the Settings UI. connGet resolves a SecretRef row's key
// only far enough to report HasAPIKey/KeySuffix — the secret itself never
// leaves this function.
func (s *Store) ListSummaries(ctx context.Context, connGet ConnGet) ([]Summary, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, name, endpoint, api_key_encrypted, priority, enabled, fansite_only, secret_ref, updated_at
		FROM stashbox_databases
		ORDER BY priority ASC, id ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("listing stash-box databases: %w", err)
	}

	// Claude 2026-08-04: two-pass scan — same SetMaxOpenConns(1) deadlock
	// Reason as List (resolveKey → connections.Get needs a free conn).
	// Review if: the pool ever allows >1 open conn.
	type summaryRow struct {
		row       storedDatabase
		updatedAt string
	}
	var stored []summaryRow
	for rows.Next() {
		var r summaryRow
		if err := rows.Scan(&r.row.ID, &r.row.Name, &r.row.Endpoint, &r.row.apiKeyEncrypted,
			&r.row.Priority, &r.row.Enabled, &r.row.FansiteOnly, &r.row.SecretRef, &r.updatedAt); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scanning stash-box database: %w", err)
		}
		stored = append(stored, r)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}

	// []Summary{}, not var out []Summary — an empty registry must serialize
	// as [] over the API, not null (same convention as connections.List).
	out := make([]Summary, 0, len(stored))
	for _, r := range stored {
		key, err := s.resolveKey(ctx, connGet, r.row.SecretRef, r.row.apiKeyEncrypted)
		if err != nil {
			return nil, err
		}
		sum := Summary{
			ID: r.row.ID, Name: r.row.Name, Endpoint: r.row.Endpoint, Priority: r.row.Priority,
			Enabled: r.row.Enabled, FansiteOnly: r.row.FansiteOnly, UpdatedAt: r.updatedAt,
		}
		if key != "" {
			sum.HasAPIKey = true
			if len(key) >= 4 {
				sum.KeySuffix = key[len(key)-4:]
			}
		}
		out = append(out, sum)
	}
	return out, nil
}

// Get returns one database by id with its key resolved. ErrNotFound if absent.
func (s *Store) Get(ctx context.Context, id int64, connGet ConnGet) (Database, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, name, endpoint, api_key_encrypted, priority, enabled, fansite_only, secret_ref
		FROM stashbox_databases WHERE id = ?
	`, id)
	stored, err := scanDatabase(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Database{}, ErrNotFound
		}
		return Database{}, err
	}
	key, err := s.resolveKey(ctx, connGet, stored.SecretRef, stored.apiKeyEncrypted)
	if err != nil {
		return Database{}, err
	}
	stored.APIKey = key
	return stored.Database, nil
}

// Create inserts an operator-added database (SecretRef == "", key stored
// inline). It enforces, atomically with the insert: the MaxDatabases cap, a
// name that is non-blank / not ReservedName / unique among live rows / not
// haunted (§2.8 tombstone), a valid http(s) endpoint, and a non-empty key.
// Priority is assigned as max(existing)+1 so a new row lands last in the
// cascade rather than silently jumping ahead of a configured one.
//
// CONCURRENCY: the count-check and the insert run in ONE transaction opened
// with an explicit BEGIN IMMEDIATE (Go's db.BeginTx issues DEFERRED, and the
// prose label alone does nothing), so two concurrent creates can never both
// pass the cap. Note this is future-proofing rather than a load-bearing fix
// today: internal/db opens the pool with SetMaxOpenConns(1), which already
// serializes the two calls before either reaches SQLite's locking layer.
func (s *Store) Create(ctx context.Context, name, endpoint, apiKey string) (Database, error) {
	name = strings.TrimSpace(name)
	endpoint = strings.TrimSpace(endpoint)
	if err := validateName(name); err != nil {
		return Database{}, err
	}
	if err := validateEndpoint(endpoint); err != nil {
		return Database{}, err
	}
	if apiKey == "" {
		return Database{}, ErrKeyRequired
	}
	encrypted, err := s.secrets.Encrypt(apiKey)
	if err != nil {
		return Database{}, fmt.Errorf("encrypting stash-box database key: %w", err)
	}

	conn, err := s.db.Conn(ctx)
	if err != nil {
		return Database{}, fmt.Errorf("acquiring connection: %w", err)
	}
	defer conn.Close()
	// Claude 2026-08-04: BEGIN IMMEDIATE is SQLite-only; PG uses BEGIN + xact advisory lock.
	// Reason: concurrent Create must still serialize the cap check (pool MaxOpen > 1 now).
	// Troubleshooting: "syntax error at or near IMMEDIATE".
	// Review if: Create moves to a single INSERT…ON CONFLICT / exclusion constraint.
	if _, err := conn.ExecContext(ctx, "BEGIN"); err != nil {
		return Database{}, fmt.Errorf("beginning stash-box database create: %w", err)
	}
	if _, err := conn.ExecContext(ctx, "SELECT pg_advisory_xact_lock(872014)"); err != nil {
		_, _ = conn.ExecContext(context.WithoutCancel(ctx), "ROLLBACK")
		return Database{}, fmt.Errorf("locking stash-box database create: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.WithoutCancel(ctx), "ROLLBACK")
		}
	}()

	var count int
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM stashbox_databases`).Scan(&count); err != nil {
		return Database{}, fmt.Errorf("counting stash-box databases: %w", err)
	}
	if count >= MaxDatabases {
		return Database{}, ErrCapReached
	}
	if err := nameFreeInTx(ctx, conn, name, 0); err != nil {
		return Database{}, err
	}

	out := Database{Name: name, Endpoint: endpoint, APIKey: apiKey, Enabled: true}
	row := conn.QueryRowContext(ctx, `
		INSERT INTO stashbox_databases (name, endpoint, api_key_encrypted, priority, enabled, fansite_only, secret_ref)
		VALUES (?, ?, ?, COALESCE((SELECT MAX(priority) + 1 FROM stashbox_databases), 0), true, false, '')
		RETURNING id, priority
	`, name, endpoint, encrypted)
	if err := row.Scan(&out.ID, &out.Priority); err != nil {
		return Database{}, fmt.Errorf("creating stash-box database %q: %w", name, err)
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return Database{}, fmt.Errorf("committing stash-box database create: %w", err)
	}
	committed = true
	return out, nil
}

// Update mutates any subset of name/endpoint/priority/enabled/fansite_only
// and the key. EVERY field is editable on EVERY row — there is no reserved
// tier. A name change runs the same guards as Create (non-blank, not
// ReservedName, unique, not haunted), with a carve-out for the row keeping
// its own current name.
//
// The key write routes by the row's stored SecretRef: a SecretRef row's key
// goes back to `connections` under that handle via connSet (so the seeded
// secret's home never moves), an inline row's to this table. SecretRef itself
// is never mutated. connSet may be nil, in which case a key write against a
// SecretRef row is a no-op rather than an error — the caller simply has no
// connections store wired.
func (s *Store) Update(ctx context.Context, id int64, in UpdateInput, connSet ConnSet) error {
	current, err := s.getStored(ctx, id)
	if err != nil {
		return err
	}

	next := current
	if in.Name != nil {
		name := strings.TrimSpace(*in.Name)
		if err := validateName(name); err != nil {
			return err
		}
		if !strings.EqualFold(name, current.Name) {
			if err := s.nameFree(ctx, name, id); err != nil {
				return err
			}
		}
		next.Name = name
	}
	if in.Endpoint != nil {
		endpoint := strings.TrimSpace(*in.Endpoint)
		if err := validateEndpoint(endpoint); err != nil {
			return err
		}
		next.Endpoint = endpoint
	}
	if in.Priority != nil {
		next.Priority = *in.Priority
	}
	if in.Enabled != nil {
		next.Enabled = *in.Enabled
	}
	if in.FansiteOnly != nil {
		next.FansiteOnly = *in.FansiteOnly
	}

	// The key write happens BEFORE the metadata write for a SecretRef row so a
	// failed secret write can't leave the row renamed with a stale key. For an
	// inline row the two are the same statement anyway.
	if in.APIKey != nil && current.SecretRef != "" {
		if connSet != nil {
			if err := connSet(ctx, current.SecretRef, in.APIKey); err != nil {
				return fmt.Errorf("writing stash-box database key to connections: %w", err)
			}
		}
	} else if in.APIKey != nil {
		encrypted := ""
		if *in.APIKey != "" {
			encrypted, err = s.secrets.Encrypt(*in.APIKey)
			if err != nil {
				return fmt.Errorf("encrypting stash-box database key: %w", err)
			}
		}
		next.apiKeyEncrypted = encrypted
	}

	_, err = s.db.ExecContext(ctx, `
		UPDATE stashbox_databases SET
			name = ?, endpoint = ?, api_key_encrypted = ?, priority = ?, enabled = ?, fansite_only = ?,
			updated_at = sakms_now()
		WHERE id = ?
	`, next.Name, next.Endpoint, next.apiKeyEncrypted, next.Priority, next.Enabled, next.FansiteOnly, id)
	if err != nil {
		return fmt.Errorf("updating stash-box database %d: %w", id, err)
	}
	return nil
}

// Reorder rewrites priority for every id in order, 0-based. ids must be the
// FULL set of stored ids exactly once — a partial list is rejected rather
// than silently leaving unlisted rows at a stale priority.
func (s *Store) Reorder(ctx context.Context, ids []int64) error {
	stored, err := s.allIDs(ctx)
	if err != nil {
		return err
	}
	if len(ids) != len(stored) {
		return fmt.Errorf("stashboxdb: reorder needs all %d database ids, got %d", len(stored), len(ids))
	}
	seen := make(map[int64]bool, len(ids))
	for _, id := range ids {
		if !stored[id] {
			return fmt.Errorf("stashboxdb: reorder references unknown database id %d", id)
		}
		if seen[id] {
			return fmt.Errorf("stashboxdb: reorder lists database id %d twice", id)
		}
		seen[id] = true
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beginning stash-box database reorder: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op once Commit succeeds
	for i, id := range ids {
		if _, err := tx.ExecContext(ctx, `
			UPDATE stashbox_databases SET priority = ?,
				updated_at = sakms_now()
			WHERE id = ?
		`, i, id); err != nil {
			return fmt.Errorf("reordering stash-box database %d: %w", id, err)
		}
	}
	return tx.Commit()
}

// Delete removes ANY database — including a seeded one, there is no reserved
// tier. For a SecretRef row it also clears the paired `connections` secret via
// connDelete so no orphaned secret lingers. Already-tracked scenes keep their
// stored box string: give-back to this database stops, but dedup grouping by
// that string still works (§2.8, accepted for pre-alpha).
func (s *Store) Delete(ctx context.Context, id int64, connDelete ConnDelete) error {
	current, err := s.getStored(ctx, id)
	if err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM stashbox_databases WHERE id = ?`, id); err != nil {
		return fmt.Errorf("deleting stash-box database %d: %w", id, err)
	}
	if current.SecretRef != "" && connDelete != nil {
		if err := connDelete(ctx, current.SecretRef); err != nil {
			return fmt.Errorf("clearing the paired connections secret for %q: %w", current.SecretRef, err)
		}
	}
	return nil
}

// --- internals -------------------------------------------------------------

// storedDatabase is Database plus the still-encrypted key column, so the
// scan helpers can be shared between the resolve-key and redact paths.
type storedDatabase struct {
	Database
	apiKeyEncrypted string
}

// rowScanner unifies *sql.Row and *sql.Rows for scanDatabase.
type rowScanner interface {
	Scan(dest ...any) error
}

func scanDatabase(sc rowScanner) (storedDatabase, error) {
	var row storedDatabase
	if err := sc.Scan(&row.ID, &row.Name, &row.Endpoint, &row.apiKeyEncrypted,
		&row.Priority, &row.Enabled, &row.FansiteOnly, &row.SecretRef); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return storedDatabase{}, err
		}
		return storedDatabase{}, fmt.Errorf("scanning stash-box database: %w", err)
	}
	return row, nil
}

func (s *Store) getStored(ctx context.Context, id int64) (storedDatabase, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, name, endpoint, api_key_encrypted, priority, enabled, fansite_only, secret_ref
		FROM stashbox_databases WHERE id = ?
	`, id)
	stored, err := scanDatabase(row)
	if errors.Is(err, sql.ErrNoRows) {
		return storedDatabase{}, ErrNotFound
	}
	return stored, err
}

// resolveKey routes the secret read by SecretRef: a seeded row's key comes
// from `connections`, an inline row's from its own decrypted column.
func (s *Store) resolveKey(ctx context.Context, connGet ConnGet, secretRef, encrypted string) (string, error) {
	if secretRef != "" {
		if connGet == nil {
			return "", nil
		}
		key, err := connGet(ctx, secretRef)
		if err != nil {
			return "", fmt.Errorf("resolving the stored key for %q: %w", secretRef, err)
		}
		return key, nil
	}
	if encrypted == "" {
		return "", nil
	}
	key, err := s.secrets.Decrypt(encrypted)
	if err != nil {
		return "", fmt.Errorf("decrypting a stash-box database key: %w", err)
	}
	return key, nil
}

func (s *Store) allIDs(ctx context.Context) (map[int64]bool, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM stashbox_databases`)
	if err != nil {
		return nil, fmt.Errorf("listing stash-box database ids: %w", err)
	}
	defer rows.Close()
	out := map[int64]bool{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scanning a stash-box database id: %w", err)
		}
		out[id] = true
	}
	return out, rows.Err()
}

func validateName(name string) error {
	if name == "" {
		return ErrNameRequired
	}
	if strings.EqualFold(name, ReservedName) {
		return ErrNameReserved
	}
	return nil
}

// validateEndpoint requires a parseable absolute http(s) URL with a host —
// a bad endpoint is caught at save time rather than as a mystery zero-match
// lane on the next Scan.
func validateEndpoint(endpoint string) error {
	if endpoint == "" {
		return ErrInvalidEndpoint
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return ErrInvalidEndpoint
	}
	return nil
}

// queryRower unifies *sql.DB and *sql.Conn for the name guards, so Create can
// run them inside its BEGIN IMMEDIATE transaction and Update outside one.
type queryRower interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func (s *Store) nameFree(ctx context.Context, name string, excludeID int64) error {
	return nameFreeInTx(ctx, s.db, name, excludeID)
}

// nameFreeInTx applies both name guards: uniqueness among live rows, and the
// §2.8 name-reuse tombstone. The tombstone's rationale is silent data
// conflation, not tidiness — rebinding a name that has `library_scenes` history
// to a DIFFERENT endpoint would merge two instances' scene_id namespaces with
// no error anywhere. `library_scenes` IS the tombstone; there is no separate
// retired-names table. Note the deliberate benign false positive: because
// library_scenes stores only the box name and never an endpoint, restoring an
// identical database after an accidental delete is indistinguishable from a
// rebind and is likewise refused — choose a new name (§5-S5).
func nameFreeInTx(ctx context.Context, q queryRower, name string, excludeID int64) error {
	var taken int
	if err := q.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM stashbox_databases WHERE lower(name) = lower(?) AND id <> ?`,
		name, excludeID).Scan(&taken); err != nil {
		return fmt.Errorf("checking stash-box database name %q: %w", name, err)
	}
	if taken > 0 {
		return ErrNameTaken
	}

	// The row keeping its OWN current name is the carve-out: excludeID's
	// current name is compared inside the query so an unrelated edit (endpoint,
	// priority) never trips its own history.
	var haunted int
	if err := q.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM library_scenes
		WHERE lower(box) = lower(?)
		  AND lower(box) <> lower(COALESCE((SELECT name FROM stashbox_databases WHERE id = ?), ''))
	`, name, excludeID).Scan(&haunted); err != nil {
		return fmt.Errorf("checking tracked scenes for stash-box database name %q: %w", name, err)
	}
	if haunted > 0 {
		return ErrNameHaunted
	}
	return nil
}
