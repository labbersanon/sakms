package library

import (
	"context"
	"fmt"
	"strings"
)

// Claude 2026-08-27: Organize Browse keeps library paths in lockstep with
//   the filesystem (rename/move/delete).
// Reason: a file-manager tab that moved bytes and left library_items pointing
//   at the old path would recreate the drift SAK exists to prevent.
// Troubleshooting: after Browse rename the Library still shows the old path
//   — RemapPath must run AFTER a successful os.Rename; ForgetPath AFTER
//   os.Remove/RemoveAll. Prefix match uses starts_with(path+'/'), never
//   LIKE path+'%', so /media/A does not swallow /media/Apple.
// Review if: library_*_files become the sole path source (drop denormalized
//   file_path columns).

const coveredPred = `(file_path = ? OR starts_with(file_path, ? || '/'))`

// pathArgs repeats path n times — coveredPred binds it twice, so a query
// with six UNION branches needs twelve.
func pathArgs(path string, n int) []any {
	args := make([]any, n)
	for i := range args {
		args[i] = path
	}
	return args
}

// TrackedPathsUnder returns every library-owned file_path that equals path
// or lives under it. Used to mark Browse listing rows as tracked.
func (s *Store) TrackedPathsUnder(ctx context.Context, path string) ([]string, error) {
	if s == nil || s.db == nil || path == "" {
		return nil, nil
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT DISTINCT file_path FROM (
			SELECT file_path FROM library_items WHERE `+coveredPred+`
			UNION
			SELECT file_path FROM library_item_files WHERE `+coveredPred+`
			UNION
			SELECT file_path FROM library_episodes WHERE `+coveredPred+`
			UNION
			SELECT file_path FROM library_episode_files WHERE `+coveredPred+`
			UNION
			SELECT file_path FROM library_scenes WHERE `+coveredPred+`
			UNION
			SELECT file_path FROM library_scene_files WHERE `+coveredPred+`
		) t WHERE file_path <> ''`,
		pathArgs(path, 12)...)
	if err != nil {
		return nil, fmt.Errorf("listing tracked paths under %q: %w", path, err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// HasTrackedUnder reports whether any library-owned file lives at path or
// under it. Used for the three browsable-root rows so listing /media does
// not pull every tracked path in the library.
func (s *Store) HasTrackedUnder(ctx context.Context, path string) (bool, error) {
	if s == nil || s.db == nil || path == "" {
		return false, nil
	}
	var ok bool
	err := s.db.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM library_items WHERE `+coveredPred+`
			UNION ALL
			SELECT 1 FROM library_item_files WHERE `+coveredPred+`
			UNION ALL
			SELECT 1 FROM library_episodes WHERE `+coveredPred+`
			UNION ALL
			SELECT 1 FROM library_episode_files WHERE `+coveredPred+`
			UNION ALL
			SELECT 1 FROM library_scenes WHERE `+coveredPred+`
			UNION ALL
			SELECT 1 FROM library_scene_files WHERE `+coveredPred+`
		)`,
		pathArgs(path, 12)...).Scan(&ok)
	if err != nil {
		return false, fmt.Errorf("checking tracked paths under %q: %w", path, err)
	}
	return ok, nil
}

// RemapPath rewrites every library file_path from oldPath to newPath (exact
// match or children of a renamed/moved directory). Returns whether any row
// was updated.
func (s *Store) RemapPath(ctx context.Context, oldPath, newPath string) (bool, error) {
	if s == nil || s.db == nil || oldPath == "" || newPath == "" || oldPath == newPath {
		return false, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("remapping %q → %q: %w", oldPath, newPath, err)
	}
	defer tx.Rollback()

	n := int64(0)
	tables := []string{
		"library_items",
		"library_item_files",
		"library_episodes",
		"library_episode_files",
		"library_scenes",
		"library_scene_files",
	}
	for _, table := range tables {
		res, err := tx.ExecContext(ctx, `
			UPDATE `+table+`
			SET file_path = ? || substring(file_path from length(?) + 1),
			    updated_at = sakms_now()
			WHERE `+coveredPred, newPath, oldPath, oldPath, oldPath)
		if err != nil {
			return false, fmt.Errorf("remapping %s %q → %q: %w", table, oldPath, newPath, err)
		}
		c, _ := res.RowsAffected()
		n += c
	}

	res, err := tx.ExecContext(ctx, `
		UPDATE vmaf_scores
		SET candidate_path = CASE
			WHEN candidate_path = ? OR starts_with(candidate_path, ? || '/')
				THEN ? || substring(candidate_path from length(?) + 1)
			ELSE candidate_path END,
		    reference_path = CASE
			WHEN reference_path = ? OR starts_with(reference_path, ? || '/')
				THEN ? || substring(reference_path from length(?) + 1)
			ELSE reference_path END
		WHERE candidate_path = ? OR starts_with(candidate_path, ? || '/')
		   OR reference_path = ? OR starts_with(reference_path, ? || '/')`,
		oldPath, oldPath, newPath, oldPath,
		oldPath, oldPath, newPath, oldPath,
		oldPath, oldPath, oldPath, oldPath)
	if err != nil {
		return false, fmt.Errorf("remapping vmaf_scores %q → %q: %w", oldPath, newPath, err)
	}
	c, _ := res.RowsAffected()
	n += c

	res, err = tx.ExecContext(ctx, `
		UPDATE orphan_phashes
		SET path = ? || substring(path from length(?) + 1)
		WHERE path = ? OR starts_with(path, ? || '/')`,
		newPath, oldPath, oldPath, oldPath)
	if err != nil {
		return false, fmt.Errorf("remapping orphan_phashes %q → %q: %w", oldPath, newPath, err)
	}
	c, _ = res.RowsAffected()
	n += c

	if err := tx.Commit(); err != nil {
		return false, err
	}
	return n > 0, nil
}

// ForgetPath drops library records for a deleted file, or every file under
// a deleted directory. Movies/scenes with no remaining files are removed;
// episode rows stay (missing episode is a valid state) with file_path cleared.
func (s *Store) ForgetPath(ctx context.Context, path string) (bool, error) {
	if s == nil || s.db == nil || path == "" {
		return false, nil
	}
	touched := false

	itemIDs, err := queryIDs(ctx, s, `
		SELECT id FROM library_items WHERE `+coveredPred+`
		UNION
		SELECT item_id FROM library_item_files WHERE `+coveredPred, path)
	if err != nil {
		return false, err
	}
	epIDs, err := queryIDs(ctx, s, `
		SELECT id FROM library_episodes WHERE `+coveredPred+`
		UNION
		SELECT episode_id FROM library_episode_files WHERE `+coveredPred, path)
	if err != nil {
		return false, err
	}
	sceneIDs, err := queryIDs(ctx, s, `
		SELECT id FROM library_scenes WHERE `+coveredPred+`
		UNION
		SELECT scene_id FROM library_scene_files WHERE `+coveredPred, path)
	if err != nil {
		return false, err
	}

	deletes := []struct {
		query string
		args  []any
	}{
		{`DELETE FROM library_item_files WHERE ` + coveredPred, pathArgs(path, 2)},
		{`DELETE FROM library_episode_files WHERE ` + coveredPred, pathArgs(path, 2)},
		{`DELETE FROM library_scene_files WHERE ` + coveredPred, pathArgs(path, 2)},
		{`DELETE FROM vmaf_scores WHERE candidate_path = ? OR starts_with(candidate_path, ? || '/')
			OR reference_path = ? OR starts_with(reference_path, ? || '/')`, pathArgs(path, 4)},
		{`DELETE FROM orphan_phashes WHERE path = ? OR starts_with(path, ? || '/')`, pathArgs(path, 2)},
	}
	for _, d := range deletes {
		res, err := s.db.ExecContext(ctx, d.query, d.args...)
		if err != nil {
			return false, fmt.Errorf("forgetting path %q: %w", path, err)
		}
		if c, _ := res.RowsAffected(); c > 0 {
			touched = true
		}
	}

	for _, id := range itemIDs {
		changed, err := s.forgetItemAfterDelete(ctx, id)
		if err != nil {
			return false, err
		}
		touched = touched || changed
	}
	for _, id := range epIDs {
		changed, err := s.forgetEpisodeAfterDelete(ctx, id)
		if err != nil {
			return false, err
		}
		touched = touched || changed
	}
	for _, id := range sceneIDs {
		changed, err := s.forgetSceneAfterDelete(ctx, id)
		if err != nil {
			return false, err
		}
		touched = touched || changed
	}
	return touched, nil
}

func queryIDs(ctx context.Context, s *Store, q, path string) ([]int64, error) {
	rows, err := s.db.QueryContext(ctx, q, pathArgs(path, 4)...)
	if err != nil {
		return nil, fmt.Errorf("listing ids for %q: %w", path, err)
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (s *Store) forgetItemAfterDelete(ctx context.Context, id int64) (bool, error) {
	files, err := s.ListFiles(ctx, id)
	if err != nil {
		return false, err
	}
	if len(files) == 0 {
		if err := s.Delete(ctx, id); err != nil {
			return false, err
		}
		return true, nil
	}
	item, err := s.Get(ctx, id)
	if err != nil {
		return false, err
	}
	primary := files[0]
	for _, f := range files {
		if f.IsPrimary {
			primary = f
			break
		}
	}
	if item.FilePath == primary.FilePath {
		return false, nil
	}
	if err := s.UpdateItemPrimaryPath(ctx, id, primary.FilePath, primary.QualityTier, primary.Size); err != nil {
		return false, err
	}
	if !primary.IsPrimary {
		if _, err := s.db.ExecContext(ctx, `
			UPDATE library_item_files SET is_primary = (id = ?), updated_at = sakms_now()
			WHERE item_id = ?`, primary.ID, id); err != nil {
			return false, err
		}
	}
	return true, nil
}

func (s *Store) forgetEpisodeAfterDelete(ctx context.Context, id int64) (bool, error) {
	files, err := s.ListEpisodeFiles(ctx, id)
	if err != nil {
		return false, err
	}
	ep, err := s.GetEpisodeByID(ctx, id)
	if err != nil {
		return false, err
	}
	if len(files) == 0 {
		if ep.FilePath == "" {
			return false, nil
		}
		if err := s.UpdateEpisodePrimaryPath(ctx, id, "", "", 0); err != nil {
			return false, err
		}
		return true, nil
	}
	primary := files[0]
	for _, f := range files {
		if f.IsPrimary {
			primary = f
			break
		}
	}
	if ep.FilePath == primary.FilePath {
		return false, nil
	}
	if err := s.UpdateEpisodePrimaryPath(ctx, id, primary.FilePath, primary.QualityTier, primary.Size); err != nil {
		return false, err
	}
	return true, nil
}

func (s *Store) forgetSceneAfterDelete(ctx context.Context, id int64) (bool, error) {
	files, err := s.ListSceneFiles(ctx, id)
	if err != nil {
		return false, err
	}
	if len(files) == 0 {
		if err := s.DeleteScene(ctx, id); err != nil {
			return false, err
		}
		return true, nil
	}
	scene, err := s.GetSceneByID(ctx, id)
	if err != nil {
		return false, err
	}
	primary := files[0]
	for _, f := range files {
		if f.IsPrimary {
			primary = f
			break
		}
	}
	if scene.FilePath == primary.FilePath {
		return false, nil
	}
	if err := s.UpdateScenePrimaryPath(ctx, id, primary.FilePath, primary.QualityTier, primary.Size, primary.PHash, 0, ""); err != nil {
		return false, err
	}
	return true, nil
}

// EntryTracked reports whether listing entry at fullPath (a file or dir
// under dir) is tracked, given the tracked file paths already loaded for dir.
func EntryTracked(fullPath string, isDir bool, tracked []string) bool {
	if isDir {
		prefix := fullPath + "/"
		for _, p := range tracked {
			if p == fullPath || strings.HasPrefix(p, prefix) {
				return true
			}
		}
		return false
	}
	for _, p := range tracked {
		if p == fullPath {
			return true
		}
	}
	return false
}
