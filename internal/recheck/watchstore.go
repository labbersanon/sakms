package recheck

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/labbersanon/sakms/internal/mode"
)

// Watch is one flagged pick the recheck job keeps re-probing. Its identity
// columns are mode-dependent and deliberately explicit (not a collapsed key
// pair): Movies/Series carry TMDBID [+ Season/Episode]; Adult carries
// Studio/Title (it has no tmdb/imdb/tvdb id — see availability.CheckAdultScene).
// LastCheckedAt is empty until the first probe; LastAvailable is that probe's
// most recent outcome, which the UI badge can reflect on next page load (the
// honest pull model — Stage 8 introduces no push notifier; see the plan).
type Watch struct {
	ID            int64
	Mode          mode.Mode
	TMDBID        int
	Season        int
	Episode       int
	Studio        string
	Title         string
	AddedAt       string
	LastCheckedAt string
	LastAvailable bool
}

// WatchStore is a thin SQLite-backed store over the availability_watch table,
// same shape as internal/library's Store and every other store in this repo —
// it owns no HTTP client and makes no outbound calls.
type WatchStore struct {
	db *sql.DB
}

// NewWatchStore builds a WatchStore against db.
func NewWatchStore(db *sql.DB) *WatchStore {
	return &WatchStore{db: db}
}

// Add registers w as a watched pick, or returns the already-registered row if
// an identical (mode + identity) entry exists — idempotent, so re-adding the
// same pick is a no-op that returns the existing row rather than duplicating
// or erroring (matching library.Store.Upsert's re-entrant convention). Add
// never sets a result: last_checked_at/last_available keep their stored values
// (defaults on a fresh insert), since registering interest is separate from
// probing.
func (s *WatchStore) Add(ctx context.Context, w Watch) (Watch, error) {
	row := s.db.QueryRowContext(ctx, `
		INSERT INTO availability_watch (mode, tmdb_id, season, episode, studio, title)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(mode, tmdb_id, season, episode, studio, title) DO UPDATE SET mode = excluded.mode
		RETURNING id, added_at, last_checked_at, last_available
	`, string(w.Mode), w.TMDBID, w.Season, w.Episode, w.Studio, w.Title)

	var lastAvailable int
	if err := row.Scan(&w.ID, &w.AddedAt, &w.LastCheckedAt, &lastAvailable); err != nil {
		return Watch{}, fmt.Errorf("adding availability watch: %w", err)
	}
	w.LastAvailable = lastAvailable != 0
	return w, nil
}

// List returns every watched pick, ordered by id.
func (s *WatchStore) List(ctx context.Context) ([]Watch, error) {
	return s.query(ctx, `
		SELECT id, mode, tmdb_id, season, episode, studio, title, added_at, last_checked_at, last_available
		FROM availability_watch ORDER BY id
	`)
}

// ListDue returns every watched pick due for a recheck: those never checked
// (an empty last_checked_at) and those last checked before since. Timestamps
// are stored as RFC3339Nano UTC strings, so a lexicographic comparison against
// the same format is a correct chronological comparison, and an empty string
// sorts before any real timestamp so a never-checked entry is always due.
func (s *WatchStore) ListDue(ctx context.Context, since time.Time) ([]Watch, error) {
	cutoff := since.UTC().Format(time.RFC3339Nano)
	return s.query(ctx, `
		SELECT id, mode, tmdb_id, season, episode, studio, title, added_at, last_checked_at, last_available
		FROM availability_watch
		WHERE last_checked_at = '' OR last_checked_at < ?
		ORDER BY id
	`, cutoff)
}

// UpdateResult records one probe's outcome onto an existing entry, without
// touching its identity columns. Updating an id that doesn't exist is not an
// error, matching library.Store.Delete/UpdatePHash's convention.
func (s *WatchStore) UpdateResult(ctx context.Context, id int64, available bool, checkedAt string) error {
	avail := 0
	if available {
		avail = 1
	}
	_, err := s.db.ExecContext(ctx, `
		UPDATE availability_watch SET last_available = ?, last_checked_at = ? WHERE id = ?
	`, avail, checkedAt, id)
	if err != nil {
		return fmt.Errorf("updating availability watch %d: %w", id, err)
	}
	return nil
}

// Remove deletes the watched pick id, if it exists. Removing an id that isn't
// present is not an error — the end state is the same.
func (s *WatchStore) Remove(ctx context.Context, id int64) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM availability_watch WHERE id = ?`, id); err != nil {
		return fmt.Errorf("removing availability watch %d: %w", id, err)
	}
	return nil
}

// query runs a SELECT returning the full Watch column set and scans every row.
func (s *WatchStore) query(ctx context.Context, sqlText string, args ...any) ([]Watch, error) {
	rows, err := s.db.QueryContext(ctx, sqlText, args...)
	if err != nil {
		return nil, fmt.Errorf("listing availability watches: %w", err)
	}
	defer rows.Close()

	out := []Watch{}
	for rows.Next() {
		var w Watch
		var m string
		var lastAvailable int
		if err := rows.Scan(&w.ID, &m, &w.TMDBID, &w.Season, &w.Episode, &w.Studio, &w.Title,
			&w.AddedAt, &w.LastCheckedAt, &lastAvailable); err != nil {
			return nil, fmt.Errorf("scanning availability watch: %w", err)
		}
		w.Mode = mode.Mode(m)
		w.LastAvailable = lastAvailable != 0
		out = append(out, w)
	}
	return out, rows.Err()
}
