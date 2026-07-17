package library

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
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

// UpsertEpisodes is UpsertEpisode's atomic-batch sibling: every row in eps
// is upserted within ONE transaction, so a failure partway through rolls
// back everything already written. This matters specifically for a
// logical-episode-split file (rename.ApplyLibrarySeries): the file is
// relocated exactly once, then one Episode row is upserted per bundled
// number. Without a shared transaction, a failure on episode 2's upsert
// (after episode 1's already committed) would leave the relocated file
// "known" — ScanRootFolder masks any already-tracked FilePath from ever
// being reported as an orphan again — with episode 2's row still missing
// and unrecoverable by a later re-Scan. Wrapping every number's upsert in
// one transaction means a partial failure leaves nothing committed, so a
// re-Scan can still discover and correctly resolve the file. eps[0] is
// expected to be the primary episode's row when this is used for that
// purpose, but the function itself is order-agnostic.
func (s *Store) UpsertEpisodes(ctx context.Context, eps []Episode) ([]Episode, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("upserting episodes: %w", err)
	}
	defer tx.Rollback()

	out := make([]Episode, len(eps))
	for i, ep := range eps {
		row := tx.QueryRowContext(ctx, `
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
			return nil, fmt.Errorf("upserting episode s%de%d for series %d: %w", ep.SeasonNumber, ep.EpisodeNumber, ep.SeriesID, err)
		}
		out[i] = ep
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("committing episode upserts: %w", err)
	}
	return out, nil
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

// CountEpisodesByFilePath reports how many Episode rows (across every
// series, not scoped to one) currently have exactly filePath as their
// FilePath. A path names exactly one filesystem location, so this is a
// global lookup, not a per-series one. Dedup's ApplyLibrarySeries
// (internal/dedup/dedup.go) uses this to guard against deleting a file a
// logical-episode-split sibling row still references: a count <= 1 means
// only the row about to be overwritten (if any) claims this path, safe to
// delete; > 1 means another row still needs it.
//
// This is a pure string-equality lookup — it holds only because every
// writer of a split file's sibling rows (rename.ApplyLibrarySeries's extra-
// episode loop, search.go's check-import multi-episode loop) upserts every
// sibling with the SAME already-relocated path variable in one call, never
// re-deriving or re-normalizing it per row. If a future writer ever stored
// a differently-formatted-but-equivalent path for one sibling (e.g. after
// symlink resolution or a Clean() only one side applies), this guard would
// silently stop protecting that file — flagged here since Dedup's own scan
// can never surface a counterexample itself: ScanLibrarySeries's `known`
// set masks every already-tracked FilePath from ever being re-discovered
// as an unmapped/orphan entry in the first place, so a shared file can only
// ever reach ApplyLibrarySeries labeled as ITS OWN "tracked" candidate, with
// the exact DB-stored string — never as a scan-produced orphan path that
// could diverge from it.
func (s *Store) CountEpisodesByFilePath(ctx context.Context, filePath string) (int, error) {
	var count int
	if err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM library_episodes WHERE file_path = ?
	`, filePath).Scan(&count); err != nil {
		return 0, fmt.Errorf("counting episodes for file path %q: %w", filePath, err)
	}
	return count, nil
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
//
// concatPrefixPattern/concatNumPattern and rangeSuffixPattern/
// altRangeSuffixPattern detect a bundled multi-episode filename
// immediately following the primary match — concatenated ("S01E01E02E03")
// or dash-range ("S01E01-E02", "01x01-02"). Go's RE2 engine has no
// repeated-capture-group extraction (unlike PCRE), so concatenated numbers
// are pulled out with a second, non-anchored FindAllStringSubmatch pass
// over just the matched prefix substring, not one combined regex.
var (
	episodePattern        = regexp.MustCompile(`(?i)S(\d{1,2})E(\d{1,3})`)
	altEpisodePattern     = regexp.MustCompile(`(?i)\b(\d{1,2})x(\d{1,3})\b`)
	concatPrefixPattern   = regexp.MustCompile(`(?i)^(?:E\d{1,3})+`)
	concatNumPattern      = regexp.MustCompile(`(?i)E(\d{1,3})`)
	rangeSuffixPattern    = regexp.MustCompile(`(?i)^-E?(\d{1,3})`)
	altRangeSuffixPattern = regexp.MustCompile(`^-(\d{1,3})`)
)

// maxEpisodeRangeSpan caps a dash-range expansion (e.g. "S01E01-E02") to
// reject a pathological misparse — "S01E01-E99" expanding into 99
// fabricated episode rows — rather than trusting an arbitrarily large gap.
const maxEpisodeRangeSpan = 26

// expandRange returns the inclusive [first, last] integer sequence, or just
// []int{first} if the range is invalid (last < first) or exceeds
// maxEpisodeRangeSpan — the same "don't trust an implausible parse" posture
// as the rest of this file's best-effort parsing.
func expandRange(first, last int) []int {
	if last < first || last-first+1 > maxEpisodeRangeSpan {
		return []int{first}
	}
	out := make([]int, 0, last-first+1)
	for n := first; n <= last; n++ {
		out = append(out, n)
	}
	return out
}

// dedupSorted returns nums deduplicated and ascending-sorted.
func dedupSorted(nums []int) []int {
	seen := make(map[int]bool, len(nums))
	out := make([]int, 0, len(nums))
	for _, n := range nums {
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	sort.Ints(out)
	return out
}

// ParseEpisodeNumbers extracts a season and ALL bundled episode numbers from
// name (a release or file name) — the logical-episode-splitting parser.
// Supports three shapes on top of the plain single-episode case
// ParseEpisodeFilename already handled: concatenated multi-episode
// ("S01E01E02E03"), dash range ("S01E01-E02"/"S01E01-02", inclusive
// expansion), and the alt-format's own range sibling ("01x01-02"). Returns
// a deduped, ascending-sorted slice; ok is false only when neither the
// SxxExx nor NxNN format matches at all — the same "no match" contract
// ParseEpisodeFilename already had.
func ParseEpisodeNumbers(name string) (season int, episodes []int, ok bool) {
	if loc := episodePattern.FindStringSubmatchIndex(name); loc != nil {
		season, _ = strconv.Atoi(name[loc[2]:loc[3]])
		first, _ := strconv.Atoi(name[loc[4]:loc[5]])
		rest := name[loc[1]:]
		switch {
		case concatPrefixPattern.MatchString(rest):
			prefix := concatPrefixPattern.FindString(rest)
			nums := []int{first}
			for _, m := range concatNumPattern.FindAllStringSubmatch(prefix, -1) {
				n, _ := strconv.Atoi(m[1])
				nums = append(nums, n)
			}
			return season, dedupSorted(nums), true
		case rangeSuffixPattern.MatchString(rest):
			m := rangeSuffixPattern.FindStringSubmatch(rest)
			last, _ := strconv.Atoi(m[1])
			return season, expandRange(first, last), true
		default:
			return season, []int{first}, true
		}
	}
	if loc := altEpisodePattern.FindStringSubmatchIndex(name); loc != nil {
		season, _ = strconv.Atoi(name[loc[2]:loc[3]])
		first, _ := strconv.Atoi(name[loc[4]:loc[5]])
		rest := name[loc[1]:]
		if altRangeSuffixPattern.MatchString(rest) {
			m := altRangeSuffixPattern.FindStringSubmatch(rest)
			last, _ := strconv.Atoi(m[1])
			return season, expandRange(first, last), true
		}
		return season, []int{first}, true
	}
	return 0, nil, false
}

// ParseEpisodeFilename best-effort extracts a season and PRIMARY episode
// number from name — a thin wrapper over ParseEpisodeNumbers returning just
// the first bundled episode number, for the many existing callers that only
// ever need one (Dedup's orphan matching, autograb's grab-time lookups,
// etc. — see ParseEpisodeNumbers' doc comment for why Dedup deliberately
// stays on this single-episode view rather than adopting the full list).
func ParseEpisodeFilename(name string) (season, episode int, ok bool) {
	season, episodes, ok := ParseEpisodeNumbers(name)
	if !ok {
		return 0, 0, false
	}
	return season, episodes[0], true
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
