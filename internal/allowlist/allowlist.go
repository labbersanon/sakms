// Package allowlist persists Purge's editable tag rules, one list per mode.
// Matching those tags against tracked items is internal/purge's job; this
// package only knows how to store and retrieve the rule list itself, the
// same split internal/proposals keeps from internal/rename.
package allowlist

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/labbersanon/sakms/internal/mode"
)

type Store struct {
	db *sql.DB
}

func New(db *sql.DB) *Store {
	return &Store{db: db}
}

// List returns every allowlisted tag for m, alphabetically.
func (s *Store) List(ctx context.Context, m mode.Mode) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT tag FROM purge_allowlist WHERE mode = ? ORDER BY tag COLLATE NOCASE`, string(m))
	if err != nil {
		return nil, fmt.Errorf("listing allowlist for %q: %w", m, err)
	}
	defer rows.Close()

	// []string{}, not var out []string — an empty allowlist should serialize
	// as [] over the API, not null, so a frontend never needs a special case
	// for "no rules yet" versus "some rules."
	out := []string{}
	for rows.Next() {
		var tag string
		if err := rows.Scan(&tag); err != nil {
			return nil, fmt.Errorf("scanning allowlist entry: %w", err)
		}
		out = append(out, tag)
	}
	return out, rows.Err()
}

// Add inserts tag into m's allowlist. Adding a tag already present
// (case-insensitively) is not an error — the end state is the same.
func (s *Store) Add(ctx context.Context, m mode.Mode, tag string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO purge_allowlist (mode, tag) VALUES (?, ?) ON CONFLICT DO NOTHING`, string(m), tag)
	if err != nil {
		return fmt.Errorf("adding %q to %q's allowlist: %w", tag, m, err)
	}
	return nil
}

// Remove deletes tag from m's allowlist (case-insensitively). Removing a tag
// that isn't present is not an error.
func (s *Store) Remove(ctx context.Context, m mode.Mode, tag string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM purge_allowlist WHERE mode = ? AND tag = ? COLLATE NOCASE`, string(m), tag)
	if err != nil {
		return fmt.Errorf("removing %q from %q's allowlist: %w", tag, m, err)
	}
	return nil
}
