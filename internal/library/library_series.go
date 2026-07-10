package library

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Series is one tracked show — the parent row episodes hang off of. Unlike
// Item, there's no Mode field: this table only ever holds Series, so the
// omission is deliberate, not an oversight.
type Series struct {
	ID             int64  `json:"id"`
	TMDBID         int    `json:"tmdbId"`
	TVDBID         int    `json:"tvdbId,omitempty"`
	Title          string `json:"title"`
	Year           int    `json:"year,omitempty"`
	RootFolderPath string `json:"rootFolderPath"`
	CreatedAt      string `json:"createdAt"`
	UpdatedAt      string `json:"updatedAt"`
}

// Episode is one canonical episode of a Series, whether or not it's
// actually on disk yet — FilePath is "" for an episode TMDB reports but
// that hasn't been found/grabbed, which is exactly what makes "missing
// episodes" a plain query (see MissingEpisodes) instead of a separately
// tracked state.
type Episode struct {
	ID            int64  `json:"id"`
	SeriesID      int64  `json:"seriesId"`
	SeasonNumber  int    `json:"seasonNumber"`
	EpisodeNumber int    `json:"episodeNumber"`
	Title         string `json:"title,omitempty"`
	AirDate       string `json:"airDate,omitempty"`
	FilePath      string `json:"filePath"`
	// PHash is the SAK-computed perceptual hash of this episode's video file,
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

// UpsertSeries creates a series, or updates it if one already exists for
// the same TMDB id — mirrors Upsert's re-entrant "this is now what I have"
// shape, used both by the one-time Sonarr importer and Rename/Search's
// get-or-create-by-TMDBID calls.
func (s *Store) UpsertSeries(ctx context.Context, series Series) (Series, error) {
	row := s.db.QueryRowContext(ctx, `
		INSERT INTO library_series (tmdb_id, tvdb_id, title, year, root_folder_path)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(tmdb_id) DO UPDATE SET
			tvdb_id = excluded.tvdb_id,
			title = excluded.title,
			year = excluded.year,
			root_folder_path = excluded.root_folder_path,
			updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
		RETURNING id, created_at, updated_at
	`, series.TMDBID, series.TVDBID, series.Title, series.Year, series.RootFolderPath)

	if err := row.Scan(&series.ID, &series.CreatedAt, &series.UpdatedAt); err != nil {
		return Series{}, fmt.Errorf("upserting series %q: %w", series.Title, err)
	}
	return series, nil
}

// GetSeriesByTMDBID looks up a series by its TMDB id — the duplicate-
// detection/get-or-create key Rename, Search, and the Sonarr importer use.
func (s *Store) GetSeriesByTMDBID(ctx context.Context, tmdbID int) (*Series, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, tmdb_id, tvdb_id, title, year, root_folder_path, created_at, updated_at
		FROM library_series WHERE tmdb_id = ?
	`, tmdbID)
	series, err := scanSeries(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("loading series for tmdb id %d: %w", tmdbID, err)
	}
	return &series, nil
}

// ListSeries returns every tracked series, ordered by title.
func (s *Store) ListSeries(ctx context.Context) ([]Series, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, tmdb_id, tvdb_id, title, year, root_folder_path, created_at, updated_at
		FROM library_series ORDER BY title
	`)
	if err != nil {
		return nil, fmt.Errorf("listing series: %w", err)
	}
	defer rows.Close()

	out := []Series{}
	for rows.Next() {
		series, err := scanSeries(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning series: %w", err)
		}
		out = append(out, series)
	}
	return out, rows.Err()
}

// DeleteSeries permanently removes seriesID, its episodes, and its tags.
// Explicit multi-statement delete rather than relying on the schema's
// declared foreign keys — same reasoning as Store.Delete: SQLite only
// enforces them when a connection has run `PRAGMA foreign_keys = ON`,
// which internal/db's shared Open doesn't set.
func (s *Store) DeleteSeries(ctx context.Context, seriesID int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("deleting series %d: %w", seriesID, err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `DELETE FROM library_episodes WHERE series_id = ?`, seriesID); err != nil {
		return fmt.Errorf("deleting episodes for series %d: %w", seriesID, err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM library_series_tags WHERE series_id = ?`, seriesID); err != nil {
		return fmt.Errorf("deleting tags for series %d: %w", seriesID, err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM library_series WHERE id = ?`, seriesID); err != nil {
		return fmt.Errorf("deleting series %d: %w", seriesID, err)
	}
	return tx.Commit()
}

// UpsertEpisode creates or updates the row for one (seriesID, season,
// episode) — the same call records both "TMDB says this episode exists"
// (FilePath left "") and "we found/grabbed the file" (FilePath set),
// exactly mirroring how Upsert's idempotent shape works for Item.
func (s *Store) UpsertEpisode(ctx context.Context, ep Episode) (Episode, error) {
	row := s.db.QueryRowContext(ctx, `
		INSERT INTO library_episodes (series_id, season_number, episode_number, title, air_date, file_path, phash, phash_file_size, phash_file_mtime)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(series_id, season_number, episode_number) DO UPDATE SET
			title = excluded.title,
			air_date = excluded.air_date,
			file_path = excluded.file_path,
			phash = excluded.phash,
			phash_file_size = excluded.phash_file_size,
			phash_file_mtime = excluded.phash_file_mtime,
			updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
		RETURNING id, created_at, updated_at
	`, ep.SeriesID, ep.SeasonNumber, ep.EpisodeNumber, ep.Title, ep.AirDate, ep.FilePath, ep.PHash, ep.PHashFileSize, ep.PHashFileMTime)

	if err := row.Scan(&ep.ID, &ep.CreatedAt, &ep.UpdatedAt); err != nil {
		return Episode{}, fmt.Errorf("upserting episode s%de%d for series %d: %w", ep.SeasonNumber, ep.EpisodeNumber, ep.SeriesID, err)
	}
	return ep, nil
}

// UpdateEpisodePHash writes a freshly-computed perceptual hash and its
// file-identity key (size + mtime) onto an existing tracked episode, without
// rewriting the rest of the row — the targeted write Dedup's Scan uses to
// cache a tracked episode's hash mid-scan (orphans have no row yet; their
// surviving winner's hash is persisted via UpsertEpisode in
// ApplyLibrarySeries). Kept separate from UpsertEpisode precisely so caching a
// hash never touches title/air_date/file_path. Updating an id that doesn't
// exist is not an error, matching DeleteSeries's convention.
func (s *Store) UpdateEpisodePHash(ctx context.Context, id int64, phash string, fileSize int64, fileMTime string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE library_episodes
		SET phash = ?, phash_file_size = ?, phash_file_mtime = ?,
		    updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
		WHERE id = ?
	`, phash, fileSize, fileMTime, id)
	if err != nil {
		return fmt.Errorf("updating phash for library episode %d: %w", id, err)
	}
	return nil
}

// GetEpisode returns a single episode by (seriesID, season, episode), or
// ErrNotFound if no such row exists yet — used by Rename's Apply to check
// for existing title/air-date metadata (e.g. from a prior Sonarr import or
// Scan) before overwriting it with a freshly-relocated file.
func (s *Store) GetEpisode(ctx context.Context, seriesID int64, seasonNumber, episodeNumber int) (*Episode, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, series_id, season_number, episode_number, title, air_date, file_path, phash, phash_file_size, phash_file_mtime, created_at, updated_at
		FROM library_episodes WHERE series_id = ? AND season_number = ? AND episode_number = ?
	`, seriesID, seasonNumber, episodeNumber)
	ep, err := scanEpisode(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("loading episode s%de%d for series %d: %w", seasonNumber, episodeNumber, seriesID, err)
	}
	return &ep, nil
}

// ListEpisodes returns every episode of seriesID, ordered by season then
// episode number.
func (s *Store) ListEpisodes(ctx context.Context, seriesID int64) ([]Episode, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, series_id, season_number, episode_number, title, air_date, file_path, phash, phash_file_size, phash_file_mtime, created_at, updated_at
		FROM library_episodes WHERE series_id = ? ORDER BY season_number, episode_number
	`, seriesID)
	if err != nil {
		return nil, fmt.Errorf("listing episodes for series %d: %w", seriesID, err)
	}
	defer rows.Close()

	out := []Episode{}
	for rows.Next() {
		ep, err := scanEpisode(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning episode: %w", err)
		}
		out = append(out, ep)
	}
	return out, rows.Err()
}

// MissingEpisodes returns every episode of seriesID TMDB reports but that
// has no file on disk yet (FilePath == "") — the query "missing episodes"
// reduces to, now that TMDB's full episode list is recorded up front by
// the Sonarr importer/Rename's ScanLibrarySeries instead of inferred.
func (s *Store) MissingEpisodes(ctx context.Context, seriesID int64) ([]Episode, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, series_id, season_number, episode_number, title, air_date, file_path, phash, phash_file_size, phash_file_mtime, created_at, updated_at
		FROM library_episodes WHERE series_id = ? AND file_path = '' ORDER BY season_number, episode_number
	`, seriesID)
	if err != nil {
		return nil, fmt.Errorf("listing missing episodes for series %d: %w", seriesID, err)
	}
	defer rows.Close()

	out := []Episode{}
	for rows.Next() {
		ep, err := scanEpisode(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning episode: %w", err)
		}
		out = append(out, ep)
	}
	return out, rows.Err()
}

// SeriesTags returns seriesID's assigned tags, alphabetically. Tags live at
// the series level, not per-episode — matching Sonarr's own tag model, and
// the only sane granularity for Purge (which removes a whole series at a
// time, see internal/purge).
func (s *Store) SeriesTags(ctx context.Context, seriesID int64) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT tag FROM library_series_tags WHERE series_id = ? ORDER BY tag`, seriesID)
	if err != nil {
		return nil, fmt.Errorf("listing tags for series %d: %w", seriesID, err)
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

// AddSeriesTag assigns tag to seriesID. A no-op (not an error) if already assigned.
func (s *Store) AddSeriesTag(ctx context.Context, seriesID int64, tag string) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO library_series_tags (series_id, tag) VALUES (?, ?)
		ON CONFLICT(series_id, tag) DO NOTHING
	`, seriesID, tag)
	if err != nil {
		return fmt.Errorf("adding tag %q to series %d: %w", tag, seriesID, err)
	}
	return nil
}

// RemoveSeriesTag unassigns tag from seriesID. A no-op if it wasn't assigned.
func (s *Store) RemoveSeriesTag(ctx context.Context, seriesID int64, tag string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM library_series_tags WHERE series_id = ? AND tag = ?`, seriesID, tag)
	if err != nil {
		return fmt.Errorf("removing tag %q from series %d: %w", tag, seriesID, err)
	}
	return nil
}

// SeriesTagVocabulary returns every distinct tag currently used by any
// series — what a Tag picker autocompletes against, same principle as
// TagVocabulary.
func (s *Store) SeriesTagVocabulary(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT tag FROM library_series_tags ORDER BY tag`)
	if err != nil {
		return nil, fmt.Errorf("listing series tag vocabulary: %w", err)
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

func scanSeries(row rowScanner) (Series, error) {
	var series Series
	err := row.Scan(&series.ID, &series.TMDBID, &series.TVDBID, &series.Title, &series.Year,
		&series.RootFolderPath, &series.CreatedAt, &series.UpdatedAt)
	return series, err
}

func scanEpisode(row rowScanner) (Episode, error) {
	var ep Episode
	err := row.Scan(&ep.ID, &ep.SeriesID, &ep.SeasonNumber, &ep.EpisodeNumber,
		&ep.Title, &ep.AirDate, &ep.FilePath, &ep.PHash, &ep.PHashFileSize, &ep.PHashFileMTime,
		&ep.CreatedAt, &ep.UpdatedAt)
	return ep, err
}

// episodePattern matches "S03E05"/"s3e5" style episode markers.
// altEpisodePattern falls back to the older "3x05" style. Both are
// best-effort, same posture as internal/searchterm's own doc comment —
// real-world release names are inconsistent enough that a full parser
// isn't worth building for this.
var (
	episodePattern    = regexp.MustCompile(`(?i)S(\d{1,2})E(\d{1,3})`)
	altEpisodePattern = regexp.MustCompile(`(?i)\b(\d{1,2})x(\d{1,3})\b`)
)

// ParseEpisodeFilename best-effort extracts a season and episode number
// from name (a release or file name). ok is false if neither pattern
// matches.
func ParseEpisodeFilename(name string) (season, episode int, ok bool) {
	if m := episodePattern.FindStringSubmatch(name); m != nil {
		season, _ = strconv.Atoi(m[1])
		episode, _ = strconv.Atoi(m[2])
		return season, episode, true
	}
	if m := altEpisodePattern.FindStringSubmatch(name); m != nil {
		season, _ = strconv.Atoi(m[1])
		episode, _ = strconv.Atoi(m[2])
		return season, episode, true
	}
	return 0, 0, false
}

// StripEpisodeMarker removes the first SxxExx/NxNN token (and everything
// after it) from name, so what's left is just the show title — the
// preprocessing searchterm.FromName needs before it runs, since that
// package is general-purpose and deliberately doesn't know about TV-style
// episode markers.
func StripEpisodeMarker(name string) string {
	if loc := episodePattern.FindStringIndex(name); loc != nil {
		return trimSeparators(name[:loc[0]])
	}
	if loc := altEpisodePattern.FindStringIndex(name); loc != nil {
		return trimSeparators(name[:loc[0]])
	}
	return name
}

// trimSeparators trims whitespace and the "." "-" "_" characters release
// names commonly use in place of spaces, left over at the cut point once
// an episode marker (and everything after it) is removed.
func trimSeparators(s string) string {
	return strings.TrimRight(strings.TrimSpace(s), ".-_ ")
}

// ResolveEpisodeVideoFiles is ResolveVideoFile's season-pack-aware sibling:
// if path is a file, returns just that file; if it's a directory, returns
// EVERY video-extensioned file inside (not just the largest — a season
// pack legitimately contains many different-sized episodes), skipping
// sidecars the same way ScanRootFolder does.
func ResolveEpisodeVideoFiles(path string) ([]string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("stat %s: %w", path, err)
	}
	if !info.IsDir() {
		return []string{path}, nil
	}

	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() || !videoExts[strings.ToLower(filepath.Ext(e.Name()))] {
			continue
		}
		out = append(out, filepath.Join(path, e.Name()))
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no video files found under %s", path)
	}
	return out, nil
}
