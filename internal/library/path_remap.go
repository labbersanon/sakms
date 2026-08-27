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

// Claude 2026-08-27: Browse tracked badges list child names, not every
//   descendant file_path.
// Reason: opening /media/Movies waited on the whole library tree; the
//   DISTINCT first segment is all the listing rows need.
// Troubleshooting: "Foo Two" showing as "Foo" — the match must stay
//   coveredPred (starts_with(dir+'/')), never LIKE dir+'%'. length(?) is
//   the SQL character length of the bound dir, not Go len, so multibyte
//   segments stay aligned with substr.
// Review if: hover prefetch lands or library_*_files become sole source.

// TrackedChildNames returns the distinct first path segment after dir/ among
// library-owned file_paths covered by dir, never nil. Browse merges these
// onto its disk-only listing to badge tracked rows.
func (s *Store) TrackedChildNames(ctx context.Context, dir string) ([]string, error) {
	if s == nil || s.db == nil || dir == "" {
		return []string{}, nil
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT DISTINCT name FROM (
			SELECT split_part(substr(file_path, length(?) + 2), '/', 1) AS name
			FROM (
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
			) t
		) x WHERE name <> ''
		ORDER BY name`,
		append([]any{dir}, pathArgs(dir, 12)...)...)
	if err != nil {
		return nil, fmt.Errorf("listing tracked child names under %q: %w", dir, err)
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out = append(out, name)
	}
	return out, rows.Err()
}

// TrackedPathsUnder returns every library-owned file_path that equals path
// or lives under it. Browse listing no longer pulls this full set (see
// TrackedChildNames); RemapPath / ForgetPath / HasTrackedUnder still share
// coveredPred.
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
// under it. GET /api/organize/browse/tracked uses it for the three
// browsable-root rows, which have no useful child names to list.
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

// Claude 2026-08-27: Browse properties pane lists library titles for a path.
// Reason: filesystem details alone do not tell the operator which movie /
//   episode / scene owns a tracked file (or how many titles sit under a
//   folder). Prefix match is the same coveredPred as RemapPath — /media/A
//   must not swallow /media/Apple.
// Troubleshooting: empty library on a Tracked row — check item_files /
//   episode_files / scene_files, not only the denormalized file_path column.
// Review if: library_*_files become the sole path source.

const libraryHitCap = 25

// LibraryHit is one tracked title that owns path (exact file) or lives
// under it (directory). Kind is "movie", "episode", or "scene".
type LibraryHit struct {
	Kind   string
	Mode   string
	ID     int64
	Title  string
	Series string
	Year   int
}

func coveredCol(col string) string {
	return `(` + col + ` = ? OR starts_with(` + col + `, ? || '/'))`
}

// LibraryHitsForPath returns a capped sample of library titles at path or
// under it, plus the uncapped distinct-title count.
func (s *Store) LibraryHitsForPath(ctx context.Context, path string) ([]LibraryHit, int, error) {
	if s == nil || s.db == nil || path == "" {
		return nil, 0, nil
	}
	movies, nMovies, err := s.movieHitsForPath(ctx, path)
	if err != nil {
		return nil, 0, err
	}
	episodes, nEps, err := s.episodeHitsForPath(ctx, path)
	if err != nil {
		return nil, 0, err
	}
	scenes, nScenes, err := s.sceneHitsForPath(ctx, path)
	if err != nil {
		return nil, 0, err
	}
	total := nMovies + nEps + nScenes
	hits := make([]LibraryHit, 0, libraryHitCap)
	for _, group := range [][]LibraryHit{movies, episodes, scenes} {
		remain := libraryHitCap - len(hits)
		if remain <= 0 {
			break
		}
		if len(group) > remain {
			group = group[:remain]
		}
		hits = append(hits, group...)
	}
	return hits, total, nil
}

func (s *Store) movieHitsForPath(ctx context.Context, path string) ([]LibraryHit, int, error) {
	n, err := s.countHits(ctx, `
		SELECT COUNT(*) FROM (
			SELECT i.id FROM library_items i WHERE `+coveredCol("i.file_path")+`
			UNION
			SELECT i.id FROM library_item_files f
			JOIN library_items i ON i.id = f.item_id
			WHERE `+coveredCol("f.file_path")+`
		) t`, path)
	if err != nil {
		return nil, 0, err
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, mode, title, year FROM (
			SELECT i.id, i.mode, i.title, i.year FROM library_items i
			WHERE `+coveredCol("i.file_path")+`
			UNION
			SELECT i.id, i.mode, i.title, i.year FROM library_item_files f
			JOIN library_items i ON i.id = f.item_id
			WHERE `+coveredCol("f.file_path")+`
		) t ORDER BY title LIMIT ?`,
		append(pathArgs(path, 4), libraryHitCap)...)
	if err != nil {
		return nil, 0, fmt.Errorf("listing movie hits for %q: %w", path, err)
	}
	defer rows.Close()
	var hits []LibraryHit
	for rows.Next() {
		var h LibraryHit
		if err := rows.Scan(&h.ID, &h.Mode, &h.Title, &h.Year); err != nil {
			return nil, 0, err
		}
		h.Kind = "movie"
		hits = append(hits, h)
	}
	return hits, n, rows.Err()
}

func (s *Store) episodeHitsForPath(ctx context.Context, path string) ([]LibraryHit, int, error) {
	n, err := s.countHits(ctx, `
		SELECT COUNT(*) FROM (
			SELECT e.id FROM library_episodes e WHERE `+coveredCol("e.file_path")+`
			UNION
			SELECT e.id FROM library_episode_files f
			JOIN library_episodes e ON e.id = f.episode_id
			WHERE `+coveredCol("f.file_path")+`
		) t`, path)
	if err != nil {
		return nil, 0, err
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, ep_title, season_number, episode_number, series_title, year FROM (
			SELECT e.id, e.title AS ep_title, e.season_number, e.episode_number,
			       s.title AS series_title, s.year
			FROM library_episodes e
			JOIN library_series s ON s.id = e.series_id
			WHERE `+coveredCol("e.file_path")+`
			UNION
			SELECT e.id, e.title, e.season_number, e.episode_number, s.title, s.year
			FROM library_episode_files f
			JOIN library_episodes e ON e.id = f.episode_id
			JOIN library_series s ON s.id = e.series_id
			WHERE `+coveredCol("f.file_path")+`
		) t ORDER BY series_title, season_number, episode_number LIMIT ?`,
		append(pathArgs(path, 4), libraryHitCap)...)
	if err != nil {
		return nil, 0, fmt.Errorf("listing episode hits for %q: %w", path, err)
	}
	defer rows.Close()
	var hits []LibraryHit
	for rows.Next() {
		var h LibraryHit
		var epTitle string
		var season, epNum int
		if err := rows.Scan(&h.ID, &epTitle, &season, &epNum, &h.Series, &h.Year); err != nil {
			return nil, 0, err
		}
		h.Kind = "episode"
		h.Mode = "series"
		if epTitle == "" {
			h.Title = fmt.Sprintf("S%02dE%02d", season, epNum)
		} else {
			h.Title = fmt.Sprintf("S%02dE%02d %s", season, epNum, epTitle)
		}
		hits = append(hits, h)
	}
	return hits, n, rows.Err()
}

func (s *Store) sceneHitsForPath(ctx context.Context, path string) ([]LibraryHit, int, error) {
	n, err := s.countHits(ctx, `
		SELECT COUNT(*) FROM (
			SELECT s.id FROM library_scenes s WHERE `+coveredCol("s.file_path")+`
			UNION
			SELECT s.id FROM library_scene_files f
			JOIN library_scenes s ON s.id = f.scene_id
			WHERE `+coveredCol("f.file_path")+`
		) t`, path)
	if err != nil {
		return nil, 0, err
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, title FROM (
			SELECT s.id, s.title FROM library_scenes s
			WHERE `+coveredCol("s.file_path")+`
			UNION
			SELECT s.id, s.title FROM library_scene_files f
			JOIN library_scenes s ON s.id = f.scene_id
			WHERE `+coveredCol("f.file_path")+`
		) t ORDER BY title LIMIT ?`,
		append(pathArgs(path, 4), libraryHitCap)...)
	if err != nil {
		return nil, 0, fmt.Errorf("listing scene hits for %q: %w", path, err)
	}
	defer rows.Close()
	var hits []LibraryHit
	for rows.Next() {
		var h LibraryHit
		if err := rows.Scan(&h.ID, &h.Title); err != nil {
			return nil, 0, err
		}
		h.Kind = "scene"
		h.Mode = "adult"
		hits = append(hits, h)
	}
	return hits, n, rows.Err()
}

// countHits runs a two-branch (denormalized column UNION *_files) count; both
// branches bind path twice, so every caller needs four args.
func (s *Store) countHits(ctx context.Context, q, path string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, q, pathArgs(path, 4)...).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("counting library hits for %q: %w", path, err)
	}
	return n, nil
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
