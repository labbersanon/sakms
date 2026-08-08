package library

import (
	"context"
	"fmt"
)

// EpisodeFile is one video file belonging to a library_episodes slot (primary
// or alternate) — the Series counterpart to ItemFile (library_files.go:13).
type EpisodeFile struct {
	ID          int64   `json:"id"`
	EpisodeID   int64   `json:"episodeId"`
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

// ListEpisodeFiles returns every file row for episodeID, primary first.
// Mirrors ListFiles (library_files.go:31).
func (s *Store) ListEpisodeFiles(ctx context.Context, episodeID int64) ([]EpisodeFile, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, episode_id, file_path, is_primary, quality_tier, size, width, height,
		       video_codec, bitrate, duration_sec, phash, created_at, updated_at
		FROM library_episode_files
		WHERE episode_id = ?
		ORDER BY is_primary DESC, id ASC
	`, episodeID)
	if err != nil {
		return nil, fmt.Errorf("listing files for library episode %d: %w", episodeID, err)
	}
	defer rows.Close()
	out := []EpisodeFile{}
	for rows.Next() {
		f, err := scanEpisodeFile(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// UpsertEpisodeFile inserts or updates a file row by (episode_id, file_path).
// When IsPrimary is true, clears primary on siblings of the SAME episode first
// (equal-tier keep-existing is the caller's job, exactly as in UpsertFile).
// Mirrors UpsertFile (library_files.go:84).
//
// The demote clause is deliberately `WHERE episode_id = ? AND is_primary = true`
// with NO `file_path <> ?` conjunct, unlike SyncPrimaryEpisodeFile above — that
// is UpsertFile's shape, and the two are not to be harmonized: this one is
// called with an explicitly chosen IsPrimary and must clear whatever incumbent
// exists, while SyncPrimaryEpisodeFile's narrower clause is what makes a repeat
// sync of an unchanged episode a no-op rather than a demote/re-promote flap.
//
// The ON CONFLICT target carries no `episode_id = excluded.episode_id` line —
// unlike UpsertFile, which re-points item_id because its conflict target is
// file_path alone. Here episode_id is part of the key, so re-pointing is
// impossible by construction.
func (s *Store) UpsertEpisodeFile(ctx context.Context, f EpisodeFile) (EpisodeFile, error) {
	if f.EpisodeID == 0 || f.FilePath == "" {
		return EpisodeFile{}, fmt.Errorf("library: UpsertEpisodeFile requires episode_id and file_path")
	}
	if f.IsPrimary {
		if _, err := s.db.ExecContext(ctx, `
			UPDATE library_episode_files SET is_primary = false, updated_at = sakms_now()
			WHERE episode_id = ? AND is_primary = true
		`, f.EpisodeID); err != nil {
			return EpisodeFile{}, fmt.Errorf("clearing primary for episode %d: %w", f.EpisodeID, err)
		}
	}
	row := s.db.QueryRowContext(ctx, `
		INSERT INTO library_episode_files (
			episode_id, file_path, is_primary, quality_tier, size, width, height,
			video_codec, bitrate, duration_sec, phash
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (episode_id, file_path) DO UPDATE SET
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
	`, f.EpisodeID, f.FilePath, f.IsPrimary, f.QualityTier, f.Size, f.Width, f.Height,
		f.VideoCodec, f.BitRate, f.DurationSec, f.PHash)
	if err := row.Scan(&f.ID, &f.CreatedAt, &f.UpdatedAt); err != nil {
		return EpisodeFile{}, fmt.Errorf("upserting library episode file %q: %w", f.FilePath, err)
	}
	return f, nil
}

func scanEpisodeFile(row interface {
	Scan(dest ...any) error
}) (EpisodeFile, error) {
	var f EpisodeFile
	err := row.Scan(
		&f.ID, &f.EpisodeID, &f.FilePath, &f.IsPrimary, &f.QualityTier, &f.Size,
		&f.Width, &f.Height, &f.VideoCodec, &f.BitRate, &f.DurationSec, &f.PHash,
		&f.CreatedAt, &f.UpdatedAt,
	)
	return f, err
}

// PrimaryEpisodeFile returns the primary file row for episodeID, or ErrNotFound.
// Mirrors PrimaryFile (library_files.go:135); reuses errorsIsNoRows from that
// file (same package — do not redeclare it).
func (s *Store) PrimaryEpisodeFile(ctx context.Context, episodeID int64) (EpisodeFile, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, episode_id, file_path, is_primary, quality_tier, size, width, height,
		       video_codec, bitrate, duration_sec, phash, created_at, updated_at
		FROM library_episode_files
		WHERE episode_id = ? AND is_primary = true
	`, episodeID)
	f, err := scanEpisodeFile(row)
	if errorsIsNoRows(err) {
		return EpisodeFile{}, ErrNotFound
	}
	return f, err
}

// AllEpisodeFilePaths returns every on-disk Series path — denormalized
// library_episodes.file_path plus library_episode_files — so Rename's known-map
// skips alternates. A range file repeats one path across several episode rows;
// the inner query's UNION (not UNION ALL) already dedupes those repeats, so the
// outer SELECT DISTINCT is redundant here rather than load-bearing. Mirrors
// AllFilePaths (library_files.go:155),
// minus the mode parameter (library_episodes is Series-only, like library_series).
func (s *Store) AllEpisodeFilePaths(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT DISTINCT file_path FROM (
			SELECT file_path FROM library_episodes WHERE file_path <> ''
			UNION
			SELECT file_path FROM library_episode_files WHERE file_path <> ''
		) paths
	`)
	if err != nil {
		return nil, fmt.Errorf("listing all episode file paths: %w", err)
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

// UpdateEpisodePrimaryPath writes the denormalized primary path/tier/size on
// library_episodes after a promote/demote, without a full UpsertEpisode (which
// would blank title/air_date if handed a zero-valued Episode — the same hazard
// UpsertEpisodeCatalog's doc comment describes).
// Mirrors UpdateItemPrimaryPath (library_files.go:182).
func (s *Store) UpdateEpisodePrimaryPath(ctx context.Context, episodeID int64, filePath, qualityTier string, size int64) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE library_episodes
		SET file_path = ?, quality_tier = ?, size = ?, updated_at = sakms_now()
		WHERE id = ?
	`, filePath, qualityTier, size, episodeID)
	if err != nil {
		return fmt.Errorf("updating primary path for library episode %d: %w", episodeID, err)
	}
	return nil
}

// SyncPrimaryEpisodeFile ensures a primary library_episode_files row matches the
// episode's denormalized file_path / tier / size — the Series counterpart to
// SyncPrimaryFile (library_files.go:56). Called from UpsertEpisode and
// UpsertEpisodes, so Rename Apply, Series Dedup Apply and the importer all keep
// the primary entry naming the file library_episodes itself names, for free.
//
// No-ops for ep.ID == 0 or ep.FilePath == "". The second guard is load-bearing,
// not defensive tidiness: an episode row legitimately exists with no file (the
// "TMDB knows about this episode, we don't have it" catalog row that makes
// missing-episode queries possible), and minting a file row for one would put a
// primary entry on a path that never existed.
//
// The SHAPE differs from SyncPrimaryFile deliberately, and that is not a
// Movies-parity break: this is a demote-then-upsert pair rather than the Movies
// version's list-then-UPDATE-or-INSERT, because UNIQUE (episode_id, file_path)
// makes the write expressible as one INSERT ... ON CONFLICT and removes the
// read-then-write race SyncPrimaryFile carries.
//
// SCOPE — narrower than the obvious reading, stated narrowly on purpose. It
// keeps the PRIMARY entry correct; it does NOT delete rows for files removed by
// someone else. When Series Dedup deletes a losing file, or Purge removes one,
// nothing deletes that file's library_episode_files row — it survives as a
// NON-primary row pointing at a path that no longer exists. That is a flagged,
// accepted gap (.omc/plans/autopilot-impl.md §11.11), not an oversight: Movies
// has the identical gap today, and the fix (a DeleteEpisodeFile/DeleteFile pair
// wired into both Dedup and Purge, for both modes) is deliberately out of scope
// here. Do not add deletion logic to this function.
//
// Writes only path/tier/size/phash — not width/height/codec/bitrate/duration —
// the same split Movies has between SyncPrimaryFile and Apply's own full
// UpsertFile write.
func (s *Store) SyncPrimaryEpisodeFile(ctx context.Context, ep Episode) error {
	if ep.ID == 0 || ep.FilePath == "" {
		return nil
	}
	// Demote first: library_episode_files_one_primary is a PARTIAL unique index
	// on (episode_id) WHERE is_primary, so flagging the incoming row primary
	// before clearing the incumbent violates it. The file_path <> ? clause
	// deliberately never demotes the row about to be written, which is what
	// makes a repeat sync of an unchanged episode a no-op rather than a
	// demote/re-promote flap.
	if _, err := s.db.ExecContext(ctx, `
		UPDATE library_episode_files SET is_primary = false, updated_at = sakms_now()
		WHERE episode_id = ? AND is_primary = true AND file_path <> ?
	`, ep.ID, ep.FilePath); err != nil {
		return fmt.Errorf("clearing primary file for library episode %d: %w", ep.ID, err)
	}
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO library_episode_files (episode_id, file_path, is_primary, quality_tier, size, phash)
		VALUES (?, ?, true, ?, ?, ?)
		ON CONFLICT (episode_id, file_path) DO UPDATE SET
			is_primary   = true,
			quality_tier = excluded.quality_tier,
			size         = excluded.size,
			phash        = excluded.phash,
			updated_at   = sakms_now()
	`, ep.ID, ep.FilePath, ep.QualityTier, ep.Size, ep.PHash); err != nil {
		return fmt.Errorf("syncing primary file %q for library episode %d: %w", ep.FilePath, ep.ID, err)
	}
	return nil
}
