// Package library is SAK's own record of "what's in my library" — the
// replacement for asking Radarr and Sonarr what they track. One Item per
// movie, keyed by mode (movies today; adult could use this same shape
// later, since a scene is also a flat one-file-done-once thing).
//
// Series does NOT reuse Item/library_items — a much earlier draft of this
// doc comment said it would, but that turned out wrong once Series was
// actually designed: a movie is downloaded once and done, while a series
// needs rows for episodes TMDB knows about but that aren't on disk yet, to
// make "missing episodes" a real query instead of an inferred state.
// Forcing that into Item's one-row-per-title shape would mean faking a
// synthetic per-episode "title" row. See library_series.go for Series'
// own Series/Episode types and Store methods, living in this same package
// (same concern — "what's in my library" — just a genuinely different
// shape), not a new top-level package.
//
// This package owns no HTTP client and makes no outbound calls — it's a
// thin SQLite-backed Store (same shape as internal/grabs/internal/
// connections) plus ScanRootFolder, a plain directory walk that replaces
// what Radarr/Sonarr's RootFolder.UnmappedFolders used to compute for free.
package library

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/curtiswtaylorjr/sakms/internal/config"
	"github.com/curtiswtaylorjr/sakms/internal/mode"
)

// ErrNotFound is returned by Get/GetByTMDBID when no matching item exists.
var ErrNotFound = errors.New("library: no item found")

// Item is one thing SAK's own library tracks — a movie today.
type Item struct {
	ID             int64     `json:"id"`
	Mode           mode.Mode `json:"mode"`
	TMDBID         int       `json:"tmdbId"`
	Title          string    `json:"title"`
	Year           int       `json:"year,omitempty"`
	FilePath       string    `json:"filePath"`
	RootFolderPath string    `json:"rootFolderPath"`
	// PHash is the SAK-computed perceptual hash of this item's video file,
	// cached so Dedup decodes each tracked file once rather than every Scan.
	// PHashFileSize/PHashFileMTime are the file-identity key it's valid for:
	// the cache is trusted only if the current file's os.Stat size+mtime still
	// match, which detects a replaced/re-encoded file at the same path. Empty/
	// zero means "not computed yet" — recomputed lazily on the next Dedup Scan.
	// The phash string is scheme-tagged (see internal/phash), so a value cached
	// under an older algorithm/frame-count is self-invalidating on comparison.
	PHash          string `json:"phash,omitempty"`
	PHashFileSize  int64  `json:"phashFileSize,omitempty"`
	PHashFileMTime string `json:"phashFileMtime,omitempty"`
	CreatedAt      string `json:"createdAt"`
	UpdatedAt      string `json:"updatedAt"`
}

type Store struct {
	db *sql.DB
}

func New(db *sql.DB) *Store {
	return &Store{db: db}
}

// Upsert creates item, or replaces it if one already exists for the same
// (mode, tmdbId) pair — the single re-entrant "this is now what I have for
// this title" operation Rename/Dedup/Search's check-import all use, so a
// re-scan or a re-grab of something already in the library updates it in
// place rather than erroring or duplicating.
func (s *Store) Upsert(ctx context.Context, item Item) (Item, error) {
	row := s.db.QueryRowContext(ctx, `
		INSERT INTO library_items (mode, tmdb_id, title, year, file_path, root_folder_path, phash, phash_file_size, phash_file_mtime)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(mode, tmdb_id) DO UPDATE SET
			title = excluded.title,
			year = excluded.year,
			file_path = excluded.file_path,
			root_folder_path = excluded.root_folder_path,
			phash = excluded.phash,
			phash_file_size = excluded.phash_file_size,
			phash_file_mtime = excluded.phash_file_mtime,
			updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
		RETURNING id, created_at, updated_at
	`, string(item.Mode), item.TMDBID, item.Title, item.Year, item.FilePath, item.RootFolderPath, item.PHash, item.PHashFileSize, item.PHashFileMTime)

	if err := row.Scan(&item.ID, &item.CreatedAt, &item.UpdatedAt); err != nil {
		return Item{}, fmt.Errorf("upserting library item %q: %w", item.Title, err)
	}
	return item, nil
}

// UpdatePHash writes a freshly-computed perceptual hash and its file-identity
// key (size + mtime) onto an existing tracked item, without rewriting the rest
// of the row — the targeted write Dedup's Scan uses to cache a tracked item's
// hash mid-scan (orphans have no row yet; their surviving winner's hash is
// persisted via Upsert in ApplyLibrary). Updating an id that doesn't exist is
// not an error, matching Delete's convention.
func (s *Store) UpdatePHash(ctx context.Context, id int64, phash string, fileSize int64, fileMTime string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE library_items
		SET phash = ?, phash_file_size = ?, phash_file_mtime = ?,
		    updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
		WHERE id = ?
	`, phash, fileSize, fileMTime, id)
	if err != nil {
		return fmt.Errorf("updating phash for library item %d: %w", id, err)
	}
	return nil
}

// List returns every item tracked for m, ordered by title.
func (s *Store) List(ctx context.Context, m mode.Mode) ([]Item, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, mode, tmdb_id, title, year, file_path, root_folder_path, phash, phash_file_size, phash_file_mtime, created_at, updated_at
		FROM library_items WHERE mode = ? ORDER BY title
	`, string(m))
	if err != nil {
		return nil, fmt.Errorf("listing library items: %w", err)
	}
	defer rows.Close()

	out := []Item{}
	for rows.Next() {
		item, err := scanItem(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning library item: %w", err)
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// Get returns a single item by ID.
func (s *Store) Get(ctx context.Context, id int64) (*Item, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, mode, tmdb_id, title, year, file_path, root_folder_path, phash, phash_file_size, phash_file_mtime, created_at, updated_at
		FROM library_items WHERE id = ?
	`, id)
	item, err := scanItem(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("loading library item %d: %w", id, err)
	}
	return &item, nil
}

// GetByTMDBID looks up an item by its (mode, tmdbId) identity — the
// duplicate-detection key Rename/Dedup use instead of Servarr's
// TVDB/TMDB-keyed TrackedItem list.
func (s *Store) GetByTMDBID(ctx context.Context, m mode.Mode, tmdbID int) (*Item, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, mode, tmdb_id, title, year, file_path, root_folder_path, phash, phash_file_size, phash_file_mtime, created_at, updated_at
		FROM library_items WHERE mode = ? AND tmdb_id = ?
	`, string(m), tmdbID)
	item, err := scanItem(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("loading library item for tmdb id %d: %w", tmdbID, err)
	}
	return &item, nil
}

// Delete permanently removes item id and its tags. Explicit two-statement
// delete rather than relying on the schema's ON DELETE CASCADE: SQLite only
// enforces foreign keys when a connection has run `PRAGMA foreign_keys =
// ON`, which internal/db's shared Open doesn't set (changing that
// connection-wide default is out of scope for this package). Deleting an id
// that doesn't exist is not an error — the end state is the same, matching
// connections.Store.Delete's convention.
func (s *Store) Delete(ctx context.Context, id int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("deleting library item %d: %w", id, err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `DELETE FROM library_tags WHERE item_id = ?`, id); err != nil {
		return fmt.Errorf("deleting tags for library item %d: %w", id, err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM library_items WHERE id = ?`, id); err != nil {
		return fmt.Errorf("deleting library item %d: %w", id, err)
	}
	return tx.Commit()
}

// Tags returns itemID's assigned tags, alphabetically.
func (s *Store) Tags(ctx context.Context, itemID int64) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT tag FROM library_tags WHERE item_id = ? ORDER BY tag`, itemID)
	if err != nil {
		return nil, fmt.Errorf("listing tags for item %d: %w", itemID, err)
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

// AddTag assigns tag to itemID. A no-op (not an error) if already assigned.
func (s *Store) AddTag(ctx context.Context, itemID int64, tag string) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO library_tags (item_id, tag) VALUES (?, ?)
		ON CONFLICT(item_id, tag) DO NOTHING
	`, itemID, tag)
	if err != nil {
		return fmt.Errorf("adding tag %q to item %d: %w", tag, itemID, err)
	}
	return nil
}

// RemoveTag unassigns tag from itemID. A no-op (not an error) if it wasn't assigned.
func (s *Store) RemoveTag(ctx context.Context, itemID int64, tag string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM library_tags WHERE item_id = ? AND tag = ?`, itemID, tag)
	if err != nil {
		return fmt.Errorf("removing tag %q from item %d: %w", tag, itemID, err)
	}
	return nil
}

// TagVocabulary returns every distinct tag currently used by any item in m —
// what a Tag picker autocompletes against, imported live from usage rather
// than a separately-maintained vocabulary list (the same principle
// internal/tag's Servarr-backed Vocabulary already follows).
func (s *Store) TagVocabulary(ctx context.Context, m mode.Mode) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT DISTINCT t.tag FROM library_tags t
		JOIN library_items i ON i.id = t.item_id
		WHERE i.mode = ? ORDER BY t.tag
	`, string(m))
	if err != nil {
		return nil, fmt.Errorf("listing tag vocabulary: %w", err)
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

// rowScanner is satisfied by both *sql.Row and *sql.Rows.
type rowScanner interface {
	Scan(dest ...any) error
}

func scanItem(row rowScanner) (Item, error) {
	var item Item
	var m string
	err := row.Scan(&item.ID, &m, &item.TMDBID, &item.Title, &item.Year,
		&item.FilePath, &item.RootFolderPath,
		&item.PHash, &item.PHashFileSize, &item.PHashFileMTime,
		&item.CreatedAt, &item.UpdatedAt)
	item.Mode = mode.Mode(m)
	return item, err
}

// UnmappedEntry is one file or directory found directly under a root folder
// that ScanRootFolder couldn't match to any already-known library item —
// the library-local equivalent of Radarr's RootFolder.UnmappedFolders.
type UnmappedEntry struct {
	Name string
	Path string
}

// ScanRootFolder walks rootPath recursively, reporting one UnmappedEntry per
// "atomic" unit not already claimed by known (keyed by absolute path):
// either a loose file, or a directory with no real subdirectories of its own
// (ignoring config.ExcludedDirNames, which are pruned regardless) and no
// already-known direct children — a movie's wrapping folder, a fresh
// season-pack release, or an empty placeholder folder, handed whole to
// ResolveVideoFile/ResolveEpisodeVideoFiles. A directory that doesn't
// qualify as atomic — because it has a real subdirectory of its own (an
// organizational "Series Title/" or "Season NN/" folder) or because one of
// its direct children is already known (a season folder with some episodes
// tracked and one new file dropped in) — is recursed into instead of
// reported whole, so content nested arbitrarily deep, or added alongside
// something already tracked, is still discovered. Skips sidecar files
// (config.SidecarExts) entirely. Mode-agnostic by design, shared by
// Rename's and Dedup's Movies/Series orphan scans alike.
func ScanRootFolder(rootPath string, known map[string]bool) ([]UnmappedEntry, error) {
	var out []UnmappedEntry
	err := filepath.WalkDir(rootPath, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == rootPath {
			return nil
		}
		if d.IsDir() && config.ExcludedDirNames[strings.ToLower(d.Name())] {
			return filepath.SkipDir
		}
		if !d.IsDir() && config.SidecarExts[strings.ToLower(filepath.Ext(d.Name()))] {
			return nil
		}
		if known[path] {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			atomic, err := dirIsLeafEntry(path, known)
			if err != nil {
				return nil // unreadable subdir — skip quietly, matches this scan's tolerant style elsewhere
			}
			if atomic {
				out = append(out, UnmappedEntry{Name: d.Name(), Path: path})
				return filepath.SkipDir
			}
			return nil
		}
		out = append(out, UnmappedEntry{Name: d.Name(), Path: path})
		return nil
	})
	if err != nil {
		return nil, classifyScanErr(rootPath, err)
	}
	return out, nil
}

// classifyScanErr wraps a WalkDir failure with an operator-actionable
// message when it looks like a mount disconnecting mid-scan (a dropped
// CIFS/NFS mount, an unplugged drive) — the overwhelmingly common real
// cause of a scan aborting outright, and one an operator can actually act
// on, unlike a bare "no such file or directory". The original error is
// still wrapped via %w either way, so logs/debugging keep the raw OS error
// underneath regardless of which message is shown.
func classifyScanErr(rootPath string, err error) error {
	if errors.Is(err, fs.ErrNotExist) ||
		errors.Is(err, syscall.ENOTCONN) ||
		errors.Is(err, syscall.ESTALE) ||
		errors.Is(err, syscall.EIO) ||
		errors.Is(err, syscall.EHOSTUNREACH) {
		return fmt.Errorf("root folder unreadable — check that %s is still mounted and reachable: %w", rootPath, err)
	}
	return fmt.Errorf("reading %s: %w", rootPath, err)
}

// dirIsLeafEntry reports whether path should be reported as one atomic
// UnmappedEntry rather than recursed into: none of its direct children are a
// real subdirectory (config.ExcludedDirNames don't count — they're pruned by
// the walk regardless, so a bonus-content folder like Sample/ shouldn't stop
// its parent movie folder from being reported whole), and none of its direct
// file children are already known (which would mean it's a partially-tracked
// organizational directory that needs opening up, not a fresh release to
// hand whole to ResolveVideoFile/ResolveEpisodeVideoFiles).
func dirIsLeafEntry(path string, known map[string]bool) (bool, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return false, err
	}
	for _, e := range entries {
		if e.IsDir() {
			if config.ExcludedDirNames[strings.ToLower(e.Name())] {
				continue
			}
			return false, nil
		}
		if known[filepath.Join(path, e.Name())] {
			return false, nil
		}
	}
	return true, nil
}

// videoExts are the file extensions ResolveVideoFile treats as playable
// video content — matches internal/dedup's own (private, independently
// tested) videoExts list.
var videoExts = map[string]bool{
	".mkv": true, ".mp4": true, ".avi": true, ".m4v": true,
	".ts": true, ".wmv": true, ".mov": true, ".webm": true,
}

// ResolveVideoFile resolves path to an actual video file: itself, if it's
// already one, or the largest video-extensioned file directly inside it, if
// it's a directory. Needed anywhere a relocated path might be a wrapping
// folder (root/Title (Year)/movie.mkv) rather than the file itself — e.g.
// after rename.Relocate or a completed grab's contentPath, both of which
// preserve whatever shape the source had (a single file, or a directory
// containing one).
func ResolveVideoFile(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("stat %s: %w", path, err)
	}
	if !info.IsDir() {
		return path, nil
	}

	entries, err := os.ReadDir(path)
	if err != nil {
		return "", fmt.Errorf("reading %s: %w", path, err)
	}
	var best string
	var bestSize int64
	for _, e := range entries {
		if e.IsDir() || !videoExts[strings.ToLower(filepath.Ext(e.Name()))] {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		if fi.Size() > bestSize {
			bestSize = fi.Size()
			best = filepath.Join(path, e.Name())
		}
	}
	if best == "" {
		return "", fmt.Errorf("no video file found under %s", path)
	}
	return best, nil
}
