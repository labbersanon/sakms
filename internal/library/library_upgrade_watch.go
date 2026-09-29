package library

import (
	"context"
	"fmt"

	"github.com/labbersanon/sakms/internal/mode"
)

// SetItemUpgradeWatch writes the opt-in "watch for a better release" flag on
// one Movies library_items row. Upsert does not touch this column.
func (s *Store) SetItemUpgradeWatch(ctx context.Context, id int64, watch bool) error {
	res, err := s.db.ExecContext(ctx, `
		UPDATE library_items
		SET upgrade_watch = ?, updated_at = sakms_now()
		WHERE id = ?
	`, watch, id)
	if err != nil {
		return fmt.Errorf("setting upgrade watch on library item %d: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("setting upgrade watch on library item %d: %w", id, err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// ListUpgradeWatchMovies returns every Movies row whose upgrade_watch flag is
// on, ordered by title. Used by the daily retry-cycle pass; not a UI list.
func (s *Store) ListUpgradeWatchMovies(ctx context.Context) ([]Item, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT li.id, li.mode, li.tmdb_id, li.title, li.year, li.file_path, li.root_folder_path,
		       li.phash, li.phash_file_size, li.phash_file_mtime, li.created_at, li.updated_at,
		       COALESCE(c.tmdb_collection_id, 0), COALESCE(c.name, ''),
		       COALESCE(li.genres, '[]'), COALESCE(li."cast", '[]'),
		       li.size, li.quality_tier, li.rating, li.upgrade_watch
		FROM library_items li
		LEFT JOIN library_collections c ON c.id = li.collection_id
		WHERE li.mode = ? AND li.upgrade_watch = true
		ORDER BY li.title
	`, string(mode.Movies))
	if err != nil {
		return nil, fmt.Errorf("listing upgrade-watch movies: %w", err)
	}
	defer rows.Close()

	out := []Item{}
	for rows.Next() {
		item, err := scanItem(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning upgrade-watch movie: %w", err)
		}
		out = append(out, item)
	}
	return out, rows.Err()
}
