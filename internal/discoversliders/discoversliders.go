// Package discoversliders persists admin-defined custom Discover sliders —
// Seerr's CreateSlider/DiscoverSliderEdit equivalent. A slider is a saved
// filter (genre/keyword/studio/network, or one of the fixed upcoming/
// trending/popular feeds) plus a target (movie/tv/mixed) and a display
// position, rendered as one more row on the Discover screen alongside the
// built-in ones. This package is persistence + validation only — no HTTP
// handlers and no internal/apidto types; the API layer maps its own DTOs
// onto Slider.
package discoversliders

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// ErrNotFound is returned by Update/Delete when id has no stored slider.
var ErrNotFound = errors.New("discoversliders: no slider with that id")

// ErrInvalidFilterType is returned when FilterType isn't one of the fixed enum values.
var ErrInvalidFilterType = errors.New("discoversliders: invalid filter type")

// ErrInvalidTarget is returned when Target isn't one of the fixed enum values.
var ErrInvalidTarget = errors.New("discoversliders: invalid target")

// ErrTitleRequired is returned when Title is blank.
var ErrTitleRequired = errors.New("discoversliders: title is required")

// ErrFilterValueRequired is returned when FilterType needs a FilterValue
// (genre/keyword/studio/network) but none was given.
var ErrFilterValueRequired = errors.New("discoversliders: filter value is required for this filter type")

// ErrFilterValueNotAllowed is returned when FilterType is one of the fixed
// feeds (upcoming/trending/popular) but a FilterValue was given anyway —
// those feeds take no id/text filter, so a non-empty value here almost
// always indicates the caller picked the wrong filter type.
var ErrFilterValueNotAllowed = errors.New("discoversliders: filter value is not allowed for this filter type")

// ErrReorderMismatch is returned by Reorder when the given ids don't cover
// exactly the same set of existing sliders — a partial or stale id list
// would otherwise silently strand the omitted sliders at their old
// sort_order instead of a well-defined position.
var ErrReorderMismatch = errors.New("discoversliders: reorder ids must match the full set of existing sliders exactly")

// FilterType is the fixed set of ways a slider selects titles from TMDB/TPDB.
type FilterType string

const (
	FilterGenre    FilterType = "genre"
	FilterKeyword  FilterType = "keyword"
	FilterStudio   FilterType = "studio"
	FilterNetwork  FilterType = "network"
	FilterUpcoming FilterType = "upcoming"
	FilterTrending FilterType = "trending"
	FilterPopular  FilterType = "popular"
)

// filterValueRequired is the subset of FilterType that identifies titles by
// an id/text FilterValue (a genre id, keyword, studio id, or network id).
// The rest (upcoming/trending/popular) are fixed feeds with no such value.
var filterValueRequired = map[FilterType]bool{
	FilterGenre:   true,
	FilterKeyword: true,
	FilterStudio:  true,
	FilterNetwork: true,
}

var validFilterTypes = map[FilterType]bool{
	FilterGenre:    true,
	FilterKeyword:  true,
	FilterStudio:   true,
	FilterNetwork:  true,
	FilterUpcoming: true,
	FilterTrending: true,
	FilterPopular:  true,
}

// Target restricts a slider's results to movies, TV, or both.
type Target string

const (
	TargetMovie Target = "movie"
	TargetTV    Target = "tv"
	TargetMixed Target = "mixed"
)

var validTargets = map[Target]bool{
	TargetMovie: true,
	TargetTV:    true,
	TargetMixed: true,
}

// Slider is one admin-defined Discover row.
type Slider struct {
	ID          int
	Title       string
	FilterType  FilterType
	FilterValue string
	Target      Target
	SortOrder   int
	Enabled     bool
	CreatedAt   string
	UpdatedAt   string
}

// Store persists Discover sliders against a database.
type Store struct {
	db *sql.DB
}

func New(db *sql.DB) *Store {
	return &Store{db: db}
}

// validate checks title/filterType/filterValue/target against the fixed
// enums and the filter-type/filter-value pairing rule shared by Create and
// Update.
func validate(title string, filterType FilterType, filterValue string, target Target) error {
	if title == "" {
		return ErrTitleRequired
	}
	if !validFilterTypes[filterType] {
		return fmt.Errorf("%w: %q", ErrInvalidFilterType, filterType)
	}
	if !validTargets[target] {
		return fmt.Errorf("%w: %q", ErrInvalidTarget, target)
	}
	if filterValueRequired[filterType] {
		if filterValue == "" {
			return fmt.Errorf("%w: %q", ErrFilterValueRequired, filterType)
		}
	} else if filterValue != "" {
		return fmt.Errorf("%w: %q", ErrFilterValueNotAllowed, filterType)
	}
	return nil
}

// Create validates and inserts a new slider, appended after every existing
// one (sort_order = current max + 1, or 0 for the first slider), and
// returns the stored row with its assigned id and timestamps.
func (s *Store) Create(ctx context.Context, title string, filterType FilterType, filterValue string, target Target, enabled bool) (*Slider, error) {
	if err := validate(title, filterType, filterValue, target); err != nil {
		return nil, err
	}
	row := s.db.QueryRowContext(ctx, `
		INSERT INTO discover_sliders (title, filter_type, filter_value, target, sort_order, enabled, updated_at)
		VALUES (?, ?, ?, ?, (SELECT COALESCE(MAX(sort_order), -1) + 1 FROM discover_sliders), ?, sakms_now())
		RETURNING id, sort_order, created_at, updated_at
	`, title, string(filterType), filterValue, string(target), enabled)

	sl := &Slider{Title: title, FilterType: filterType, FilterValue: filterValue, Target: target, Enabled: enabled}
	if err := row.Scan(&sl.ID, &sl.SortOrder, &sl.CreatedAt, &sl.UpdatedAt); err != nil {
		return nil, fmt.Errorf("creating slider %q: %w", title, err)
	}
	return sl, nil
}

// Update validates and overwrites every editable field of the slider with
// the given id. sort_order is untouched — reordering is Reorder's job, not
// Update's, so an editor form save never has to know the current order.
func (s *Store) Update(ctx context.Context, id int, title string, filterType FilterType, filterValue string, target Target, enabled bool) (*Slider, error) {
	if err := validate(title, filterType, filterValue, target); err != nil {
		return nil, err
	}
	row := s.db.QueryRowContext(ctx, `
		UPDATE discover_sliders SET
			title = ?, filter_type = ?, filter_value = ?, target = ?, enabled = ?,
			updated_at = sakms_now()
		WHERE id = ?
		RETURNING id, sort_order, created_at, updated_at
	`, title, string(filterType), filterValue, string(target), enabled, id)

	sl := &Slider{ID: id, Title: title, FilterType: filterType, FilterValue: filterValue, Target: target, Enabled: enabled}
	if err := row.Scan(&sl.ID, &sl.SortOrder, &sl.CreatedAt, &sl.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("updating slider %d: %w", id, err)
	}
	return sl, nil
}

// Delete removes the slider with the given id. Deleting an id that doesn't
// exist is not an error — the end state is the same, matching
// connections.Store.Delete's convention.
func (s *Store) Delete(ctx context.Context, id int) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM discover_sliders WHERE id = ?`, id); err != nil {
		return fmt.Errorf("deleting slider %d: %w", id, err)
	}
	return nil
}

// List returns every slider ordered by sort_order, ascending.
func (s *Store) List(ctx context.Context) ([]Slider, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, title, filter_type, filter_value, target, sort_order, enabled, created_at, updated_at
		FROM discover_sliders
		ORDER BY sort_order ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("listing sliders: %w", err)
	}
	defer rows.Close()

	// []Slider{}, not var out []Slider — a blank install's "no custom
	// sliders yet" should serialize as [] over the API, not null (see
	// connections.Store.List's identical convention).
	out := []Slider{}
	for rows.Next() {
		var sl Slider
		var filterType, target string
		if err := rows.Scan(&sl.ID, &sl.Title, &filterType, &sl.FilterValue, &target, &sl.SortOrder, &sl.Enabled, &sl.CreatedAt, &sl.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scanning slider: %w", err)
		}
		sl.FilterType = FilterType(filterType)
		sl.Target = Target(target)
		out = append(out, sl)
	}
	return out, rows.Err()
}

// Reorder assigns sort_order 0..len(ids)-1 to the sliders named by ids, in
// the given order. ids must contain exactly the ids of every existing
// slider, each exactly once — this is one explicit "here is the new order"
// action on the full resource, not a bulk per-item mutation, consistent
// with this project's staged-for-approval-one-item convention (there is no
// per-item bulk create/delete).
func (s *Store) Reorder(ctx context.Context, ids []int) error {
	existing, err := s.List(ctx)
	if err != nil {
		return fmt.Errorf("reordering sliders: %w", err)
	}
	existingIDs := make(map[int]bool, len(existing))
	for _, sl := range existing {
		existingIDs[sl.ID] = true
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
		return fmt.Errorf("reordering sliders: %w", err)
	}
	defer tx.Rollback()

	for i, id := range ids {
		if _, err := tx.ExecContext(ctx, `
			UPDATE discover_sliders SET sort_order = ?, updated_at = sakms_now()
			WHERE id = ?
		`, i, id); err != nil {
			return fmt.Errorf("reordering slider %d: %w", id, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("reordering sliders: %w", err)
	}
	return nil
}
