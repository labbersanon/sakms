package library

import (
	"context"
	"fmt"
)

// SetSeriesTMDBID assigns a real TMDB id to a series row that was stored with
// tmdb_id=0 (or repairs a wrong zero). Fails if another series already owns
// newTMDBID.
// Claude 2026-09-22: repair path for Ancient Aliens–style rows (NFO has IDs,
// library row does not). UpsertSeries keys on tmdb_id so 0→N needs UPDATE by id.
// Review if: movies gain the same zero-id repair.
func (s *Store) SetSeriesTMDBID(ctx context.Context, seriesID int64, newTMDBID, tvdbID int) error {
	if seriesID == 0 || newTMDBID <= 0 {
		return fmt.Errorf("library: SetSeriesTMDBID requires series id and positive tmdb id")
	}
	res, err := s.db.ExecContext(ctx, `
		UPDATE library_series
		SET tmdb_id = ?,
		    tvdb_id = CASE WHEN ? > 0 THEN ? ELSE tvdb_id END,
		    updated_at = sakms_now()
		WHERE id = ?
		  AND NOT EXISTS (
		    SELECT 1 FROM library_series o
		    WHERE o.tmdb_id = ? AND o.id <> ?
		  )
	`, newTMDBID, tvdbID, tvdbID, seriesID, newTMDBID, seriesID)
	if err != nil {
		return fmt.Errorf("setting series %d tmdb_id=%d: %w", seriesID, newTMDBID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("library: could not set tmdb_id=%d on series %d (conflict or missing)", newTMDBID, seriesID)
	}
	return nil
}

// ListSeriesNeedingIdentity returns series rows with a missing or non-positive
// tmdb_id. Poster listing only includes tmdb_id=0, so negative web-authority
// ids are invisible there.
func (s *Store) ListSeriesNeedingIdentity(ctx context.Context) ([]Series, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, tmdb_id, tvdb_id, title, year, root_folder_path
		FROM library_series
		WHERE tmdb_id <= 0
		ORDER BY title
	`)
	if err != nil {
		return nil, fmt.Errorf("listing series needing identity: %w", err)
	}
	defer rows.Close()
	var out []Series
	for rows.Next() {
		var ser Series
		if err := rows.Scan(&ser.ID, &ser.TMDBID, &ser.TVDBID, &ser.Title, &ser.Year, &ser.RootFolderPath); err != nil {
			return nil, fmt.Errorf("scanning series needing identity: %w", err)
		}
		out = append(out, ser)
	}
	return out, rows.Err()
}

// SeriesDirHint returns RootFolderPath for a series when set.
func (s *Store) SeriesDirHint(ctx context.Context, seriesID int64) (root string, title string, err error) {
	err = s.db.QueryRowContext(ctx, `
		SELECT root_folder_path, title FROM library_series WHERE id = ?
	`, seriesID).Scan(&root, &title)
	if err != nil {
		return "", "", err
	}
	return root, title, nil
}
