package library

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// ErrInvalidRating is returned when a rating is outside 0–5.
// 0 means unset; 1–5 are operator stars.
var ErrInvalidRating = errors.New("library: rating must be 0 through 5")

func validateRating(rating int) error {
	if rating < 0 || rating > 5 {
		return ErrInvalidRating
	}
	return nil
}

func rowsOrNotFound(res sql.Result, id int64, what string) error {
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("setting rating on %s %d: %w", what, id, err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetItemRating writes the operator star rating on one Movies library_items
// row. 0 clears it. Upsert does not touch this column.
func (s *Store) SetItemRating(ctx context.Context, id int64, rating int) error {
	if err := validateRating(rating); err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx, `
		UPDATE library_items
		SET rating = ?, updated_at = sakms_now()
		WHERE id = ?
	`, rating, id)
	if err != nil {
		return fmt.Errorf("setting rating on library item %d: %w", id, err)
	}
	return rowsOrNotFound(res, id, "library item")
}

// SetSceneRating writes the operator star rating on one Adult library_scenes
// row. 0 clears it. UpsertScene does not touch this column.
func (s *Store) SetSceneRating(ctx context.Context, id int64, rating int) error {
	if err := validateRating(rating); err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx, `
		UPDATE library_scenes
		SET rating = ?, updated_at = sakms_now()
		WHERE id = ?
	`, rating, id)
	if err != nil {
		return fmt.Errorf("setting rating on library scene %d: %w", id, err)
	}
	return rowsOrNotFound(res, id, "library scene")
}

// SetSeriesRating writes the operator star rating on one library_series row
// (show-level, not per-episode). 0 clears it. UpsertSeries does not touch
// this column.
func (s *Store) SetSeriesRating(ctx context.Context, id int64, rating int) error {
	if err := validateRating(rating); err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx, `
		UPDATE library_series
		SET rating = ?, updated_at = sakms_now()
		WHERE id = ?
	`, rating, id)
	if err != nil {
		return fmt.Errorf("setting rating on library series %d: %w", id, err)
	}
	return rowsOrNotFound(res, id, "library series")
}
