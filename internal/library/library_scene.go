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
	// Size is the byte size of the file at FilePath, captured via os.Stat when
	// the row was written. 0 means "not captured yet" (a row predating this
	// column that the boot-time backfill hasn't reached). Deliberately NOT
	// PHashFileSize: that field is a cache-validation key allowed to go stale
	// on purpose so a replaced file is detected, which is the opposite of what
	// a storage total needs.
	Size int64 `json:"size,omitempty"`
	// QualityTier is the quality.Tier preference in force when this file
	// entered the library. "" means "not captured yet"; "unknown" means the
	// backfill ran and could not determine one (an accepted permanent state).
	QualityTier string `json:"qualityTier,omitempty"`
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
	// PosterAspectClass is write-once at INSERT: "vertical" (catalog poster
	// within ±5% of 2:3) or "horizontal" (everything else, including missing
	// art). Existing rows and failed probes stay horizontal. Never re-measured
	// on poster updates or UpgradeSceneIdentity.
	PosterAspectClass string `json:"posterAspectClass,omitempty"`
	// PosterURL is the catalog artwork URL copied from MatchResult.Image at
	// grab/import (imageproxy-validated). Empty when identify had no art or
	// the URL failed Validate. Fill-if-empty on later upserts; never fetched
	// by GET /tracked.
	PosterURL string `json:"posterUrl,omitempty"`
}

// UpsertScene creates a scene, or updates it if one already exists for the
// same (box, scene_id) pair — mirrors Upsert's re-entrant "this is now what
// I have" shape, used by Rename/Search's get-or-create-by-identity calls.
func (s *Store) UpsertScene(ctx context.Context, scene Scene) (Scene, error) {
	// Claude 2026-08-13: poster_aspect_class is INSERT-only (write-once).
	// Reason: Adult Movies vs Scenes membership is classified once from the
	//   catalog poster at grab/import; a later re-upsert must not flip a
	//   vertical row back to the horizontal default.
	// Troubleshooting: ON CONFLICT DO UPDATE SET must omit this column or a
	//   Rename Apply / Dedup path with a zero Scene would clobber it.
	// Review if: classification is allowed to re-run when a poster URL is stored.
	// Claude 2026-08-14: poster_url INSERT + fill-if-empty on conflict.
	// Reason: grab-import has MatchResult.Image; Apply/Dedup must not wipe it,
	//   but an empty row can gain art later. GET /tracked never probes.
	// Troubleshooting: sanitizePosterURL drops non-https / private hosts.
	// Review if: aspect is re-measured from this stored URL.
	scene.PosterAspectClass = normalizePosterAspect(scene.PosterAspectClass)
	scene.PosterURL = sanitizePosterURL(ctx, scene.PosterURL)
	row := s.db.QueryRowContext(ctx, `
		INSERT INTO library_scenes (box, scene_id, title, studio, date, file_path, root_folder_path, phash, phash_file_size, phash_file_mtime, size, quality_tier, poster_aspect_class, poster_url)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(box, scene_id) DO UPDATE SET
			title = excluded.title,
			studio = excluded.studio,
			date = excluded.date,
			file_path = excluded.file_path,
			root_folder_path = excluded.root_folder_path,
			phash = excluded.phash,
			phash_file_size = excluded.phash_file_size,
			phash_file_mtime = excluded.phash_file_mtime,
			size = excluded.size,
			quality_tier = excluded.quality_tier,
			poster_url = CASE WHEN library_scenes.poster_url = '' THEN excluded.poster_url ELSE library_scenes.poster_url END,
			updated_at = sakms_now()
		RETURNING id, created_at, updated_at, poster_aspect_class, poster_url
	`, scene.Box, scene.SceneID, scene.Title, scene.Studio, scene.Date, scene.FilePath, scene.RootFolderPath, scene.PHash, scene.PHashFileSize, scene.PHashFileMTime, scene.Size, scene.QualityTier, scene.PosterAspectClass, scene.PosterURL)

	if err := row.Scan(&scene.ID, &scene.CreatedAt, &scene.UpdatedAt, &scene.PosterAspectClass, &scene.PosterURL); err != nil {
		return Scene{}, fmt.Errorf("upserting scene %q: %w", scene.Title, err)
	}
	// Claude 2026-08-12: sync the primary library_scene_files row after every upsert.
	// Reason: deep-interview-adult-rename-review-alts — Adult gains alternate-version
	//   support (library_scene_files, 0010). Mirrors UpsertEpisode's SyncPrimaryEpisodeFile
	//   call so every existing Adult write path (ApplyLibraryAdult, OrganizeImportedAdult,
	//   Dedup Apply, cross-mode move) maintains a correct primary file row for free, with
	//   no call-site changes.
	// Troubleshooting: alternate fold logic found no primary file row because Apply
	//   wrote only library_scenes without a corresponding library_scene_files primary entry.
	// Review if: UpsertScene is split into a catalog-only and a file-bearing variant.
	if err := s.SyncPrimarySceneFile(ctx, scene); err != nil {
		return Scene{}, fmt.Errorf("syncing primary file for scene %q: %w", scene.Title, err)
	}
	return scene, nil
}

// GetScene looks up a scene by its (box, scene_id) identity, or ErrNotFound
// if no such row exists yet — the duplicate-detection/get-or-create key
// Rename and Search use, the direct analogue of GetSeriesByTMDBID with the
// key swapped.
func (s *Store) GetScene(ctx context.Context, box, sceneID string) (*Scene, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, box, scene_id, title, studio, date, file_path, root_folder_path, phash, phash_file_size, phash_file_mtime, created_at, updated_at, size, quality_tier, poster_aspect_class, poster_url
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

// GetSceneByPHash looks up a tracked scene by its perceptual hash. When
// multiple rows share a phash (should not happen in normal operation), the
// row with a live primary file_path is preferred.
func (s *Store) GetSceneByPHash(ctx context.Context, phash string) (*Scene, error) {
	if phash == "" {
		return nil, ErrNotFound
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, box, scene_id, title, studio, date, file_path, root_folder_path,
		       phash, phash_file_size, phash_file_mtime, created_at, updated_at, size, quality_tier, poster_aspect_class, poster_url
		FROM library_scenes WHERE phash = ?
	`, phash)
	if err != nil {
		return nil, fmt.Errorf("loading scene by phash %q: %w", phash, err)
	}
	defer rows.Close()

	var best *Scene
	for rows.Next() {
		scene, err := scanScene(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning scene by phash: %w", err)
		}
		if best == nil {
			best = &scene
			continue
		}
		if best.FilePath == "" && scene.FilePath != "" {
			best = &scene
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reading scenes by phash: %w", err)
	}
	if best == nil {
		return nil, ErrNotFound
	}
	return best, nil
}

// ListScenes returns every tracked scene, ordered by title.
func (s *Store) ListScenes(ctx context.Context) ([]Scene, error) {
	out, err := s.queryScenes(ctx, `
		SELECT id, box, scene_id, title, studio, date, file_path, root_folder_path, phash, phash_file_size, phash_file_mtime, created_at, updated_at, size, quality_tier, poster_aspect_class, poster_url
		FROM library_scenes ORDER BY title
	`)
	if err != nil {
		return nil, fmt.Errorf("listing scenes: %w", err)
	}
	return out, nil
}

// ListScenesByAspect is ListScenes filtered by poster_aspect_class.
// Invalid aspect is the caller's problem — ParsePosterAspectFilter maps it
// to 400 before this runs. Dedup/Purge/Rename scheduled scans still pass
// empty (every row); operator Organize chips pass vertical or horizontal.
func (s *Store) ListScenesByAspect(ctx context.Context, aspect string) ([]Scene, error) {
	out, err := s.queryScenes(ctx, `
		SELECT id, box, scene_id, title, studio, date, file_path, root_folder_path, phash, phash_file_size, phash_file_mtime, created_at, updated_at, size, quality_tier, poster_aspect_class, poster_url
		FROM library_scenes WHERE poster_aspect_class = ? ORDER BY title
	`, aspect)
	if err != nil {
		return nil, fmt.Errorf("listing scenes by aspect %q: %w", aspect, err)
	}
	return out, nil
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

// DeleteSceneTx is DeleteScene's caller-transaction-scoped sibling, added
// for the cross-mode move's source-row retirement
// (.omc/plans/autopilot-impl.md §4.2b). It takes a *sql.Tx rather than using
// s.db because retirement must commit or roll back together with the
// proposals.mode UPDATE that owns the transaction (Store.MoveMode) — a
// partial apply is data loss. It is NOT a thin wrapper around DeleteScene:
// DeleteScene opens its OWN BeginTx/Commit on s.db, which would commit the
// retirement independently of MoveMode's transaction.
//
// The tag delete is NOT optional and NOT a tidy-up, and the ORDER is not a
// style choice: library_scene_tags.scene_id references library_scenes(id)
// ON DELETE NO ACTION (0001_baseline.sql:443) — unlike library_items'
// dependents, this one does not cascade. Deleting the scene row first while
// a tag row still references it raises a foreign-key-violation (23503) and
// aborts the caller's whole transaction. Deleting an id that doesn't exist
// is not an error, matching DeleteScene's convention.
func (s *Store) DeleteSceneTx(ctx context.Context, tx *sql.Tx, id int64) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM library_scene_tags WHERE scene_id = ?`, id); err != nil {
		return fmt.Errorf("deleting tags for scene %d: %w", id, err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM library_scenes WHERE id = ?`, id); err != nil {
		return fmt.Errorf("deleting scene %d: %w", id, err)
	}
	return nil
}

// UpgradeSceneIdentity rewrites the (box, scene_id) key and display fields of
// an existing local scene row when the auto-upgrade pass finds a catalog match
// for it. It is keyed on id (the stable row primary key), NOT on the old
// (box, scene_id), so the row's tags (library_scene_tags.scene_id), its
// library_scene_files rows, and any undo-archive touched_rows_snapshot entries
// pointing at this id all survive the rename. Updating a non-existent id is
// not an error, matching DeleteScene's convention.
//
// Claude 2026-08-12: added for Phase D5 — adult-rename-review-alts.
// Reason: local phash-backed scenes gain a permanent catalog identity when
//
//	UpgradeLocalAdultScenes finds a fingerprint match. A delete+reinsert would
//	silently orphan tags, file rows, and undo references; a targeted UPDATE
//	keyed on id preserves them all.
//
// Review if: library_scenes stops having a stable id across identity changes.
func (s *Store) UpgradeSceneIdentity(ctx context.Context, id int64, box, sceneID, title, studio, date string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE library_scenes
		SET box = ?, scene_id = ?, title = ?, studio = ?, date = ?,
		    updated_at = sakms_now()
		WHERE id = ?
	`, box, sceneID, title, studio, date, id)
	if err != nil {
		return fmt.Errorf("upgrading scene identity for row %d: %w", id, err)
	}
	return nil
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
		    updated_at = sakms_now()
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
		ON CONFLICT (scene_id, (lower(tag))) DO NOTHING
	`, sceneID, tag)
	if err != nil {
		return fmt.Errorf("adding tag %q to scene %d: %w", tag, sceneID, err)
	}
	return nil
}

// RemoveSceneTag unassigns tag from sceneID. A no-op if it wasn't assigned.
func (s *Store) RemoveSceneTag(ctx context.Context, sceneID int64, tag string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM library_scene_tags WHERE scene_id = ? AND lower(tag) = lower(?)`, sceneID, tag)
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

func (s *Store) queryScenes(ctx context.Context, query string, args ...any) ([]Scene, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
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

func scanScene(row rowScanner) (Scene, error) {
	var scene Scene
	err := row.Scan(&scene.ID, &scene.Box, &scene.SceneID, &scene.Title, &scene.Studio, &scene.Date,
		&scene.FilePath, &scene.RootFolderPath,
		&scene.PHash, &scene.PHashFileSize, &scene.PHashFileMTime,
		&scene.CreatedAt, &scene.UpdatedAt,
		&scene.Size, &scene.QualityTier, &scene.PosterAspectClass, &scene.PosterURL)
	return scene, err
}
