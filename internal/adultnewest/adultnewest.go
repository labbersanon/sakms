// Package adultnewest implements a periodic background job that queries
// Prowlarr's Adult category for its newest releases, matches each one back
// to a TPDB/StashDB/FansDB entity via internal/identify, and caches the
// matched, enriched results for Adult Discover to read — plus the
// admin-configurable "row" definitions (Movie/Scene/Performer/Studio,
// optionally narrowed by genre) that pick which cached rows render where.
//
// This package deliberately does NOT share code with internal/discoversliders
// (a similarly-shaped admin-configurable-row system, but TMDB-backed) or
// internal/recheck (a similarly-shaped periodic-background-job system, but a
// cheap boolean availability probe) — see CLAUDE.md's "no premature
// abstraction" convention. Its scan cycle's *shape* mirrors recheck's
// (settings-DB-backed interval, off by default, single sequential loop, no
// concurrency) precisely because that shape is what avoids reproducing the
// documented incident behind CLAUDE.md's "Discover never queries Prowlarr"
// rule (hundreds of concurrent live probes on every page load) — but Adult
// Discover's rows read ONLY the adult_newest_releases cache table this
// package writes, never Prowlarr or the identify pipeline directly, at
// request time. See scan.go for the job itself; this file is persistence +
// validation only for the row-config half, mirroring discoversliders.Store's
// own shape.
//
// Caching an entity requires more than "the identify pipeline matched it"
// (see scan.go's confirmAvailable, added after a live gap found in
// production 2026-07-15): dedup is by ENTITY, not by the specific release
// that triggered the match, so the original release's identity is never
// retained. A later Grab click has no choice but to re-search Prowlarr from
// scratch using the matched entity's CANONICAL title+studio — a stricter,
// different query than the raw release title the AI-assisted fuzzy pipeline
// actually matched against. Scene/Movie entities are therefore only cached
// once a live confirmation search (the same one a later Grab would run)
// proves it finds something — otherwise the pipeline would confidently
// display a card Grab can never fulfill.
package adultnewest

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// ErrNotFound is returned by Update/Delete when id has no stored row.
var ErrNotFound = errors.New("adultnewest: no row with that id")

// ErrInvalidRowType is returned when RowType isn't one of the fixed enum values.
var ErrInvalidRowType = errors.New("adultnewest: invalid row type")

// ErrTitleRequired is returned when Title is blank.
var ErrTitleRequired = errors.New("adultnewest: title is required")

// ErrReorderMismatch is returned by Reorder when the given ids don't cover
// exactly the same set of existing rows — a partial or stale id list would
// otherwise silently strand the omitted rows at their old sort_order instead
// of a well-defined position.
var ErrReorderMismatch = errors.New("adultnewest: reorder ids must match the full set of existing rows exactly")

// RowType is the fixed set of entity kinds a Discover row can bucket by.
type RowType string

const (
	RowMovie     RowType = "movie"
	RowScene     RowType = "scene"
	RowPerformer RowType = "performer"
	RowStudio    RowType = "studio"
)

var validRowTypes = map[RowType]bool{
	RowMovie:     true,
	RowScene:     true,
	RowPerformer: true,
	RowStudio:    true,
}

// Row is one admin-defined Adult Discover row, sourced from the
// adult_newest_releases cache rather than a live TMDB/Prowlarr call.
// GenreFilter is always optional — unlike discoversliders' FilterValue,
// every RowType can be freely narrowed by genre or left unfiltered, so there
// is no required/forbidden pairing rule to enforce here.
type Row struct {
	ID          int
	Title       string
	RowType     RowType
	GenreFilter string
	SortOrder   int
	Enabled     bool
	CreatedAt   string
	UpdatedAt   string
}

// Store persists Adult Discover row configs against a database.
type Store struct {
	db *sql.DB
}

func New(db *sql.DB) *Store {
	return &Store{db: db}
}

// validate checks title/rowType against the fixed enum, shared by Create and
// Update. GenreFilter has no validity constraint of its own — any string
// (including empty, meaning "no genre filter") is accepted; a genre that
// matches nothing simply yields an empty resolved row, same as any other
// slider-style filter that happens to have no current matches.
func validate(title string, rowType RowType) error {
	if title == "" {
		return ErrTitleRequired
	}
	if !validRowTypes[rowType] {
		return fmt.Errorf("%w: %q", ErrInvalidRowType, rowType)
	}
	return nil
}

// Create validates and inserts a new row, appended after every existing one
// (sort_order = current max + 1, or 0 for the first row), and returns the
// stored row with its assigned id and timestamps.
func (s *Store) Create(ctx context.Context, title string, rowType RowType, genreFilter string, enabled bool) (*Row, error) {
	if err := validate(title, rowType); err != nil {
		return nil, err
	}
	row := s.db.QueryRowContext(ctx, `
		INSERT INTO adult_newest_rows (title, row_type, genre_filter, sort_order, enabled, updated_at)
		VALUES (?, ?, ?, (SELECT COALESCE(MAX(sort_order), -1) + 1 FROM adult_newest_rows), ?, strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
		RETURNING id, sort_order, created_at, updated_at
	`, title, string(rowType), genreFilter, enabled)

	r := &Row{Title: title, RowType: rowType, GenreFilter: genreFilter, Enabled: enabled}
	if err := row.Scan(&r.ID, &r.SortOrder, &r.CreatedAt, &r.UpdatedAt); err != nil {
		return nil, fmt.Errorf("creating adult newest row %q: %w", title, err)
	}
	return r, nil
}

// Update validates and overwrites every editable field of the row with the
// given id. sort_order is untouched — reordering is Reorder's job, not
// Update's, matching discoversliders.Store.Update's convention.
func (s *Store) Update(ctx context.Context, id int, title string, rowType RowType, genreFilter string, enabled bool) (*Row, error) {
	if err := validate(title, rowType); err != nil {
		return nil, err
	}
	row := s.db.QueryRowContext(ctx, `
		UPDATE adult_newest_rows SET
			title = ?, row_type = ?, genre_filter = ?, enabled = ?,
			updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
		WHERE id = ?
		RETURNING id, sort_order, created_at, updated_at
	`, title, string(rowType), genreFilter, enabled, id)

	r := &Row{ID: id, Title: title, RowType: rowType, GenreFilter: genreFilter, Enabled: enabled}
	if err := row.Scan(&r.ID, &r.SortOrder, &r.CreatedAt, &r.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("updating adult newest row %d: %w", id, err)
	}
	return r, nil
}

// Delete removes the row with the given id. Deleting an id that doesn't
// exist is not an error — the end state is the same, matching
// discoversliders.Store.Delete's convention.
func (s *Store) Delete(ctx context.Context, id int) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM adult_newest_rows WHERE id = ?`, id); err != nil {
		return fmt.Errorf("deleting adult newest row %d: %w", id, err)
	}
	return nil
}

// List returns every row ordered by sort_order, ascending.
func (s *Store) List(ctx context.Context) ([]Row, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, title, row_type, genre_filter, sort_order, enabled, created_at, updated_at
		FROM adult_newest_rows
		ORDER BY sort_order ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("listing adult newest rows: %w", err)
	}
	defer rows.Close()

	// []Row{}, not var out []Row — an empty result should serialize as []
	// over the API, not null (matches discoversliders.Store.List's convention).
	out := []Row{}
	for rows.Next() {
		var r Row
		var rowType string
		if err := rows.Scan(&r.ID, &r.Title, &rowType, &r.GenreFilter, &r.SortOrder, &r.Enabled, &r.CreatedAt, &r.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scanning adult newest row: %w", err)
		}
		r.RowType = RowType(rowType)
		out = append(out, r)
	}
	return out, rows.Err()
}

// Reorder assigns sort_order 0..len(ids)-1 to the rows named by ids, in the
// given order. ids must contain exactly the ids of every existing row, each
// exactly once — matches discoversliders.Store.Reorder's convention exactly
// (one explicit "here is the new order" action on the full resource).
func (s *Store) Reorder(ctx context.Context, ids []int) error {
	existing, err := s.List(ctx)
	if err != nil {
		return fmt.Errorf("reordering adult newest rows: %w", err)
	}
	existingIDs := make(map[int]bool, len(existing))
	for _, r := range existing {
		existingIDs[r.ID] = true
	}
	if len(ids) != len(existingIDs) {
		return ErrReorderMismatch
	}
	seen := make(map[int]bool, len(ids))
	for _, id := range ids {
		if seen[id] || !existingIDs[id] {
			return ErrReorderMismatch
		}
		seen[id] = true
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("reordering adult newest rows: %w", err)
	}
	defer tx.Rollback()

	for i, id := range ids {
		if _, err := tx.ExecContext(ctx, `
			UPDATE adult_newest_rows SET sort_order = ?, updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
			WHERE id = ?
		`, i, id); err != nil {
			return fmt.Errorf("reordering adult newest row %d: %w", id, err)
		}
	}
	return tx.Commit()
}
