// Package connections persists SAK's configured service connections —
// non-secret fields in SQLite, API keys encrypted via internal/secrets. This
// is what Settings' connection list is backed by. internal/api's
// TestConnection is a separate, unpersisted one-shot check: you Test before
// you Save, and this package is what Save actually writes to.
package connections

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// ErrNotFound is returned by Get when service has no configured connection.
var ErrNotFound = errors.New("connections: no connection configured for that service")

type encryptor interface {
	Encrypt(plaintext string) (string, error)
	Decrypt(encoded string) (string, error)
}

// Store persists connections against a database, encrypting API keys with
// secretStore before they're written and decrypting them on read.
type Store struct {
	db      *sql.DB
	secrets encryptor
}

func New(db *sql.DB, secretStore encryptor) *Store {
	return &Store{db: db, secrets: secretStore}
}

// Connection is one configured service connection, with its secret already
// decrypted. Never round-tripped through the HTTP API with the secret
// intact — see Summary for what's actually safe to expose.
type Connection struct {
	Service   string
	URL       string
	Username  string
	APIKey    string
	CreatedAt string
	UpdatedAt string
}

// Summary is what's safe to expose over the API: whether a secret is set and
// its last 4 characters (matching Settings' masked "••••••••3f2a" display),
// never the secret itself.
type Summary struct {
	Service   string `json:"service"`
	URL       string `json:"url"`
	Username  string `json:"username,omitempty"`
	HasAPIKey bool   `json:"hasApiKey"`
	KeySuffix string `json:"keySuffix,omitempty"`
	UpdatedAt string `json:"updatedAt"`
	// FixedURL is the hardcoded package-constant base URL for services whose
	// URL is not user-supplied (see fixedURLValues in internal/api/handler.go).
	// Not scanned from the DB — left zero-value by List(), populated by the API
	// layer before encoding so the UI can show the real in-use URL read-only.
	FixedURL string `json:"fixedUrl,omitempty"`
}

// Upsert creates or replaces the connection for service, with no username —
// every service so far (Radarr/Sonarr/Whisparr, the AI providers, the Adult
// identification pipeline) authenticates with a single API key. It's a thin
// wrapper around UpsertWithUsername so the encrypt-and-write logic lives in
// exactly one place. An empty apiKey clears any previously stored key (e.g.
// for a service like Ollama that doesn't need one).
func (s *Store) Upsert(ctx context.Context, service, url, apiKey string) error {
	return s.UpsertWithUsername(ctx, service, url, "", apiKey)
}

// UpsertWithUsername is Upsert plus a plaintext username — for services like
// qBittorrent and NZBGet that authenticate with username+password rather
// than a single API key. secret is encrypted the same way apiKey always has
// been; the column name (api_key_encrypted) predates this and stays generic
// in meaning (whatever secret the service needs), not renamed, to avoid a
// second migration for a cosmetic-only change.
func (s *Store) UpsertWithUsername(ctx context.Context, service, url, username, secret string) error {
	encrypted := ""
	if secret != "" {
		var err error
		encrypted, err = s.secrets.Encrypt(secret)
		if err != nil {
			return fmt.Errorf("encrypting secret: %w", err)
		}
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO connections (service, url, username, api_key_encrypted, updated_at)
		VALUES (?, ?, ?, ?, strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
		ON CONFLICT(service) DO UPDATE SET
			url = excluded.url,
			username = excluded.username,
			api_key_encrypted = excluded.api_key_encrypted,
			updated_at = excluded.updated_at
	`, service, url, username, encrypted)
	if err != nil {
		return fmt.Errorf("saving connection %q: %w", service, err)
	}
	return nil
}

// UpsertPreservingSecret is UpsertWithUsername, but a nil secret preserves
// whatever secret (if any) is already stored instead of clearing it. The HTTP
// API needs this because a connection's real secret is never sent back to the
// client once set (see Summary/List/Get) — the client's key field is always
// blank for an already-configured connection, so saving it again after editing
// only the URL would otherwise send "" and silently wipe the working key. nil
// is the only way for the client to express "leave the secret as it is". A
// secret pointing at "" still clears it explicitly (e.g. switching to a
// service that needs no key, like Ollama), and a non-empty *secret still
// sets/replaces it — both exactly as UpsertWithUsername already does.
func (s *Store) UpsertPreservingSecret(ctx context.Context, service, url, username string, secret *string) error {
	if secret != nil {
		return s.UpsertWithUsername(ctx, service, url, username, *secret)
	}
	// secret == nil: preserve the existing api_key_encrypted by omitting it
	// from the ON CONFLICT SET clause entirely. A brand-new row (no prior
	// secret to preserve) correctly gets '' — there's nothing to keep yet.
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO connections (service, url, username, api_key_encrypted, updated_at)
		VALUES (?, ?, ?, '', strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
		ON CONFLICT(service) DO UPDATE SET
			url = excluded.url,
			username = excluded.username,
			updated_at = excluded.updated_at
	`, service, url, username)
	if err != nil {
		return fmt.Errorf("saving connection %q: %w", service, err)
	}
	return nil
}

// Get returns the connection for service with its secret decrypted.
// Returns ErrNotFound if service isn't configured.
func (s *Store) Get(ctx context.Context, service string) (*Connection, error) {
	var c Connection
	var encrypted string
	row := s.db.QueryRowContext(ctx,
		`SELECT service, url, username, api_key_encrypted, created_at, updated_at FROM connections WHERE service = ?`, service)
	if err := row.Scan(&c.Service, &c.URL, &c.Username, &encrypted, &c.CreatedAt, &c.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("loading connection %q: %w", service, err)
	}
	if encrypted != "" {
		key, err := s.secrets.Decrypt(encrypted)
		if err != nil {
			return nil, fmt.Errorf("decrypting secret for %q: %w", service, err)
		}
		c.APIKey = key
	}
	return &c, nil
}

// List returns a redacted Summary for every configured connection, ordered
// by service name. Never includes a decrypted secret in full.
func (s *Store) List(ctx context.Context) ([]Summary, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT service, url, username, api_key_encrypted, updated_at FROM connections ORDER BY service`)
	if err != nil {
		return nil, fmt.Errorf("listing connections: %w", err)
	}
	defer rows.Close()

	// []Summary{}, not var out []Summary — a blank install's "no connections
	// yet" should serialize as [] over the API, not null (see
	// allowlist.Store.List's identical convention).
	out := []Summary{}
	for rows.Next() {
		var sum Summary
		var encrypted string
		if err := rows.Scan(&sum.Service, &sum.URL, &sum.Username, &encrypted, &sum.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scanning connection: %w", err)
		}
		if encrypted != "" {
			sum.HasAPIKey = true
			if key, err := s.secrets.Decrypt(encrypted); err == nil && len(key) >= 4 {
				sum.KeySuffix = key[len(key)-4:]
			}
		}
		out = append(out, sum)
	}
	return out, rows.Err()
}

// Delete removes the connection for service, if one exists. Deleting a
// service that isn't configured is not an error — the end state is the same.
func (s *Store) Delete(ctx context.Context, service string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM connections WHERE service = ?`, service); err != nil {
		return fmt.Errorf("deleting connection %q: %w", service, err)
	}
	return nil
}
