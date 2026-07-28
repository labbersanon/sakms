package adultmergecache

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

// Store persists precomputed Adult Discover merged-row pages in the
// adult_merged_row_cache table (migration 0045). The persistence layer is
// byte-for-byte identical for both row types ('performers'/'studios'), so it is
// shared here — the genuine per-row-type difference lives in the precompute
// pipelines (precompute.go's sibling functions), not the store.
type Store struct{ db *sql.DB }

// New returns a Store backed by db (the same *sql.DB every other store wraps —
// no second connection needed).
func New(db *sql.DB) *Store { return &Store{db: db} }

// CachedPage is one precomputed page as stored: the PRE-availability-filter
// merged card list (an opaque JSON blob the caller decodes into the right card
// type), the pre-filter HasMore flag, and the compute timestamp.
type CachedPage struct {
	Items      json.RawMessage
	HasMore    bool
	ComputedAt string
}

// Get returns the cached page for (rowType, page, perPage). A miss returns
// (CachedPage{}, false, nil) — never an error — so the read handler treats "no
// cached row yet" as a plain fall-through to the live merge path. A real store
// error is returned; the handler treats that as fall-through too (fail open).
func (s *Store) Get(ctx context.Context, rowType string, page, perPage int) (CachedPage, bool, error) {
	var (
		payload    string
		hasMore    int
		computedAt string
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT payload, has_more, computed_at
		   FROM adult_merged_row_cache
		  WHERE row_type = ? AND page = ? AND per_page = ?`,
		rowType, page, perPage,
	).Scan(&payload, &hasMore, &computedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return CachedPage{}, false, nil
	}
	if err != nil {
		return CachedPage{}, false, err
	}
	return CachedPage{
		Items:      json.RawMessage(payload),
		HasMore:    hasMore != 0,
		ComputedAt: computedAt,
	}, true, nil
}

// Put upserts one precomputed page, overwriting any existing row for the same
// (rowType, page, perPage) via ON CONFLICT DO UPDATE — so a re-run of the
// precompute cycle replaces the page in place rather than erroring or
// duplicating. payloadJSON is the PRE-filter merged card list; hasMore is the
// pre-filter flag; computedAt is the compute timestamp (RFC3339).
func (s *Store) Put(ctx context.Context, rowType string, page, perPage int, payloadJSON json.RawMessage, hasMore bool, computedAt string) error {
	hm := 0
	if hasMore {
		hm = 1
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO adult_merged_row_cache (row_type, page, per_page, payload, has_more, computed_at)
		 VALUES (?, ?, ?, ?, ?, ?)
		 ON CONFLICT(row_type, page, per_page) DO UPDATE SET
		   payload     = excluded.payload,
		   has_more    = excluded.has_more,
		   computed_at = excluded.computed_at`,
		rowType, page, perPage, string(payloadJSON), hm, computedAt,
	)
	return err
}
