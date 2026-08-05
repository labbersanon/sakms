package library

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/labbersanon/sakms/internal/mode"
)

// ItemFile is one video file belonging to a library_items title (primary or alternate).
type ItemFile struct {
	ID          int64   `json:"id"`
	ItemID      int64   `json:"itemId"`
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

// ListFiles returns every file row for itemID, primary first.
func (s *Store) ListFiles(ctx context.Context, itemID int64) ([]ItemFile, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, item_id, file_path, is_primary, quality_tier, size, width, height,
		       video_codec, bitrate, duration_sec, phash, created_at, updated_at
		FROM library_item_files
		WHERE item_id = ?
		ORDER BY is_primary DESC, id ASC
	`, itemID)
	if err != nil {
		return nil, fmt.Errorf("listing files for library item %d: %w", itemID, err)
	}
	defer rows.Close()
	out := []ItemFile{}
	for rows.Next() {
		f, err := scanItemFile(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// SyncPrimaryFile ensures a primary library_item_files row matches the item's
// denormalized file_path / tier / size. Used after Upsert of a single-file title.
func (s *Store) SyncPrimaryFile(ctx context.Context, item Item) error {
	if item.ID == 0 || item.FilePath == "" {
		return nil
	}
	files, err := s.ListFiles(ctx, item.ID)
	if err != nil {
		return err
	}
	for _, f := range files {
		if f.IsPrimary {
			_, err := s.db.ExecContext(ctx, `
				UPDATE library_item_files SET
					file_path = ?, quality_tier = ?, size = ?, phash = ?,
					updated_at = sakms_now()
				WHERE id = ?
			`, item.FilePath, item.QualityTier, item.Size, item.PHash, f.ID)
			return err
		}
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO library_item_files (item_id, file_path, is_primary, quality_tier, size, phash)
		VALUES (?, ?, true, ?, ?, ?)
	`, item.ID, item.FilePath, item.QualityTier, item.Size, item.PHash)
	return err
}

// UpsertFile inserts or updates a file row by file_path. When isPrimary is true,
// clears primary on siblings first (equal-tier keep-existing is caller's job).
func (s *Store) UpsertFile(ctx context.Context, f ItemFile) (ItemFile, error) {
	if f.ItemID == 0 || f.FilePath == "" {
		return ItemFile{}, fmt.Errorf("library: UpsertFile requires item_id and file_path")
	}
	if f.IsPrimary {
		if _, err := s.db.ExecContext(ctx, `
			UPDATE library_item_files SET is_primary = false, updated_at = sakms_now()
			WHERE item_id = ? AND is_primary = true
		`, f.ItemID); err != nil {
			return ItemFile{}, fmt.Errorf("clearing primary for item %d: %w", f.ItemID, err)
		}
	}
	row := s.db.QueryRowContext(ctx, `
		INSERT INTO library_item_files (
			item_id, file_path, is_primary, quality_tier, size, width, height,
			video_codec, bitrate, duration_sec, phash
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (file_path) DO UPDATE SET
			item_id = excluded.item_id,
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
	`, f.ItemID, f.FilePath, f.IsPrimary, f.QualityTier, f.Size, f.Width, f.Height,
		f.VideoCodec, f.BitRate, f.DurationSec, f.PHash)
	if err := row.Scan(&f.ID, &f.CreatedAt, &f.UpdatedAt); err != nil {
		return ItemFile{}, fmt.Errorf("upserting library file %q: %w", f.FilePath, err)
	}
	return f, nil
}

func scanItemFile(row interface {
	Scan(dest ...any) error
}) (ItemFile, error) {
	var f ItemFile
	err := row.Scan(
		&f.ID, &f.ItemID, &f.FilePath, &f.IsPrimary, &f.QualityTier, &f.Size,
		&f.Width, &f.Height, &f.VideoCodec, &f.BitRate, &f.DurationSec, &f.PHash,
		&f.CreatedAt, &f.UpdatedAt,
	)
	return f, err
}

// PrimaryFile returns the primary file row for itemID, or ErrNotFound.
func (s *Store) PrimaryFile(ctx context.Context, itemID int64) (ItemFile, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, item_id, file_path, is_primary, quality_tier, size, width, height,
		       video_codec, bitrate, duration_sec, phash, created_at, updated_at
		FROM library_item_files
		WHERE item_id = ? AND is_primary = true
	`, itemID)
	f, err := scanItemFile(row)
	if errorsIsNoRows(err) {
		return ItemFile{}, ErrNotFound
	}
	return f, err
}

func errorsIsNoRows(err error) bool {
	return errors.Is(err, sql.ErrNoRows)
}

// AllFilePaths returns every on-disk path for mode m — denormalized item
// paths plus library_item_files — so Rename/Dedup known-maps skip alternates.
func (s *Store) AllFilePaths(ctx context.Context, m mode.Mode) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT DISTINCT file_path FROM (
			SELECT file_path FROM library_items WHERE mode = ? AND file_path <> ''
			UNION
			SELECT f.file_path FROM library_item_files f
			JOIN library_items i ON i.id = f.item_id
			WHERE i.mode = ? AND f.file_path <> ''
		) paths
	`, string(m), string(m))
	if err != nil {
		return nil, fmt.Errorf("listing all file paths for %s: %w", m, err)
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

// UpdateItemPrimaryPath writes the denormalized primary path/tier/size on
// library_items after a promote/demote without a full metadata Upsert.
func (s *Store) UpdateItemPrimaryPath(ctx context.Context, itemID int64, filePath, qualityTier string, size int64) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE library_items
		SET file_path = ?, quality_tier = ?, size = ?, updated_at = sakms_now()
		WHERE id = ?
	`, filePath, qualityTier, size, itemID)
	if err != nil {
		return fmt.Errorf("updating primary path for library item %d: %w", itemID, err)
	}
	return nil
}
