package trakt

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// ErrNotConfigured is returned by Get (and used internally by Session) when
// no Trakt application credentials have been saved yet.
var ErrNotConfigured = errors.New("trakt: no connection configured")

// encryptor is the subset of *secrets.Store this package needs — mirrors
// internal/connections' own encryptor interface so both packages can be
// satisfied by the same *secrets.Store without either importing the other.
type encryptor interface {
	Encrypt(plaintext string) (string, error)
	Decrypt(encoded string) (string, error)
}

// Store persists Trakt's single application connection: operator-entered
// credentials plus (once the device flow has completed) one linked
// account's OAuth tokens. Backed by a singleton row (trakt_connection,
// id=1) rather than internal/connections' generic service/url/api_key
// shape, which has no room for five distinct fields — see this package's
// migration (0022_trakt_connection.sql) for why.
type Store struct {
	db      *sql.DB
	secrets encryptor
}

// NewStore builds a Store. secretStore encrypts client_secret/access_token/
// refresh_token before they reach SQLite and decrypts them on read — same
// key, same convention as internal/connections.Store.
func NewStore(db *sql.DB, secretStore encryptor) *Store {
	return &Store{db: db, secrets: secretStore}
}

// Credentials is the operator-registered Trakt application. ClientID is not
// secret (Trakt sends it as a plain header on every request, even
// unauthenticated ones) but is stored alongside the encrypted secret for
// convenience — same "config lives with its secret" shape as
// connections.Connection's URL+APIKey.
type Credentials struct {
	ClientID     string
	ClientSecret string
}

// Tokens is one linked Trakt account's OAuth state, produced only by a
// successful device-flow exchange (see oauth.go's PollDeviceToken) or a
// refresh — never operator-entered, so unlike Credentials it has no
// three-state save semantics; SaveTokens always overwrites.
type Tokens struct {
	AccessToken  string
	RefreshToken string
	ExpiresAt    time.Time
}

// Linked reports whether tokens have actually been obtained (as opposed to
// a zero Tokens on a connection that only has credentials so far).
func (t Tokens) Linked() bool {
	return t.AccessToken != ""
}

// Connection is the full stored state.
type Connection struct {
	Credentials
	Tokens
}

// row mirrors trakt_connection's columns before decryption.
type row struct {
	clientID              string
	clientSecretEncrypted string
	accessTokenEncrypted  string
	refreshTokenEncrypted string
	tokenExpiresAt        string
}

func (s *Store) selectRow(ctx context.Context) (*row, error) {
	var r row
	err := s.db.QueryRowContext(ctx, `
		SELECT client_id, client_secret_encrypted, access_token_encrypted, refresh_token_encrypted, token_expires_at
		FROM trakt_connection WHERE id = 1
	`).Scan(&r.clientID, &r.clientSecretEncrypted, &r.accessTokenEncrypted, &r.refreshTokenEncrypted, &r.tokenExpiresAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotConfigured
		}
		return nil, fmt.Errorf("loading trakt connection: %w", err)
	}
	return &r, nil
}

func (s *Store) decrypt(encoded string) (string, error) {
	if encoded == "" {
		return "", nil
	}
	return s.secrets.Decrypt(encoded)
}

// Get returns the stored connection with all secrets decrypted.
// Returns ErrNotConfigured if no row exists yet (SaveCredentials never
// called). A row with credentials but no tokens yet is not an error —
// callers check Tokens.Linked().
func (s *Store) Get(ctx context.Context) (*Connection, error) {
	r, err := s.selectRow(ctx)
	if err != nil {
		return nil, err
	}
	clientSecret, err := s.decrypt(r.clientSecretEncrypted)
	if err != nil {
		return nil, fmt.Errorf("decrypting trakt client secret: %w", err)
	}
	accessToken, err := s.decrypt(r.accessTokenEncrypted)
	if err != nil {
		return nil, fmt.Errorf("decrypting trakt access token: %w", err)
	}
	refreshToken, err := s.decrypt(r.refreshTokenEncrypted)
	if err != nil {
		return nil, fmt.Errorf("decrypting trakt refresh token: %w", err)
	}
	var expiresAt time.Time
	if r.tokenExpiresAt != "" {
		expiresAt, err = time.Parse(time.RFC3339, r.tokenExpiresAt)
		if err != nil {
			return nil, fmt.Errorf("parsing trakt token expiry: %w", err)
		}
	}
	return &Connection{
		Credentials: Credentials{ClientID: r.clientID, ClientSecret: clientSecret},
		Tokens:      Tokens{AccessToken: accessToken, RefreshToken: refreshToken, ExpiresAt: expiresAt},
	}, nil
}

// Configured reports whether application credentials have been saved.
func (s *Store) Configured(ctx context.Context) (bool, error) {
	r, err := s.selectRow(ctx)
	if errors.Is(err, ErrNotConfigured) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return r.clientID != "" && r.clientSecretEncrypted != "", nil
}

// SaveCredentials creates or updates the stored client_id/client_secret,
// leaving any already-linked tokens untouched. clientSecret follows this
// project's three-state secret convention (see connections.Store.
// UpsertPreservingSecret and CLAUDE.md's *string/omitempty rule): nil
// preserves whatever secret is already stored (the Settings form never
// receives the real secret back, so re-saving after only editing client_id
// must not wipe it), an empty string clears it, and any other value
// sets/replaces it.
func (s *Store) SaveCredentials(ctx context.Context, clientID string, clientSecret *string) error {
	if clientSecret != nil {
		encrypted := ""
		if *clientSecret != "" {
			var err error
			encrypted, err = s.secrets.Encrypt(*clientSecret)
			if err != nil {
				return fmt.Errorf("encrypting trakt client secret: %w", err)
			}
		}
		_, err := s.db.ExecContext(ctx, `
			INSERT INTO trakt_connection (id, client_id, client_secret_encrypted, updated_at)
			VALUES (1, ?, ?, strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
			ON CONFLICT(id) DO UPDATE SET
				client_id = excluded.client_id,
				client_secret_encrypted = excluded.client_secret_encrypted,
				updated_at = excluded.updated_at
		`, clientID, encrypted)
		if err != nil {
			return fmt.Errorf("saving trakt credentials: %w", err)
		}
		return nil
	}
	// clientSecret == nil: preserve the existing client_secret_encrypted by
	// omitting it from the UPDATE SET clause. A brand-new row (no prior
	// secret to preserve) correctly gets '' via the INSERT default.
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO trakt_connection (id, client_id, updated_at)
		VALUES (1, ?, strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
		ON CONFLICT(id) DO UPDATE SET
			client_id = excluded.client_id,
			updated_at = excluded.updated_at
	`, clientID)
	if err != nil {
		return fmt.Errorf("saving trakt credentials: %w", err)
	}
	return nil
}

// SaveTokens persists a device-flow exchange or refresh's resulting tokens.
// Always overwrites — tokens have no "preserve" state because they're never
// operator-entered (see Tokens' doc comment). Requires a row to already
// exist (SaveCredentials must run first); returns ErrNotConfigured
// otherwise, since a token with no client_id/secret to refresh it later
// would be a dead end.
func (s *Store) SaveTokens(ctx context.Context, accessToken, refreshToken string, expiresAt time.Time) error {
	accessEnc, err := s.secrets.Encrypt(accessToken)
	if err != nil {
		return fmt.Errorf("encrypting trakt access token: %w", err)
	}
	refreshEnc, err := s.secrets.Encrypt(refreshToken)
	if err != nil {
		return fmt.Errorf("encrypting trakt refresh token: %w", err)
	}
	res, err := s.db.ExecContext(ctx, `
		UPDATE trakt_connection SET
			access_token_encrypted = ?,
			refresh_token_encrypted = ?,
			token_expires_at = ?,
			updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
		WHERE id = 1
	`, accessEnc, refreshEnc, expiresAt.UTC().Format(time.RFC3339))
	if err != nil {
		return fmt.Errorf("saving trakt tokens: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("saving trakt tokens: %w", err)
	}
	if n == 0 {
		return ErrNotConfigured
	}
	return nil
}

// ClearTokens unlinks the Trakt account (e.g. an operator-initiated
// "disconnect") while leaving the application credentials in place, so
// re-linking doesn't require re-entering client_id/secret.
func (s *Store) ClearTokens(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE trakt_connection SET
			access_token_encrypted = '',
			refresh_token_encrypted = '',
			token_expires_at = '',
			updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
		WHERE id = 1
	`)
	if err != nil {
		return fmt.Errorf("clearing trakt tokens: %w", err)
	}
	return nil
}

// Delete removes the connection entirely (credentials and tokens). Deleting
// when nothing is configured is not an error — the end state is the same,
// matching connections.Store.Delete's convention.
func (s *Store) Delete(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM trakt_connection WHERE id = 1`); err != nil {
		return fmt.Errorf("deleting trakt connection: %w", err)
	}
	return nil
}
