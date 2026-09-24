package library

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/labbersanon/sakms/internal/mode"
)

// SetMovieTMDBID rekeys a library_items row by id to a real positive TMDB id
// (and optional title/year). Used to repair tmdb_id=0 / negative web-authority
// rows after embedded-tag or NFO identity succeeds.
// Claude 2026-09-23: movie twin of SetSeriesTMDBID for embedded-tag repair.
// Review if: also clears synthetic web-authority title when details disagree.
func (s *Store) SetMovieTMDBID(ctx context.Context, itemID int64, newTMDBID int, title string, year int) error {
	if itemID == 0 || newTMDBID <= 0 {
		return fmt.Errorf("library: SetMovieTMDBID requires item id and positive tmdb id")
	}
	res, err := s.db.ExecContext(ctx, `
		UPDATE library_items
		SET tmdb_id = ?,
		    title = CASE WHEN ? <> '' THEN ? ELSE title END,
		    year = CASE WHEN ? > 0 THEN ? ELSE year END,
		    updated_at = sakms_now()
		WHERE id = ?
		  AND mode = ?
		  AND NOT EXISTS (
		    SELECT 1 FROM library_items o
		    WHERE o.mode = library_items.mode AND o.tmdb_id = ? AND o.id <> ?
		  )
	`, newTMDBID, title, title, year, year, itemID, string(mode.Movies), newTMDBID, itemID)
	if err != nil {
		return fmt.Errorf("setting movie %d tmdb_id=%d: %w", itemID, newTMDBID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("library: could not set tmdb_id=%d on movie item %d (conflict or missing)", newTMDBID, itemID)
	}
	return nil
}

// RematchMovie rekeys one Movies row. Same-row rematch still updates title/year.
// Claude 2026-09-24: owned-detail Rematch; row id stays stable for play URLs.
func (s *Store) RematchMovie(ctx context.Context, itemID int64, newTMDBID int, title string, year int) error {
	if itemID == 0 || newTMDBID <= 0 || strings.TrimSpace(title) == "" {
		return fmt.Errorf("library: RematchMovie requires item id, positive tmdb id, and title")
	}
	other, err := s.GetByTMDBID(ctx, mode.Movies, newTMDBID)
	if err == nil && other != nil && other.ID != itemID {
		return ErrIdentityConflict
	}
	if err != nil && !errors.Is(err, ErrNotFound) {
		return err
	}
	res, err := s.db.ExecContext(ctx, `
		UPDATE library_items
		SET tmdb_id = ?, title = ?,
		    year = CASE WHEN ? > 0 THEN ? ELSE year END,
		    updated_at = sakms_now()
		WHERE id = ? AND mode = ?
	`, newTMDBID, title, year, year, itemID, string(mode.Movies))
	if err != nil {
		return fmt.Errorf("rematching movie %d to tmdb_id=%d: %w", itemID, newTMDBID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// ListMoviesNeedingIdentity returns Movies rows with missing or non-positive
// tmdb_id (shared zero row and web-authority synthetics).
func (s *Store) ListMoviesNeedingIdentity(ctx context.Context) ([]Item, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, tmdb_id, title, year, file_path, root_folder_path
		FROM library_items
		WHERE mode = ? AND tmdb_id <= 0
		ORDER BY title
	`, string(mode.Movies))
	if err != nil {
		return nil, fmt.Errorf("listing movies needing identity: %w", err)
	}
	defer rows.Close()
	var out []Item
	for rows.Next() {
		var item Item
		item.Mode = mode.Movies
		if err := rows.Scan(&item.ID, &item.TMDBID, &item.Title, &item.Year, &item.FilePath, &item.RootFolderPath); err != nil {
			return nil, fmt.Errorf("scanning movie needing identity: %w", err)
		}
		out = append(out, item)
	}
	return out, rows.Err()
}
