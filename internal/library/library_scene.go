package library

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// Scene is one tracked Adult scene — a flat, one-row-per-file thing like
// Item (a scene has no "missing episode" concept), but with its own table
// and no Mode field, following Series' dedicated-table precedent since this
// table only ever holds Scenes.
//
// A scene's only stable identity is a stash-box UUID, so it's keyed on
// (Box, SceneID) as SEPARATE columns, not a collapsed opaque string: a
// StashDB match and a FansDB match both yield raw UUIDs in the same shape,
// and give-back needs to know which box a scene came from. See internal/
// servarr and internal/identify for why.
type Scene struct {
	ID             int64  `json:"id"`
	Box            string `json:"box"`
	SceneID        string `json:"sceneId"`
	Title          string `json:"title"`
	Studio         string `json:"studio,omitempty"`
	Date           string `json:"date,omitempty"`
	FilePath       string `json:"filePath"`
	RootFolderPath string `json:"rootFolderPath"`
	// PHash is the SAK-computed perceptual hash of this scene's video file,
	// cached so Dedup decodes each tracked file once rather than every Scan.
	// PHashFileSize/PHashFileMTime are the file-identity key it's valid for:
	// the cache is trusted only if the current file's os.Stat size+mtime still
	// match, which detects a replaced/re-encoded file at the same path. Empty/
	// zero means "not computed yet" — recomputed lazily on the next Dedup Scan.
	// Unlike Series (which added phash later in a separate migration), Adult is
	// greenfield and carries these columns from its initial migration, since
	// Dedup needs them from day one.
	PHash          string `json:"phash,omitempty"`
	PHashFileSize  int64  `json:"phashFileSize,omitempty"`
	PHashFileMTime string `json:"phashFileMtime,omitempty"`
	CreatedAt      string `json:"createdAt"`
	UpdatedAt      string `json:"updatedAt"`
}

// UpsertScene creates a scene, or updates it if one already exists for the
// same (box, scene_id) pair — mirrors Upsert's re-entrant "this is now what
// I have" shape, used by the one-time Whisparr importer and Rename/Search's
// get-or-create-by-identity calls.
func (s *Store) UpsertScene(ctx context.Context, scene Scene) (Scene, error) {
	row := s.db.QueryRowContext(ctx, `
		INSERT INTO library_scenes (box, scene_id, title, studio, date, file_path, root_folder_path, phash, phash_file_size, phash_file_mtime)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(box, scene_id) DO UPDATE SET
			title = excluded.title,
			studio = excluded.studio,
			date = excluded.date,
			file_path = excluded.file_path,
			root_folder_path = excluded.root_folder_path,
			phash = excluded.phash,
			phash_file_size = excluded.phash_file_size,
			phash_file_mtime = excluded.phash_file_mtime,
			updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
		RETURNING id, created_at, updated_at
	`, scene.Box, scene.SceneID, scene.Title, scene.Studio, scene.Date, scene.FilePath, scene.RootFolderPath, scene.PHash, scene.PHashFileSize, scene.PHashFileMTime)

	if err := row.Scan(&scene.ID, &scene.CreatedAt, &scene.UpdatedAt); err != nil {
		return Scene{}, fmt.Errorf("upserting scene %q: %w", scene.Title, err)
	}
	return scene, nil
}

// GetScene looks up a scene by its (box, scene_id) identity, or ErrNotFound
// if no such row exists yet — the duplicate-detection/get-or-create key
// Rename, Search, and the Whisparr importer use, the direct analogue of
// GetSeriesByTMDBID with the key swapped.
func (s *Store) GetScene(ctx context.Context, box, sceneID string) (*Scene, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, box, scene_id, title, studio, date, file_path, root_folder_path, phash, phash_file_size, phash_file_mtime, created_at, updated_at
		FROM library_scenes WHERE box = ? AND scene_id = ?
	`, box, sceneID)
	scene, err := scanScene(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("loading scene for box %q scene id %q: %w", box, sceneID, err)
	}
	return &scene, nil
}

// ListScenes returns every tracked scene, ordered by title.
func (s *Store) ListScenes(ctx context.Context) ([]Scene, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, box, scene_id, title, studio, date, file_path, root_folder_path, phash, phash_file_size, phash_file_mtime, created_at, updated_at
		FROM library_scenes ORDER BY title
	`)
	if err != nil {
		return nil, fmt.Errorf("listing scenes: %w", err)
	}
	defer rows.Close()

	out := []Scene{}
	for rows.Next() {
		scene, err := scanScene(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning scene: %w", err)
		}
		out = append(out, scene)
	}
	return out, rows.Err()
}

// DeleteScene permanently removes scene id and its tags. Explicit two-
// statement delete rather than relying on the schema's declared foreign
// keys — same reasoning as Store.Delete: SQLite only enforces them when a
// connection has run `PRAGMA foreign_keys = ON`, which internal/db's shared
// Open doesn't set. Deleting an id that doesn't exist is not an error.
func (s *Store) DeleteScene(ctx context.Context, id int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("deleting scene %d: %w", id, err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `DELETE FROM library_scene_tags WHERE scene_id = ?`, id); err != nil {
		return fmt.Errorf("deleting tags for scene %d: %w", id, err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM library_scenes WHERE id = ?`, id); err != nil {
		return fmt.Errorf("deleting scene %d: %w", id, err)
	}
	return tx.Commit()
}

// UpdateScenePHash writes a freshly-computed perceptual hash and its file-
// identity key (size + mtime) onto an existing tracked scene, without
// rewriting the rest of the row — the targeted write Dedup's Scan uses to
// cache a tracked scene's hash mid-scan. Kept separate from UpsertScene
// precisely so caching a hash never touches title/file_path/etc. Updating an
// id that doesn't exist is not an error, matching DeleteScene's convention.
func (s *Store) UpdateScenePHash(ctx context.Context, id int64, phash string, fileSize int64, fileMTime string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE library_scenes
		SET phash = ?, phash_file_size = ?, phash_file_mtime = ?,
		    updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
		WHERE id = ?
	`, phash, fileSize, fileMTime, id)
	if err != nil {
		return fmt.Errorf("updating phash for library scene %d: %w", id, err)
	}
	return nil
}

// SceneTags returns sceneID's assigned tags, alphabetically. Tags live at
// the scene level — the natural granularity for Purge, which removes a whole
// scene at a time.
func (s *Store) SceneTags(ctx context.Context, sceneID int64) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT tag FROM library_scene_tags WHERE scene_id = ? ORDER BY tag`, sceneID)
	if err != nil {
		return nil, fmt.Errorf("listing tags for scene %d: %w", sceneID, err)
	}
	defer rows.Close()

	out := []string{}
	for rows.Next() {
		var tag string
		if err := rows.Scan(&tag); err != nil {
			return nil, fmt.Errorf("scanning tag: %w", err)
		}
		out = append(out, tag)
	}
	return out, rows.Err()
}

// AddSceneTag assigns tag to sceneID. A no-op (not an error) if already assigned.
func (s *Store) AddSceneTag(ctx context.Context, sceneID int64, tag string) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO library_scene_tags (scene_id, tag) VALUES (?, ?)
		ON CONFLICT(scene_id, tag) DO NOTHING
	`, sceneID, tag)
	if err != nil {
		return fmt.Errorf("adding tag %q to scene %d: %w", tag, sceneID, err)
	}
	return nil
}

// RemoveSceneTag unassigns tag from sceneID. A no-op if it wasn't assigned.
func (s *Store) RemoveSceneTag(ctx context.Context, sceneID int64, tag string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM library_scene_tags WHERE scene_id = ? AND tag = ?`, sceneID, tag)
	if err != nil {
		return fmt.Errorf("removing tag %q from scene %d: %w", tag, sceneID, err)
	}
	return nil
}

// SceneTagVocabulary returns every distinct tag currently used by any scene —
// what a Tag picker autocompletes against, same principle as
// SeriesTagVocabulary.
func (s *Store) SceneTagVocabulary(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT tag FROM library_scene_tags ORDER BY tag`)
	if err != nil {
		return nil, fmt.Errorf("listing scene tag vocabulary: %w", err)
	}
	defer rows.Close()

	out := []string{}
	for rows.Next() {
		var tag string
		if err := rows.Scan(&tag); err != nil {
			return nil, fmt.Errorf("scanning tag: %w", err)
		}
		out = append(out, tag)
	}
	return out, rows.Err()
}

func scanScene(row rowScanner) (Scene, error) {
	var scene Scene
	err := row.Scan(&scene.ID, &scene.Box, &scene.SceneID, &scene.Title, &scene.Studio, &scene.Date,
		&scene.FilePath, &scene.RootFolderPath,
		&scene.PHash, &scene.PHashFileSize, &scene.PHashFileMTime,
		&scene.CreatedAt, &scene.UpdatedAt)
	return scene, err
}
