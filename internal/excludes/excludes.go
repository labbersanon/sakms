// Package excludes persists the "excluded titles" list — titles an operator
// has permanently removed from the Requests worklist so they never surface (and
// so are never auto-grabbed via that worklist) again.
//
// Requests (internal/api/requests.go) is a pure derive-on-read aggregation over
// the tracked library + in-flight grabs with NO persisted per-request row, so a
// "remove" can't delete an existing row — it records a suppression here instead,
// which the aggregation consults and skips. This is read-model suppression only:
// the exclusion suppresses what the Requests screen shows/aggregates; it does
// not reach into RSS/autograb/recheck — but there IS no autonomous auto-grab
// path in this codebase today (every dispatchToDownloadClient caller is an
// operator-triggered HTTP handler), so this suppression already covers every
// real grab route. If a scheduled/RSS auto-grab is ever added, it MUST
// consult Keys() too, or an excluded title could be silently re-grabbed.
package excludes

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// ErrInvalid is returned by Add when an exclusion carries no usable identity
// (no TMDB id AND a blank title) or a blank mode — such a row could never match
// a real Requests row, so recording it would be silently useless.
var ErrInvalid = errors.New("excludes: an exclusion needs a mode and either a tmdb id or a title")

// Key is the mode-scoped identity a Requests row is deduped by — TMDB id when
// present (Movies/Series), else the lowercased/trimmed title (Adult scenes have
// no TMDB id). Mode-prefixed so the same TMDB id in two modes never collides.
// api.requestKey delegates to this so a stored exclusion key and a live Requests
// row's key are byte-identical (the suppression match is exact-string).
func Key(mode string, tmdbID int, title string) string {
	if tmdbID > 0 {
		return mode + ":tmdb:" + strconv.Itoa(tmdbID)
	}
	return mode + ":title:" + strings.ToLower(strings.TrimSpace(title))
}

// Store persists excluded titles against a database.
type Store struct {
	db *sql.DB
}

func New(db *sql.DB) *Store {
	return &Store{db: db}
}

// Add records one excluded title. Re-excluding the same title (same derived
// Key) is idempotent, not an error — the end state is identical (matches
// connections.Store/discoversliders.Store's "adding what's already there is
// fine" convention). Returns ErrInvalid when the exclusion has no usable
// identity.
func (s *Store) Add(ctx context.Context, mode string, tmdbID int, title string) error {
	mode = strings.TrimSpace(mode)
	title = strings.TrimSpace(title)
	if mode == "" || (tmdbID <= 0 && title == "") {
		return ErrInvalid
	}
	key := Key(mode, tmdbID, title)
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO excluded_titles (exclude_key, mode, tmdb_id, title)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(exclude_key) DO NOTHING
	`, key, mode, tmdbID, title)
	if err != nil {
		return fmt.Errorf("adding exclusion %q: %w", key, err)
	}
	return nil
}

// Keys returns the set of every stored exclusion key, for the Requests
// aggregation to skip against. Empty (non-nil) set when nothing is excluded.
func (s *Store) Keys(ctx context.Context) (map[string]bool, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT exclude_key FROM excluded_titles`)
	if err != nil {
		return nil, fmt.Errorf("loading exclusion keys: %w", err)
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, fmt.Errorf("scanning exclusion key: %w", err)
		}
		out[k] = true
	}
	return out, rows.Err()
}
