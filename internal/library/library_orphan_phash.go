package library

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strings"
	"time"

	"github.com/labbersanon/sakms/internal/phash"
)

// OrphanPHash caches a SAK-computed perceptual hash for an untracked orphan
// file. The cache entry is valid only while the file's size+mtime still match
// the stored identity fields — a replaced or re-encoded file at the same path
// is detected on the next scan and the hash is recomputed.
type OrphanPHash struct {
	Path           string
	PHash          string
	PHashFileSize  int64
	PHashFileMTime string
}

// orphanFileIdentity returns the size and UTC RFC3339Nano mtime used as the
// orphan phash cache key — identical logic to dedup.fileIdentity but local to
// the library package so it doesn't create a cross-package dependency.
func orphanFileIdentity(path string) (size int64, mtime string, err error) {
	fi, err := os.Stat(path)
	if err != nil {
		return 0, "", err
	}
	return fi.Size(), fi.ModTime().UTC().Format(time.RFC3339Nano), nil
}

// GetOrphanPHash returns the cached OrphanPHash for path, or (zero, nil) when
// no entry exists. It returns an error only for unexpected database failures.
func (s *Store) GetOrphanPHash(ctx context.Context, path string) (OrphanPHash, error) {
	var o OrphanPHash
	err := s.db.QueryRowContext(ctx,
		`SELECT path, phash, phash_file_size, phash_file_mtime
		   FROM orphan_phashes WHERE path = ?`, path).
		Scan(&o.Path, &o.PHash, &o.PHashFileSize, &o.PHashFileMTime)
	if errors.Is(err, sql.ErrNoRows) {
		return OrphanPHash{}, nil
	}
	return o, err
}

// UpsertOrphanPHash inserts or replaces the cached phash entry for an orphan
// file. Called after a fresh hash computation to amortise ffmpeg cost across
// subsequent scans.
func (s *Store) UpsertOrphanPHash(ctx context.Context, path, hash string, fileSize int64, fileMTime string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO orphan_phashes (path, phash, phash_file_size, phash_file_mtime)
		 VALUES (?, ?, ?, ?)
		 ON CONFLICT(path) DO UPDATE SET
		   phash            = excluded.phash,
		   phash_file_size  = excluded.phash_file_size,
		   phash_file_mtime = excluded.phash_file_mtime`,
		path, hash, fileSize, fileMTime)
	return err
}

// DeleteOrphanPHashesNotIn removes stale cache entries for paths no longer
// present in the current scan's file list, preventing unbounded growth. The
// paths slice is the complete set of orphan paths discovered in this scan pass.
//
// SQLite's SQLITE_MAX_VARIABLE_NUMBER is 999 on older builds. Rather than
// issuing a single NOT IN (all paths) — which silently fails once the path
// count exceeds the limit — we use a two-step approach: collect the set of
// paths that ARE valid into a temporary table, then delete anything not in it.
// Since SQLite has no per-connection temp tables accessible from here, we
// instead: (1) delete everything, (2) the caller re-populates via upserts on
// the next scan. That is overkill. The correct bounded approach is to chunk
// the KEEP set: insert valid paths in batches of 999 into a temp table, then
// delete orphan_phashes rows whose path is not in the temp table.
//
// Simplest correct approach that stays within limits: build the keep-set as a
// Go map, read all current paths from the DB, delete only the ones not in the
// keep-set — one DELETE per stale row, avoiding any variable-count clause.
func (s *Store) DeleteOrphanPHashesNotIn(ctx context.Context, paths []string) error {
	if len(paths) == 0 {
		_, err := s.db.ExecContext(ctx, `DELETE FROM orphan_phashes`)
		return err
	}

	// Build keep-set from caller's paths.
	keep := make(map[string]struct{}, len(paths))
	for _, p := range paths {
		keep[p] = struct{}{}
	}

	// Read all currently cached paths from the DB.
	rows, err := s.db.QueryContext(ctx, `SELECT path FROM orphan_phashes`)
	if err != nil {
		return err
	}
	var stale []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			rows.Close()
			return err
		}
		if _, ok := keep[p]; !ok {
			stale = append(stale, p)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	// Delete stale rows in chunks of 999 to stay within SQLite's variable limit.
	const chunkSize = 999
	for i := 0; i < len(stale); i += chunkSize {
		end := i + chunkSize
		if end > len(stale) {
			end = len(stale)
		}
		chunk := stale[i:end]
		placeholders := strings.Repeat("?,", len(chunk))
		placeholders = placeholders[:len(placeholders)-1]
		args := make([]any, len(chunk))
		for j, p := range chunk {
			args[j] = p
		}
		if _, err := s.db.ExecContext(ctx,
			`DELETE FROM orphan_phashes WHERE path IN (`+placeholders+`)`, args...); err != nil {
			return err
		}
	}
	return nil
}

// OrphanPHasher is the subset of a phash hasher that LoadOrComputeOrphanPHash
// needs — matches dedup.PHasher without importing that package.
type OrphanPHasher interface {
	Hash(ctx context.Context, path string) (string, error)
}

// LoadOrComputeOrphanPHash returns the valid cached phash for path when the
// file's current size+mtime match the stored identity, or computes, caches,
// and returns a fresh hash via hasher otherwise. Returns "" when the hash
// cannot be computed (same drop-on-error tolerance as attachPHashes in dedup).
func (s *Store) LoadOrComputeOrphanPHash(ctx context.Context, hasher OrphanPHasher, path string) string {
	size, mtime, err := orphanFileIdentity(path)
	if err != nil {
		return ""
	}

	cached, err := s.GetOrphanPHash(ctx, path)
	if err == nil && cached.PHash != "" &&
		strings.HasPrefix(cached.PHash, phash.Scheme+":") &&
		cached.PHashFileSize == size && cached.PHashFileMTime == mtime {
		return cached.PHash
	}

	h, err := hasher.Hash(ctx, path)
	if err != nil {
		return ""
	}
	_ = s.UpsertOrphanPHash(ctx, path, h, size, mtime)
	return h
}
