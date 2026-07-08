// Package settings persists small, non-secret key/value preferences —
// today, just the setup wizard's dismissed flag. Anything with real
// structure (connections, proposals, the allowlist) gets its own table and
// package; this one is deliberately just a flat KV store for the handful of
// one-off flags that don't warrant that.
package settings

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// ErrNotFound is returned by Get when key has no stored value.
var ErrNotFound = errors.New("settings: no value for that key")

type Store struct {
	db *sql.DB
}

func New(db *sql.DB) *Store {
	return &Store{db: db}
}

// Get returns the stored value for key. Returns ErrNotFound if unset.
func (s *Store) Get(ctx context.Context, key string) (string, error) {
	var value string
	row := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key)
	if err := row.Scan(&value); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", ErrNotFound
		}
		return "", fmt.Errorf("loading setting %q: %w", key, err)
	}
	return value, nil
}

// GetBool returns the stored value for key parsed as a bool, or def if
// unset. Any stored value other than exactly "true" is treated as false —
// this package never stores anything else, so a mismatch would indicate
// corruption, not a valid alternate value to interpret.
func (s *Store) GetBool(ctx context.Context, key string, def bool) (bool, error) {
	value, err := s.Get(ctx, key)
	if errors.Is(err, ErrNotFound) {
		return def, nil
	}
	if err != nil {
		return false, err
	}
	return value == "true", nil
}

// Set stores value for key, creating or overwriting it.
func (s *Store) Set(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO settings (key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value
	`, key, value)
	if err != nil {
		return fmt.Errorf("saving setting %q: %w", key, err)
	}
	return nil
}

// SetBool stores value for key as "true" or "false".
func (s *Store) SetBool(ctx context.Context, key string, value bool) error {
	v := "false"
	if value {
		v = "true"
	}
	return s.Set(ctx, key, v)
}
