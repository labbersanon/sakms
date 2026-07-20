// Package nodekeys manages per-node bearer keys. Each approved node receives a
// unique raw key that is returned once at approval time and never stored.
// Only the SHA-256 hex digest is persisted in the node_keys table.
package nodekeys

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"time"
)

// Store is a SQLite-backed store for per-node bearer keys.
type Store struct {
	db *sql.DB
}

// New returns a Store backed by db.
func New(db *sql.DB) *Store { return &Store{db: db} }

// Create generates a fresh node key for name, persists only its SHA-256 hash,
// and returns (id, rawKey). The raw key is returned once and never stored; the
// caller must deliver it to the node before discarding it.
func (s *Store) Create(ctx context.Context, name string) (id, rawKey string, err error) {
	id = rand.Text()
	raw := make([]byte, 32)
	if _, err = rand.Read(raw); err != nil {
		return "", "", err
	}
	rawKey = base64.RawURLEncoding.EncodeToString(raw)
	hash := hashKey(rawKey)
	now := time.Now().UTC().Format(time.RFC3339)
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO node_keys (id, key_hash, name, approved_at) VALUES (?, ?, ?, ?)`,
		id, hash, name, now)
	if err != nil {
		return "", "", err
	}
	return id, rawKey, nil
}

// Validate does a constant-time scan of all stored hashes against rawKey.
// Returns the node name and true on a match.
func (s *Store) Validate(ctx context.Context, rawKey string) (name string, ok bool) {
	if rawKey == "" {
		return "", false
	}
	presented := []byte(hashKey(rawKey))
	rows, err := s.db.QueryContext(ctx, `SELECT key_hash, name FROM node_keys`)
	if err != nil {
		return "", false
	}
	defer rows.Close()
	for rows.Next() {
		var storedHash, nodeName string
		if err := rows.Scan(&storedHash, &nodeName); err != nil {
			continue
		}
		if subtle.ConstantTimeCompare(presented, []byte(storedHash)) == 1 {
			return nodeName, true
		}
	}
	return "", false
}

// Revoke deletes the key record with the given id. A missing id is not an error.
func (s *Store) Revoke(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM node_keys WHERE id = ?`, id)
	return err
}

func hashKey(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}
