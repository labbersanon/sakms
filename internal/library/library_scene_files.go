package library

import (
	"context"
	"fmt"
)

// SceneFile is one video file belonging to a library_scenes slot (primary or
// alternate) — the Adult counterpart to EpisodeFile (library_episode_files.go:10)
// and ItemFile (library_files.go:13).
//
// NAME HAZARD: SceneID is library_scenes.id (the bigint row id), NOT
// library_scenes.scene_id (the text stash-box id). This matches the convention
// set by library_scene_tags.scene_id and library_episode_files.episode_id.
// Always alias both tables in any join: FROM library_scene_files f JOIN
// library_scenes s ON s.id = f.scene_id.
//
// Claude 2026-08-12: SceneFile — mirrors EpisodeFile for Adult alternate-version support
// Reason: deep-interview-adult-rename-review-alts — Adult gains Movies/Series parity for
//   multi-file (primary + alternates) per scene slot.
// Troubleshooting: see migration 0010 NAME HAZARD comment; always alias in joins.
// Review if: library_scenes.file_path stops being the denormalized primary.
type SceneFile struct {
	ID          int64   `json:"id"`
	SceneID     int64   `json:"sceneId"` // library_scenes.id (row id), NOT library_scenes.scene_id
	FilePath    string  `json:"filePath"`
	IsPrimary   bool    `json:"isPrimary"`
	QualityTier string  `json:"qualityTier,omitempty"`
	Size        int64   `json:"size,omitempty"`
	Width       int     `json:"width,omitempty"`
	Height      int     `json:"height,omitempty"`
	VideoCodec  string  `json:"videoCodec,omitempty"`
	BitRate     int64   `json:"bitrate,omitempty"`
	DurationSec float64 `json:"durationSec,omitempty"`
	PHash       string  `json:"phash,omitempty"`
	CreatedAt   string  `json:"createdAt,omitempty"`
	UpdatedAt   string  `json:"updatedAt,omitempty"`
}

// ListSceneFiles returns every file row for sceneRowID (library_scenes.id),
// primary first. Mirrors ListEpisodeFiles (library_episode_files.go:29).
func (s *Store) ListSceneFiles(ctx context.Context, sceneRowID int64) ([]SceneFile, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, scene_id, file_path, is_primary, quality_tier, size, width, height,
		       video_codec, bitrate, duration_sec, phash, created_at, updated_at
		FROM library_scene_files
		WHERE scene_id = ?
		ORDER BY is_primary DESC, id ASC
	`, sceneRowID)
	if err != nil {
		return nil, fmt.Errorf("listing files for library scene %d: %w", sceneRowID, err)
	}
	defer rows.Close()
	out := []SceneFile{}
	for rows.Next() {
		f, err := scanSceneFile(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// UpsertSceneFile inserts or updates a file row by file_path. When IsPrimary
// is true, clears primary on siblings of the SAME scene first (equal-tier
// keep-existing is the caller's job, exactly as in UpsertFile).
// Mirrors UpsertFile (library_files.go:84).
//
// Two deliberate divergences from UpsertEpisodeFile, documented here:
//
//  1. The ON CONFLICT target is (file_path) — the Movies/Item shape, not the
//     Series (episode_id, file_path) shape. Adult is a flat one-file thing with
//     no range concept: one path belongs to exactly one scene, so the narrow
//     UNIQUE(file_path) key is correct and stronger (see migration 0010 §3.2).
//
//  2. Consequently, the upsert includes `scene_id = excluded.scene_id` — the
//     re-point line UpsertFile has (library_files.go:102) that UpsertEpisodeFile
//     deliberately omits (library_episode_files.go:64-67). When a path's scene
//     changes (e.g. a local scene is upgraded to a catalog identity), this
//     re-points the row rather than silently leaving it under the old scene_id.
func (s *Store) UpsertSceneFile(ctx context.Context, f SceneFile) (SceneFile, error) {
	if f.SceneID == 0 || f.FilePath == "" {
		return SceneFile{}, fmt.Errorf("library: UpsertSceneFile requires scene_id and file_path")
	}
	if f.IsPrimary {
		if _, err := s.db.ExecContext(ctx, `
			UPDATE library_scene_files SET is_primary = false, updated_at = sakms_now()
			WHERE scene_id = ? AND is_primary = true
		`, f.SceneID); err != nil {
			return SceneFile{}, fmt.Errorf("clearing primary for scene %d: %w", f.SceneID, err)
		}
	}
	row := s.db.QueryRowContext(ctx, `
		INSERT INTO library_scene_files (
			scene_id, file_path, is_primary, quality_tier, size, width, height,
			video_codec, bitrate, duration_sec, phash
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (file_path) DO UPDATE SET
			scene_id = excluded.scene_id,
			is_primary = excluded.is_primary,
			quality_tier = excluded.quality_tier,
			size = excluded.size,
			width = excluded.width,
			height = excluded.height,
			video_codec = excluded.video_codec,
			bitrate = excluded.bitrate,
			duration_sec = excluded.duration_sec,
			phash = excluded.phash,
			updated_at = sakms_now()
		RETURNING id, created_at, updated_at
	`, f.SceneID, f.FilePath, f.IsPrimary, f.QualityTier, f.Size, f.Width, f.Height,
		f.VideoCodec, f.BitRate, f.DurationSec, f.PHash)
	if err := row.Scan(&f.ID, &f.CreatedAt, &f.UpdatedAt); err != nil {
		return SceneFile{}, fmt.Errorf("upserting library scene file %q: %w", f.FilePath, err)
	}
	return f, nil
}

func scanSceneFile(row interface {
	Scan(dest ...any) error
}) (SceneFile, error) {
	var f SceneFile
	err := row.Scan(
		&f.ID, &f.SceneID, &f.FilePath, &f.IsPrimary, &f.QualityTier, &f.Size,
		&f.Width, &f.Height, &f.VideoCodec, &f.BitRate, &f.DurationSec, &f.PHash,
		&f.CreatedAt, &f.UpdatedAt,
	)
	return f, err
}

// PrimarySceneFile returns the primary file row for sceneRowID, or ErrNotFound.
// Mirrors PrimaryEpisodeFile (library_episode_files.go:120); reuses errorsIsNoRows
// from library_files.go (same package — do not redeclare it).
func (s *Store) PrimarySceneFile(ctx context.Context, sceneRowID int64) (SceneFile, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, scene_id, file_path, is_primary, quality_tier, size, width, height,
		       video_codec, bitrate, duration_sec, phash, created_at, updated_at
		FROM library_scene_files
		WHERE scene_id = ? AND is_primary = true
	`, sceneRowID)
	f, err := scanSceneFile(row)
	if errorsIsNoRows(err) {
		return SceneFile{}, ErrNotFound
	}
	return f, err
}

// DeleteSceneFileByPath removes the library_scene_files row for the given path,
// if one exists. A no-op if no row matches. Used by applyAdultAlternate to clean
// up the stale row for the original primary path after it has been physically moved
// to an alternate-named destination — the two paths differ in Adult (because
// phash is embedded in the filename) unlike Movies where the canonical filename
// stays the same.
func (s *Store) DeleteSceneFileByPath(ctx context.Context, filePath string) error {
	if filePath == "" {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM library_scene_files WHERE file_path = $1`, filePath)
	if err != nil {
		return fmt.Errorf("deleting scene file row for %q: %w", filePath, err)
	}
	return nil
}

// AllScenePaths returns every on-disk Adult path — denormalized
// library_scenes.file_path plus library_scene_files — so Rename's known-map
// skips alternates. Mirrors AllEpisodeFilePaths (library_episode_files.go:141).
func (s *Store) AllScenePaths(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT DISTINCT file_path FROM (
			SELECT file_path FROM library_scenes WHERE file_path <> ''
			UNION
			SELECT file_path FROM library_scene_files WHERE file_path <> ''
		) paths
	`)
	if err != nil {
		return nil, fmt.Errorf("listing all scene file paths: %w", err)
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

// SyncPrimarySceneFile ensures a primary library_scene_files row matches the
// scene's denormalized file_path / tier / size — the Adult counterpart to
// SyncPrimaryEpisodeFile (library_episode_files.go:212). Called from UpsertScene
// so every Adult write path (ApplyLibraryAdult, OrganizeImportedAdult, Dedup
// Apply, cross-mode move) maintains a correct primary file row for free, with no
// call-site changes.
//
// No-ops for sc.ID == 0 or sc.FilePath == "". The second guard is load-bearing:
// a scene row legitimately exists with no file (an operator-tagged stub), and
// minting a file row for one would put a primary entry on a path that never existed.
//
// SHAPE mirrors SyncPrimaryEpisodeFile (demote-then-upsert pair) rather than
// SyncPrimaryFile (list-then-UPDATE-or-INSERT), because UNIQUE(file_path) on
// library_scene_files makes the write expressible as one INSERT ... ON CONFLICT
// and removes the read-then-write race SyncPrimaryFile carries.
//
// SCOPE: keeps the PRIMARY entry correct; does NOT delete rows for files removed
// by someone else. That is a flagged, accepted gap mirrored from Movies/Series
// (library_episode_files.go:199-207). Do not add deletion logic here.
//
// Writes only path/tier/size/phash — not width/height/codec/bitrate/duration —
// the same split Movies/Series have between Sync* and Apply's own full Upsert*File.
func (s *Store) SyncPrimarySceneFile(ctx context.Context, sc Scene) error {
	if sc.ID == 0 || sc.FilePath == "" {
		return nil
	}
	// Demote first: library_scene_files_one_primary is a PARTIAL unique index
	// on (scene_id) WHERE is_primary, so flagging the incoming row primary before
	// clearing the incumbent violates it. The file_path <> ? clause deliberately
	// never demotes the row about to be written, making a repeat sync a no-op
	// rather than a demote/re-promote flap.
	if _, err := s.db.ExecContext(ctx, `
		UPDATE library_scene_files SET is_primary = false, updated_at = sakms_now()
		WHERE scene_id = ? AND is_primary = true AND file_path <> ?
	`, sc.ID, sc.FilePath); err != nil {
		return fmt.Errorf("clearing primary file for library scene %d: %w", sc.ID, err)
	}
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO library_scene_files (scene_id, file_path, is_primary, quality_tier, size, phash)
		VALUES (?, ?, true, ?, ?, ?)
		ON CONFLICT (file_path) DO UPDATE SET
			scene_id     = excluded.scene_id,
			is_primary   = true,
			quality_tier = excluded.quality_tier,
			size         = excluded.size,
			phash        = excluded.phash,
			updated_at   = sakms_now()
	`, sc.ID, sc.FilePath, sc.QualityTier, sc.Size, sc.PHash); err != nil {
		return fmt.Errorf("syncing primary file %q for library scene %d: %w", sc.FilePath, sc.ID, err)
	}
	return nil
}

// UpdateScenePrimaryPath writes the denormalized primary path/tier/size AND the
// phash triple onto library_scenes after a promote/demote, without a full
// UpsertScene (which would blank title/studio/date if handed a partial Scene).
// Mirrors UpdateEpisodePrimaryPath (library_episode_files.go:169), but extends
// it with phash + its file-identity keys.
//
// WHY the phash triple is required here (unlike Movies/Series): Adult's filename
// embeds the phash ([phash-HASH]) and Dedup validates its cache against
// (phash_file_size, phash_file_mtime). Promoting a new primary without rewriting
// those three columns leaves library_scenes.phash describing the file that is now
// an alternate, and Dedup will trust the cached hash against the wrong bytes.
// This is a correctness requirement, not symmetry-breaking — see plan §A3.
func (s *Store) UpdateScenePrimaryPath(ctx context.Context, id int64, filePath, qualityTier string, size int64, phash string, phashFileSize int64, phashFileMTime string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE library_scenes
		SET file_path = ?, quality_tier = ?, size = ?,
		    phash = ?, phash_file_size = ?, phash_file_mtime = ?,
		    updated_at = sakms_now()
		WHERE id = ?
	`, filePath, qualityTier, size, phash, phashFileSize, phashFileMTime, id)
	if err != nil {
		return fmt.Errorf("updating primary path for library scene %d: %w", id, err)
	}
	return nil
}
