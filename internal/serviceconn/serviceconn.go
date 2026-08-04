// Package serviceconn persists the shared multi-connection registry: the
// service classes an operator can configure MORE THAN ONE of — Usenet
// subscriptions and media players (Jellyfin/Emby/Plex).
//
// It does not replace internal/connections, it splits it. The `connections`
// table's PRIMARY KEY(service) shape is still correct for the singleton
// services (TMDB, TVDB, StashDB, FansDB, TPDB, Trakt, AI), which stay there;
// migration 0053 moves exactly two rows out — nntp and jellyfin — into
// `service_connections`, where a row has an integer id and there may be many
// per provider.
//
// Deliberately NOT importing internal/mode: internal/mode is what imports THIS
// package (mode.Build resolves a Session's players through PlayersForMode), so
// a mode.Mode parameter here would be an import cycle. Modes are plain strings
// validated against the same fixed three-value enum.
package serviceconn

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// ErrNotFound is returned when no registry row exists with the given id.
var ErrNotFound = errors.New("serviceconn: no connection with that id")

// Validation sentinels. Each names one fixed-enum or shape invariant enforced
// in the Store, not just in the UI — a row that violates one could never be
// built into a working client.
var (
	ErrInvalidKind     = errors.New("serviceconn: kind must be usenet or player")
	ErrInvalidProvider = errors.New("serviceconn: unknown provider")
	ErrProviderKind    = errors.New("serviceconn: provider does not belong to that kind")
	ErrInvalidMode     = errors.New("serviceconn: mode must be movies, series or adult")
	ErrModesNotPlayer  = errors.New("serviceconn: only player connections can be assigned to modes")
	ErrURLRequired     = errors.New("serviceconn: a player connection needs a url")
	ErrHostRequired    = errors.New("serviceconn: a usenet connection needs a host and port")
)

// Kind is the service class a registry row belongs to. Fixed enum — this is
// not a generic "add any connection type" framework.
type Kind string

const (
	KindUsenet Kind = "usenet"
	KindPlayer Kind = "player"
)

// Provider is the concrete backend a row talks to.
type Provider string

const (
	ProviderNNTP     Provider = "nntp"
	ProviderJellyfin Provider = "jellyfin"
	ProviderEmby     Provider = "emby"
	ProviderPlex     Provider = "plex"
)

// providerKinds fixes which providers belong to which kind, so a "usenet
// jellyfin" row can never be stored.
var providerKinds = map[Provider]Kind{
	ProviderNNTP:     KindUsenet,
	ProviderJellyfin: KindPlayer,
	ProviderEmby:     KindPlayer,
	ProviderPlex:     KindPlayer,
}

var validKinds = map[Kind]bool{KindUsenet: true, KindPlayer: true}

// validModes mirrors internal/mode's three fixed modes as plain strings — see
// the package doc for why this package must not import internal/mode.
var validModes = map[string]bool{"movies": true, "series": true, "adult": true}

// Connection is one registry row with its secret already decrypted. Never
// round-tripped through the HTTP API with the secret intact — see Summary.
//
// The shape fields are split by kind: URL is player-shaped, Host/Port/TLS/
// MaxConns are usenet-shaped, and the cross-shape fields are left zero.
// Username and Secret apply to both.
type Connection struct {
	ID        int64
	Kind      Kind
	Provider  Provider
	Label     string
	Enabled   bool
	SortOrder int

	// URL is the player base URL ("http://jellyfin:8096").
	URL string

	// Host/Port/TLS/MaxConns describe one NNTP subscription. MaxConns 0 means
	// "unset" — internal/usenet substitutes its own per-server default rather
	// than sizing a pool at zero.
	Host     string
	Port     int
	TLS      bool
	MaxConns int

	Username string
	// Secret is decrypted in-process only and MUST NEVER reach a Summary or
	// any wire DTO. On Create it carries the plaintext to store; Update takes
	// the three-state secret as its own parameter and ignores this field.
	Secret string

	// Modes is the set of modes a player is assigned to (many-to-many, no
	// exclusivity). Always empty for usenet rows.
	Modes []string

	CreatedAt string
	UpdatedAt string
}

// Summary is what's safe to expose over the API: whether a secret is set and
// its last 4 characters, never the secret itself. Mirrors
// connections.Summary's HasAPIKey/KeySuffix convention.
type Summary struct {
	ID           int64    `json:"id"`
	Kind         string   `json:"kind"`
	Provider     string   `json:"provider"`
	Label        string   `json:"label"`
	Enabled      bool     `json:"enabled"`
	SortOrder    int      `json:"sortOrder"`
	URL          string   `json:"url,omitempty"`
	Host         string   `json:"host,omitempty"`
	Port         int      `json:"port,omitempty"`
	TLS          bool     `json:"tls,omitempty"`
	MaxConns     int      `json:"maxConns,omitempty"`
	Username     string   `json:"username,omitempty"`
	HasSecret    bool     `json:"hasSecret"`
	SecretSuffix string   `json:"secretSuffix,omitempty"`
	Modes        []string `json:"modes"`
	CreatedAt    string   `json:"createdAt"`
	UpdatedAt    string   `json:"updatedAt"`
}

// encryptor is the subset of *secrets.Store this package needs — the same
// local 2-method interface internal/rssfeeds and internal/trakt declare, so all
// three are satisfied by one *secrets.Store without importing each other.
type encryptor interface {
	Encrypt(plaintext string) (string, error)
	Decrypt(encoded string) (string, error)
}

// Store persists registry rows, encrypting each row's secret at rest.
type Store struct {
	db      *sql.DB
	secrets encryptor
}

// NewStore builds a Store. secretStore encrypts a connection's secret into
// secret_encrypted before it reaches SQLite and decrypts it on read — same key,
// same convention as connections.Store / rssfeeds.Store.
func NewStore(db *sql.DB, secretStore encryptor) *Store {
	return &Store{db: db, secrets: secretStore}
}

const selectColumns = `id, kind, provider, label, url, host, port, tls, username, max_conns,
	       secret_encrypted, enabled, sort_order, created_at, updated_at`

// validate enforces the fixed enums and the per-kind shape invariants. Applied
// on every write; deliberately NOT on read, because migration 0053 lands the
// legacy nntp row with a url and no host/port until BackfillUsenetURL
// normalizes it (see backfill.go).
func validate(c Connection) error {
	if !validKinds[c.Kind] {
		return fmt.Errorf("%w: %q", ErrInvalidKind, c.Kind)
	}
	kind, ok := providerKinds[c.Provider]
	if !ok {
		return fmt.Errorf("%w: %q", ErrInvalidProvider, c.Provider)
	}
	if kind != c.Kind {
		return fmt.Errorf("%w: %q is a %s provider", ErrProviderKind, c.Provider, kind)
	}
	switch c.Kind {
	case KindPlayer:
		if strings.TrimSpace(c.URL) == "" {
			return ErrURLRequired
		}
	case KindUsenet:
		if strings.TrimSpace(c.Host) == "" || c.Port == 0 {
			return ErrHostRequired
		}
		if len(c.Modes) > 0 {
			return ErrModesNotPlayer
		}
	}
	return validateModes(c.Modes)
}

func validateModes(modes []string) error {
	for _, m := range modes {
		if !validModes[m] {
			return fmt.Errorf("%w: %q", ErrInvalidMode, m)
		}
	}
	return nil
}

// encrypt returns the ciphertext for a plaintext secret, or "" for an empty
// one — an unset secret is stored as an empty column, never as the encryption
// of "" (matches connections.Store.UpsertWithUsername).
func (s *Store) encrypt(secret string) (string, error) {
	if secret == "" {
		return "", nil
	}
	encrypted, err := s.secrets.Encrypt(secret)
	if err != nil {
		return "", fmt.Errorf("encrypting service connection secret: %w", err)
	}
	return encrypted, nil
}

func (s *Store) decrypt(encrypted string) (string, error) {
	if encrypted == "" {
		return "", nil
	}
	plaintext, err := s.secrets.Decrypt(encrypted)
	if err != nil {
		return "", fmt.Errorf("decrypting service connection secret: %w", err)
	}
	return plaintext, nil
}

// Create validates and inserts c, appended after every existing row of its kind
// (sort_order = current max + 1). c.Secret is the plaintext secret ("" stores
// none) and c.Modes is applied for player rows in the same transaction.
func (s *Store) Create(ctx context.Context, c Connection) (*Connection, error) {
	if err := validate(c); err != nil {
		return nil, err
	}
	encrypted, err := s.encrypt(c.Secret)
	if err != nil {
		return nil, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("creating service connection: %w", err)
	}
	defer tx.Rollback()

	row := tx.QueryRowContext(ctx, `
		INSERT INTO service_connections (kind, provider, label, url, host, port, tls, username, max_conns, secret_encrypted, enabled, sort_order)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, (SELECT COALESCE(MAX(sort_order), -1) + 1 FROM service_connections WHERE kind = ?))
		RETURNING id, sort_order, created_at, updated_at
	`, string(c.Kind), string(c.Provider), c.Label, c.URL, c.Host, c.Port, c.TLS, c.Username, c.MaxConns, encrypted, c.Enabled, string(c.Kind))
	if err := row.Scan(&c.ID, &c.SortOrder, &c.CreatedAt, &c.UpdatedAt); err != nil {
		return nil, fmt.Errorf("creating service connection %q: %w", c.Label, err)
	}
	if err := replaceModes(ctx, tx, c.ID, c.Modes); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("creating service connection %q: %w", c.Label, err)
	}
	return &c, nil
}

// Update validates and overwrites every editable field of the row with the
// given id. sort_order and mode assignment are untouched — SetModes owns modes,
// matching rssfeeds.Store.Update's "reordering is Reorder's job" convention.
//
// secret follows this project's three-state rule: nil PRESERVES the stored
// secret (the DTO masks it, so an untouched save must not wipe a working
// credential), "" CLEARS it, and a non-empty value replaces it. c.Secret is
// ignored by Update — pass the secret through this parameter.
func (s *Store) Update(ctx context.Context, id int64, c Connection, secret *string) (*Connection, error) {
	// Modes are SetModes' job; don't let a stale c.Modes fail validation.
	c.Modes = nil
	if err := validate(c); err != nil {
		return nil, err
	}

	var row *sql.Row
	if secret != nil {
		encrypted, err := s.encrypt(*secret)
		if err != nil {
			return nil, err
		}
		row = s.db.QueryRowContext(ctx, `
			UPDATE service_connections SET
				kind = ?, provider = ?, label = ?, url = ?, host = ?, port = ?, tls = ?, username = ?,
				max_conns = ?, secret_encrypted = ?, enabled = ?,
				updated_at = sakms_now()
			WHERE id = ?
			RETURNING id, sort_order, created_at, updated_at
		`, string(c.Kind), string(c.Provider), c.Label, c.URL, c.Host, c.Port, c.TLS, c.Username, c.MaxConns, encrypted, c.Enabled, id)
	} else {
		// Preserve the stored secret_encrypted by omitting it from SET.
		row = s.db.QueryRowContext(ctx, `
			UPDATE service_connections SET
				kind = ?, provider = ?, label = ?, url = ?, host = ?, port = ?, tls = ?, username = ?,
				max_conns = ?, enabled = ?,
				updated_at = sakms_now()
			WHERE id = ?
			RETURNING id, sort_order, created_at, updated_at
		`, string(c.Kind), string(c.Provider), c.Label, c.URL, c.Host, c.Port, c.TLS, c.Username, c.MaxConns, c.Enabled, id)
	}

	if err := row.Scan(&c.ID, &c.SortOrder, &c.CreatedAt, &c.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("updating service connection %d: %w", id, err)
	}
	modes, err := s.modesFor(ctx, id)
	if err != nil {
		return nil, err
	}
	c.Modes = modes
	return &c, nil
}

// SetModes replaces the row's mode assignment wholesale. Only player rows can
// be assigned; a usenet row is scoped by nothing.
func (s *Store) SetModes(ctx context.Context, id int64, modes []string) error {
	if err := validateModes(modes); err != nil {
		return err
	}
	var kind string
	if err := s.db.QueryRowContext(ctx, `SELECT kind FROM service_connections WHERE id = ?`, id).Scan(&kind); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return fmt.Errorf("loading service connection %d: %w", id, err)
	}
	if Kind(kind) != KindPlayer && len(modes) > 0 {
		return ErrModesNotPlayer
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("setting modes for service connection %d: %w", id, err)
	}
	defer tx.Rollback()
	if err := replaceModes(ctx, tx, id, modes); err != nil {
		return err
	}
	return tx.Commit()
}

func replaceModes(ctx context.Context, tx *sql.Tx, id int64, modes []string) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM service_connection_modes WHERE connection_id = ?`, id); err != nil {
		return fmt.Errorf("clearing modes for service connection %d: %w", id, err)
	}
	for _, m := range modes {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO service_connection_modes (connection_id, mode) VALUES (?, ?)
			ON CONFLICT (connection_id, mode) DO NOTHING
		`, id, m); err != nil {
			return fmt.Errorf("assigning mode %q to service connection %d: %w", m, id, err)
		}
	}
	return nil
}

// Delete permanently removes the row and its mode assignments. Explicit
// two-statement delete rather than relying on the schema's ON DELETE CASCADE:
// SQLite only enforces foreign keys when a connection has run `PRAGMA
// foreign_keys = ON`, which internal/db's shared Open doesn't set — the same
// workaround library.Store.Delete documents. Returns ErrNotFound when id has no
// stored row, so the HTTP handler can 404 rather than silently 204.
func (s *Store) Delete(ctx context.Context, id int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("deleting service connection %d: %w", id, err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `DELETE FROM service_connection_modes WHERE connection_id = ?`, id); err != nil {
		return fmt.Errorf("deleting modes for service connection %d: %w", id, err)
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM service_connections WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("deleting service connection %d: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return tx.Commit()
}

// Get returns one row by id with its secret decrypted and its modes loaded.
func (s *Store) Get(ctx context.Context, id int64) (*Connection, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+selectColumns+` FROM service_connections WHERE id = ?`, id)
	c, encrypted, err := scanConnection(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("loading service connection %d: %w", id, err)
	}
	if c.Secret, err = s.decrypt(encrypted); err != nil {
		return nil, err
	}
	if c.Modes, err = s.modesFor(ctx, c.ID); err != nil {
		return nil, err
	}
	return &c, nil
}

// List returns every row as a secret-free Summary, ordered by kind then
// sort_order. []Summary{}, not a nil slice — a blank install's empty list must
// serialize as [] rather than null.
func (s *Store) List(ctx context.Context) ([]Summary, error) {
	conns, encrypted, err := s.query(ctx, `SELECT `+selectColumns+` FROM service_connections ORDER BY kind ASC, sort_order ASC, id ASC`)
	if err != nil {
		return nil, err
	}
	out := []Summary{}
	for i, c := range conns {
		if c.Modes, err = s.modesFor(ctx, c.ID); err != nil {
			return nil, err
		}
		out = append(out, s.summarize(c, encrypted[i]))
	}
	return out, nil
}

// summarize builds the wire-safe view of a row. The secret is reduced to
// "is one set" plus its last 4 plaintext characters, matching Settings' masked
// "••••••••3f2a" display — the same contract connections.Summary already has.
func (s *Store) summarize(c Connection, encrypted string) Summary {
	sum := Summary{
		ID: c.ID, Kind: string(c.Kind), Provider: string(c.Provider), Label: c.Label,
		Enabled: c.Enabled, SortOrder: c.SortOrder, URL: c.URL, Host: c.Host, Port: c.Port,
		TLS: c.TLS, MaxConns: c.MaxConns, Username: c.Username, HasSecret: encrypted != "",
		Modes: c.Modes, CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt,
	}
	if sum.Modes == nil {
		sum.Modes = []string{}
	}
	if encrypted != "" {
		// A failed decrypt is not fatal for a summary — the row still exists
		// and the operator still needs to see it; only the suffix is lost.
		if plaintext, err := s.secrets.Decrypt(encrypted); err == nil && len(plaintext) >= 4 {
			sum.SecretSuffix = plaintext[len(plaintext)-4:]
		}
	}
	return sum
}

// ListByKind returns every row of one kind with secrets decrypted — what
// buildUsenetManager and the player builder consume.
func (s *Store) ListByKind(ctx context.Context, kind Kind) ([]Connection, error) {
	conns, encrypted, err := s.query(ctx,
		`SELECT `+selectColumns+` FROM service_connections WHERE kind = ? ORDER BY sort_order ASC, id ASC`, string(kind))
	if err != nil {
		return nil, err
	}
	for i := range conns {
		if conns[i].Secret, err = s.decrypt(encrypted[i]); err != nil {
			return nil, err
		}
		if conns[i].Modes, err = s.modesFor(ctx, conns[i].ID); err != nil {
			return nil, err
		}
	}
	return conns, nil
}

// PlayersForMode returns every ENABLED player assigned to m, secrets decrypted
// — the one query mode.Build needs to construct a Session's player fan-out.
// A disabled player, or one assigned to no mode, is never returned.
func (s *Store) PlayersForMode(ctx context.Context, m string) ([]Connection, error) {
	if !validModes[m] {
		return nil, fmt.Errorf("%w: %q", ErrInvalidMode, m)
	}
	conns, encrypted, err := s.query(ctx, `
		SELECT `+selectColumns+` FROM service_connections c
		WHERE c.kind = 'player' AND c.enabled = true
		  AND EXISTS (SELECT 1 FROM service_connection_modes m WHERE m.connection_id = c.id AND m.mode = ?)
		ORDER BY c.sort_order ASC, c.id ASC
	`, m)
	if err != nil {
		return nil, err
	}
	for i := range conns {
		if conns[i].Secret, err = s.decrypt(encrypted[i]); err != nil {
			return nil, err
		}
		if conns[i].Modes, err = s.modesFor(ctx, conns[i].ID); err != nil {
			return nil, err
		}
	}
	return conns, nil
}

// query runs a SELECT of selectColumns and returns the scanned rows alongside
// each row's still-encrypted secret, so callers decide whether to decrypt
// (ListByKind/PlayersForMode) or only summarize (List).
func (s *Store) query(ctx context.Context, sqlText string, args ...any) ([]Connection, []string, error) {
	rows, err := s.db.QueryContext(ctx, sqlText, args...)
	if err != nil {
		return nil, nil, fmt.Errorf("listing service connections: %w", err)
	}
	defer rows.Close()

	conns := []Connection{}
	secretsOut := []string{}
	for rows.Next() {
		c, encrypted, err := scanConnection(rows)
		if err != nil {
			return nil, nil, fmt.Errorf("scanning service connection: %w", err)
		}
		conns = append(conns, c)
		secretsOut = append(secretsOut, encrypted)
	}
	return conns, secretsOut, rows.Err()
}

// modesFor returns the modes assigned to id, alphabetically for determinism.
func (s *Store) modesFor(ctx context.Context, id int64) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT mode FROM service_connection_modes WHERE connection_id = ? ORDER BY mode ASC`, id)
	if err != nil {
		return nil, fmt.Errorf("listing modes for service connection %d: %w", id, err)
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var m string
		if err := rows.Scan(&m); err != nil {
			return nil, fmt.Errorf("scanning mode for service connection %d: %w", id, err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// rowScanner is satisfied by both *sql.Row and *sql.Rows.
type rowScanner interface {
	Scan(dest ...any) error
}

func scanConnection(row rowScanner) (Connection, string, error) {
	var c Connection
	var kind, provider, encrypted string
	err := row.Scan(&c.ID, &kind, &provider, &c.Label, &c.URL, &c.Host, &c.Port, &c.TLS,
		&c.Username, &c.MaxConns, &encrypted, &c.Enabled, &c.SortOrder, &c.CreatedAt, &c.UpdatedAt)
	c.Kind = Kind(kind)
	c.Provider = Provider(provider)
	return c, encrypted, err
}
