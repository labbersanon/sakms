package library

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// Rename Undo — the write side of reversing one Apply's library row
// (deep-interview-rename-undo, backend pass).
//
// Three explicit per-table restorers, deliberately NOT one reflection-based
// generic restorer: there are exactly three reversible tables and there will
// not be a fourth (library_series and library_item_files/library_episode_files
// are all out of scope for reversal — see internal/rename/undo_apply.go).
//
// Each is a plain UPDATE of every column the corresponding Upsert writes,
// keyed by id. They deliberately do NOT call SyncPrimaryFile (as Upsert does)
// or touch library_item_files/library_episode_files at all: undo reverses the
// denormalized primary path only. See revertTouchedRows' doc comment for the
// accepted consequence.

// RestoreItem writes every Upsert-owned column of item back, keyed by item.ID.
// Restoring a row that no longer exists is not an error (it affects zero rows),
// matching DeleteSeries' tolerance of a missing id.
func (s *Store) RestoreItem(ctx context.Context, item Item) error {
	genresJSON, err := marshalStringSlice(item.Genres)
	if err != nil {
		return fmt.Errorf("encoding genres for library item %d: %w", item.ID, err)
	}
	castJSON, err := marshalStringSlice(item.Cast)
	if err != nil {
		return fmt.Errorf("encoding cast for library item %d: %w", item.ID, err)
	}
	if _, err := s.db.ExecContext(ctx, `
		UPDATE library_items SET
			mode = ?, tmdb_id = ?, title = ?, year = ?, file_path = ?, root_folder_path = ?,
			phash = ?, phash_file_size = ?, phash_file_mtime = ?, genres = ?, "cast" = ?,
			size = ?, quality_tier = ?, updated_at = sakms_now()
		WHERE id = ?
	`, string(item.Mode), item.TMDBID, item.Title, item.Year, item.FilePath, item.RootFolderPath,
		item.PHash, item.PHashFileSize, item.PHashFileMTime, genresJSON, castJSON,
		item.Size, item.QualityTier, item.ID); err != nil {
		return fmt.Errorf("restoring library item %d: %w", item.ID, err)
	}
	return nil
}

// RestoreEpisode writes every UpsertEpisode-owned column of ep back, keyed by
// ep.ID. It never touches library_series — that row is shared by every episode
// of the show and is permanently out of scope for undo.
func (s *Store) RestoreEpisode(ctx context.Context, ep Episode) error {
	if _, err := s.db.ExecContext(ctx, `
		UPDATE library_episodes SET
			series_id = ?, season_number = ?, episode_number = ?, title = ?, air_date = ?,
			file_path = ?, phash = ?, phash_file_size = ?, phash_file_mtime = ?,
			size = ?, quality_tier = ?, updated_at = sakms_now()
		WHERE id = ?
	`, ep.SeriesID, ep.SeasonNumber, ep.EpisodeNumber, ep.Title, ep.AirDate,
		ep.FilePath, ep.PHash, ep.PHashFileSize, ep.PHashFileMTime,
		ep.Size, ep.QualityTier, ep.ID); err != nil {
		return fmt.Errorf("restoring library episode %d: %w", ep.ID, err)
	}
	return nil
}

// RestoreScene writes every UpsertScene-owned column of scene back, keyed by
// scene.ID. PHash/PHashFileSize/PHashFileMTime are restored alongside the rest
// on purpose: they are the phash cache's file-identity key, and putting the
// path back without them would leave Stage-2 Dedup trusting a hash computed for
// a different file size/mtime.
func (s *Store) RestoreScene(ctx context.Context, scene Scene) error {
	// Claude 2026-08-13: normalize missing/empty posterAspectClass to horizontal.
	// Reason: pre-migration undo snapshots have no posterAspectClass JSON key;
	//   writing "" would fail the CHECK constraint.
	// Review if: every snapshot is known to include the field.
	scene.PosterAspectClass = normalizePosterAspect(scene.PosterAspectClass)
	if _, err := s.db.ExecContext(ctx, `
		UPDATE library_scenes SET
			box = ?, scene_id = ?, title = ?, studio = ?, date = ?, file_path = ?,
			root_folder_path = ?, phash = ?, phash_file_size = ?, phash_file_mtime = ?,
			size = ?, quality_tier = ?, poster_aspect_class = ?, updated_at = sakms_now()
		WHERE id = ?
	`, scene.Box, scene.SceneID, scene.Title, scene.Studio, scene.Date, scene.FilePath,
		scene.RootFolderPath, scene.PHash, scene.PHashFileSize, scene.PHashFileMTime,
		scene.Size, scene.QualityTier, scene.PosterAspectClass, scene.ID); err != nil {
		return fmt.Errorf("restoring library scene %d: %w", scene.ID, err)
	}
	return nil
}

// DeleteEpisode removes ONE episode row — the non-transactional sibling of
// DeleteEpisodeTx, for undo's "this Apply INSERTed the row, so reversing it
// means deleting it" path. Same single-row, single-statement contract:
// it is NOT DeleteSeries and must never be implemented in terms of it
// (DeleteSeries cascades an entire show's tracking history).
// library_episode_files.episode_id is ON DELETE CASCADE, so that dependent
// clears itself. Deleting an id that doesn't exist is not an error.
func (s *Store) DeleteEpisode(ctx context.Context, episodeID int64) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM library_episodes WHERE id = ?`, episodeID); err != nil {
		return fmt.Errorf("deleting episode %d: %w", episodeID, err)
	}
	return nil
}

// GetEpisodeByID returns one episode by primary key, or ErrNotFound. Undo needs
// it for drift detection: the (series_id, season, episode) key GetEpisode takes
// is exactly what a later Apply may have rewritten, so only the id is stable.
func (s *Store) GetEpisodeByID(ctx context.Context, episodeID int64) (*Episode, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, series_id, season_number, episode_number, title, air_date, file_path, phash, phash_file_size, phash_file_mtime, created_at, updated_at, size, quality_tier
		FROM library_episodes WHERE id = ?
	`, episodeID)
	ep, err := scanEpisode(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("loading episode %d: %w", episodeID, err)
	}
	return &ep, nil
}

// GetSceneByID returns one scene by primary key, or ErrNotFound. Undo's
// counterpart to GetEpisodeByID, and for the same reason: the (box, scene_id)
// key GetScene takes is itself restorable state.
func (s *Store) GetSceneByID(ctx context.Context, sceneID int64) (*Scene, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, box, scene_id, title, studio, date, file_path, root_folder_path, phash, phash_file_size, phash_file_mtime, created_at, updated_at, size, quality_tier, poster_aspect_class
		FROM library_scenes WHERE id = ?
	`, sceneID)
	scene, err := scanScene(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("loading scene %d: %w", sceneID, err)
	}
	return &scene, nil
}
