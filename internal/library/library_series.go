package library

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/labbersanon/sakms/internal/config"
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
	// Genres and Cast are populated at Apply time from the proposal's TMDB
	// enrichment. Empty for series applied before this feature landed.
	Genres    []string `json:"genres,omitempty"`
	Cast      []string `json:"cast,omitempty"`
	CreatedAt string   `json:"createdAt"`
	UpdatedAt string   `json:"updatedAt"`
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
	genresJSON, err := marshalStringSlice(series.Genres)
	if err != nil {
		return Series{}, fmt.Errorf("encoding genres for %q: %w", series.Title, err)
	}
	castJSON, err := marshalStringSlice(series.Cast)
	if err != nil {
		return Series{}, fmt.Errorf("encoding cast for %q: %w", series.Title, err)
	}
	row := s.db.QueryRowContext(ctx, `
		INSERT INTO library_series (tmdb_id, tvdb_id, title, year, root_folder_path, genres, "cast")
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(tmdb_id) DO UPDATE SET
			tvdb_id = excluded.tvdb_id,
			title = excluded.title,
			year = excluded.year,
			root_folder_path = excluded.root_folder_path,
			genres = excluded.genres,
			"cast" = excluded."cast",
			updated_at = sakms_now()
		RETURNING id, created_at, updated_at
	`, series.TMDBID, series.TVDBID, series.Title, series.Year, series.RootFolderPath, genresJSON, castJSON)

	if err := row.Scan(&series.ID, &series.CreatedAt, &series.UpdatedAt); err != nil {
		return Series{}, fmt.Errorf("upserting series %q: %w", series.Title, err)
	}
	return series, nil
}

// GetSeriesByTMDBID looks up a series by its TMDB id — the duplicate-
// detection/get-or-create key Rename, Search, and the Sonarr importer use.
func (s *Store) GetSeriesByTMDBID(ctx context.Context, tmdbID int) (*Series, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, tmdb_id, tvdb_id, title, year, root_folder_path,
		       COALESCE(genres, '[]'), COALESCE("cast", '[]'),
		       created_at, updated_at
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

// GetSeries looks up a series by its own row id — GetSeriesByTMDBID's sibling
// for the callers that already hold the library's id rather than TMDB's (the
// per-season monitored routes, whose path carries {seriesID}, and which need
// the row's TMDBID to correlate it with grab rows). Same not-found contract:
// ErrNotFound when no such series exists.
func (s *Store) GetSeries(ctx context.Context, seriesID int64) (*Series, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, tmdb_id, tvdb_id, title, year, root_folder_path,
		       COALESCE(genres, '[]'), COALESCE("cast", '[]'),
		       created_at, updated_at
		FROM library_series WHERE id = ?
	`, seriesID)
	series, err := scanSeries(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("loading series %d: %w", seriesID, err)
	}
	return &series, nil
}

// ListSeries returns every tracked series, ordered by title.
func (s *Store) ListSeries(ctx context.Context) ([]Series, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, tmdb_id, tvdb_id, title, year, root_folder_path,
		       COALESCE(genres, '[]'), COALESCE("cast", '[]'),
		       created_at, updated_at
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

// DeleteSeries permanently removes seriesID, its episodes, its tags, and its
// per-season monitored flags. Explicit multi-statement delete rather than
// relying on the schema's declared foreign keys — same reasoning as
// Store.Delete: SQLite only enforces them when a connection has run
// `PRAGMA foreign_keys = ON`, which internal/db's shared Open doesn't set.
//
// The library_season_monitored delete is not optional bookkeeping: a monitored
// row whose series_id no longer exists leaks on every delete, and it makes
// ListSeasonStates' UNION report phantom seasons for that id.
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
	if _, err := tx.ExecContext(ctx, `DELETE FROM library_season_monitored WHERE series_id = ?`, seriesID); err != nil {
		return fmt.Errorf("deleting season monitored flags for series %d: %w", seriesID, err)
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
		INSERT INTO library_episodes (series_id, season_number, episode_number, title, air_date, file_path, phash, phash_file_size, phash_file_mtime, size, quality_tier)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(series_id, season_number, episode_number) DO UPDATE SET
			title = excluded.title,
			air_date = excluded.air_date,
			file_path = excluded.file_path,
			phash = excluded.phash,
			phash_file_size = excluded.phash_file_size,
			phash_file_mtime = excluded.phash_file_mtime,
			size = excluded.size,
			quality_tier = excluded.quality_tier,
			updated_at = sakms_now()
		RETURNING id, created_at, updated_at
	`, ep.SeriesID, ep.SeasonNumber, ep.EpisodeNumber, ep.Title, ep.AirDate, ep.FilePath, ep.PHash, ep.PHashFileSize, ep.PHashFileMTime, ep.Size, ep.QualityTier)

	if err := row.Scan(&ep.ID, &ep.CreatedAt, &ep.UpdatedAt); err != nil {
		return Episode{}, fmt.Errorf("upserting episode s%de%d for series %d: %w", ep.SeasonNumber, ep.EpisodeNumber, ep.SeriesID, err)
	}
	// Claude 2026-08-08: keep the library_episode_files primary row in sync with the denormalized file_path
	// Reason: Series parity with Upsert's SyncPrimaryFile call (library.go:135) — every writer of this
	//   row (Rename Apply, Series Dedup Apply, the importer) goes through here, so hooking it once here
	//   covers all of them without a second call site in any of them.
	// Troubleshooting: a Series Dedup Apply rewrote file_path to the surviving winner while
	//   library_episode_files still flagged the DELETED loser as primary — silently, no error, no log.
	// Review if: the primary path moves exclusively onto library_episode_files
	if err := s.SyncPrimaryEpisodeFile(ctx, ep); err != nil {
		return Episode{}, fmt.Errorf("syncing primary file for episode s%de%d: %w", ep.SeasonNumber, ep.EpisodeNumber, err)
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
//
// AMENDED 2026-08-08: the transaction covers THE UPSERTS ONLY. The
// SyncPrimaryEpisodeFile loop added below runs after tx.Commit(), so a non-nil
// error from this function no longer guarantees that nothing was committed —
// every episode row is already durable by the time that loop can fail. See the
// comment block at that loop for why post-commit is deliberate.
func (s *Store) UpsertEpisodes(ctx context.Context, eps []Episode) ([]Episode, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("upserting episodes: %w", err)
	}
	defer tx.Rollback()

	out := make([]Episode, len(eps))
	for i, ep := range eps {
		row := tx.QueryRowContext(ctx, `
			INSERT INTO library_episodes (series_id, season_number, episode_number, title, air_date, file_path, phash, phash_file_size, phash_file_mtime, size, quality_tier)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(series_id, season_number, episode_number) DO UPDATE SET
				title = excluded.title,
				air_date = excluded.air_date,
				file_path = excluded.file_path,
				phash = excluded.phash,
				phash_file_size = excluded.phash_file_size,
				phash_file_mtime = excluded.phash_file_mtime,
				size = excluded.size,
				quality_tier = excluded.quality_tier,
				updated_at = sakms_now()
			RETURNING id, created_at, updated_at
		`, ep.SeriesID, ep.SeasonNumber, ep.EpisodeNumber, ep.Title, ep.AirDate, ep.FilePath, ep.PHash, ep.PHashFileSize, ep.PHashFileMTime, ep.Size, ep.QualityTier)
		if err := row.Scan(&ep.ID, &ep.CreatedAt, &ep.UpdatedAt); err != nil {
			return nil, fmt.Errorf("upserting episode s%de%d for series %d: %w", ep.SeasonNumber, ep.EpisodeNumber, ep.SeriesID, err)
		}
		out[i] = ep
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("committing episode upserts: %w", err)
	}
	// Claude 2026-08-08: sync each row's library_episode_files primary entry, POST-COMMIT
	// Reason: post-commit rather than inside the tx above is deliberate and is exact Movies parity —
	//   Upsert uses no transaction at all, so SyncPrimaryFile is likewise unatomic there, and running
	//   an s.db statement against rows still held in an open tx would read stale state or block.
	// Troubleshooting: a logical-episode-split Apply upserts N rows here; without this loop only the
	//   single-row UpsertEpisode path kept library_episode_files correct.
	// Review if: this batch gains its own tx-scoped Store handle, at which point the sync can move inside.
	// NON-ATOMIC BY DESIGN: a crash between the commit and these syncs leaves a stale primary file
	// row behind, self-healed by the next Apply for that slot.
	for _, ep := range out {
		if err := s.SyncPrimaryEpisodeFile(ctx, ep); err != nil {
			return nil, fmt.Errorf("syncing primary file for episode s%de%d: %w", ep.SeasonNumber, ep.EpisodeNumber, err)
		}
	}
	return out, nil
}

// UpsertEpisodeCatalog records TMDB's CATALOG facts for one episode — its
// title and air date — and nothing else. On INSERT it creates the fileless
// row (file_path = ”) that makes an episode "missing"; on CONFLICT it updates
// ONLY title and air_date.
//
// It STRUCTURALLY CANNOT touch file_path, size, quality_tier or the phash
// triple. That is the entire reason it exists rather than reusing
// UpsertEpisode: this writer runs unattended from the auto-grab cycle over
// every tracked series, and UpsertEpisode's full-row overwrite would blank a
// tracked file's path if handed a zero-valued Episode.
//
// The INSERT deliberately names only the columns this writer sets. Every other
// episode column carries its own schema-level NOT NULL DEFAULT, so restating
// them buys nothing and costs a typo class that is easy to get wrong
// (phash_file_mtime is TEXT, not INTEGER) — and omitting them keeps this
// statement correct for free if a later migration adds another defaulted
// column.
//
// COALESCE(NULLIF(...)) is load-bearing, not defensive tidiness: TMDB
// legitimately returns an empty name/air_date for unannounced or placeholder
// episodes, and this runs every cycle. A plain `air_date = excluded.air_date`
// would blank an episode's real air date on the first such response, making it
// permanently ineligible for air-date detection, silently.
func (s *Store) UpsertEpisodeCatalog(ctx context.Context, seriesID int64, seasonNumber, episodeNumber int, title, airDate string) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO library_episodes (series_id, season_number, episode_number, title, air_date)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(series_id, season_number, episode_number) DO UPDATE SET
			title      = COALESCE(NULLIF(excluded.title, ''), library_episodes.title),
			air_date   = COALESCE(NULLIF(excluded.air_date, ''), library_episodes.air_date),
			updated_at = sakms_now()
	`, seriesID, seasonNumber, episodeNumber, title, airDate)
	if err != nil {
		return fmt.Errorf("upserting episode catalog s%de%d for series %d: %w", seasonNumber, episodeNumber, seriesID, err)
	}
	return nil
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
		    updated_at = sakms_now()
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
		SELECT id, series_id, season_number, episode_number, title, air_date, file_path, phash, phash_file_size, phash_file_mtime, created_at, updated_at, size, quality_tier
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
		SELECT id, series_id, season_number, episode_number, title, air_date, file_path, phash, phash_file_size, phash_file_mtime, created_at, updated_at, size, quality_tier
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
		SELECT id, series_id, season_number, episode_number, title, air_date, file_path, phash, phash_file_size, phash_file_mtime, created_at, updated_at, size, quality_tier
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

// SeasonState is one season's row in the per-season monitoring UI: how many
// episodes it has, how many of those are still missing, and whether it is
// monitored.
type SeasonState struct {
	SeasonNumber int  `json:"seasonNumber"`
	EpisodeCount int  `json:"episodeCount"`
	MissingCount int  `json:"missingCount"`
	Monitored    bool `json:"monitored"`
}

// MonitoredSeasons returns seriesID's monitored season numbers as a set.
//
// ONE query per series, returning only monitored = true rows — deliberately not a
// per-season SeasonMonitored lookup, which would be an N+1 inside the auto-grab
// cycle's per-series loop. An absent key means unmonitored, which is the same
// answer an absent ROW gives: there is no tri-state (see migration 0056).
func (s *Store) MonitoredSeasons(ctx context.Context, seriesID int64) (map[int]bool, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT season_number FROM library_season_monitored WHERE series_id = ? AND monitored = true
	`, seriesID)
	if err != nil {
		return nil, fmt.Errorf("listing monitored seasons for series %d: %w", seriesID, err)
	}
	defer rows.Close()

	out := map[int]bool{}
	for rows.Next() {
		var season int
		if err := rows.Scan(&season); err != nil {
			return nil, fmt.Errorf("scanning monitored season: %w", err)
		}
		out[season] = true
	}
	return out, rows.Err()
}

// SeasonMonitorFlags returns every (season -> monitored) row that EXISTS for
// seriesID, unfiltered by value — unlike MonitoredSeasons, which only returns
// monitored = true rows. An absent key here means "never toggled"; a present key
// with value false means "explicitly un-monitored".
//
// The destructive reap branch in airdatemonitor.go's backoff sweep needs that
// distinction: with MonitoredSeasons alone it cannot tell an explicit un-monitor
// from a season this feature has never touched — which is the state of EVERY
// season on EVERY install by default — and so kills operator-initiated retry
// rows that merely share the air-date shape. The DISPATCH filter deliberately
// does NOT want this: there, absent-means-unmonitored is the safe, intentional
// default (see migration 0056).
func (s *Store) SeasonMonitorFlags(ctx context.Context, seriesID int64) (map[int]bool, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT season_number, monitored FROM library_season_monitored WHERE series_id = ?
	`, seriesID)
	if err != nil {
		return nil, fmt.Errorf("listing season monitor flags for series %d: %w", seriesID, err)
	}
	defer rows.Close()

	out := map[int]bool{}
	for rows.Next() {
		var season int
		var monitored bool
		if err := rows.Scan(&season, &monitored); err != nil {
			return nil, fmt.Errorf("scanning season monitor flag: %w", err)
		}
		out[season] = monitored
	}
	return out, rows.Err()
}

// SetSeasonMonitored records whether (seriesID, seasonNumber) is monitored,
// creating the row if this season has never been toggled before.
func (s *Store) SetSeasonMonitored(ctx context.Context, seriesID int64, seasonNumber int, monitored bool) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO library_season_monitored (series_id, season_number, monitored)
		VALUES (?, ?, ?)
		ON CONFLICT(series_id, season_number) DO UPDATE SET
			monitored  = excluded.monitored,
			updated_at = sakms_now()
	`, seriesID, seasonNumber, monitored)
	if err != nil {
		return fmt.Errorf("setting season %d monitored for series %d: %w", seasonNumber, seriesID, err)
	}
	return nil
}

// ListSeasonStates returns one row per season of seriesID, ascending — the
// per-season monitoring UI's read.
//
// The season set is the UNION of two sources, and both halves are needed: a
// season with episode rows but no monitored row yet (every season on an
// install that predates this feature), and a season with a monitored row but no
// episode rows yet (a season discovered from TMDB before its episodes are
// synced). The monitored side of that union is deliberately NOT filtered to
// monitored = true, unlike MonitoredSeasons: an episode-less season the operator
// un-monitors would otherwise vanish from this list and become impossible to
// re-enable.
func (s *Store) ListSeasonStates(ctx context.Context, seriesID int64) ([]SeasonState, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT seasons.season_number,
		       COALESCE(counts.episode_count, 0),
		       COALESCE(counts.missing_count, 0),
		       COALESCE(flags.monitored, false)
		  FROM (SELECT DISTINCT season_number FROM library_episodes WHERE series_id = ?
		        UNION
		        SELECT season_number FROM library_season_monitored WHERE series_id = ?) AS seasons
		  LEFT JOIN (SELECT season_number,
		                    COUNT(*) AS episode_count,
		                    SUM(CASE WHEN file_path = '' THEN 1 ELSE 0 END) AS missing_count
		               FROM library_episodes WHERE series_id = ?
		              GROUP BY season_number) AS counts
		         ON counts.season_number = seasons.season_number
		  LEFT JOIN (SELECT season_number, monitored
		               FROM library_season_monitored WHERE series_id = ?) AS flags
		         ON flags.season_number = seasons.season_number
		 ORDER BY seasons.season_number
	`, seriesID, seriesID, seriesID, seriesID)
	if err != nil {
		return nil, fmt.Errorf("listing season states for series %d: %w", seriesID, err)
	}
	defer rows.Close()

	out := []SeasonState{}
	for rows.Next() {
		var st SeasonState
		if err := rows.Scan(&st.SeasonNumber, &st.EpisodeCount, &st.MissingCount, &st.Monitored); err != nil {
			return nil, fmt.Errorf("scanning season state: %w", err)
		}
		out = append(out, st)
	}
	return out, rows.Err()
}

// EpisodeTiersBySeries returns each series' distinct episode-FILE quality
// tiers, keyed by series id, sorted — for the tracked-item list's tier filter,
// which needs a series to match ANY tier its episodes were grabbed at
// (episodes land at different times under different settings, so forcing one
// value per series would be a fabricated majority vote).
//
// ONE query rather than one per series: listTrackedHandler already does a
// per-item Tags() lookup, and a second N+1 on the same route is not worth
// adding.
//
// The inner GROUP BY series_id, file_path + MAX(quality_tier) is load-bearing
// and must stay in lockstep with storageAllocationQuery's series subquery,
// which does exactly the same collapse. A Dashboard Storage Allocation tier
// cell DRILLS DOWN into the list this function filters, so the two must agree
// on which tier bucket a series belongs to. Several library_episodes rows can
// share one physical file (a S01E01-E02 double episode, a season pack broken
// into slots), and UpsertEpisode — the single-row writer internal/dedup uses —
// can leave two such rows momentarily divergent. Flattened back to a bare
// SELECT DISTINCT, this function would report the series under BOTH tiers
// while the aggregation counted it under only MAX(quality_tier)'s — clicking
// the other cell would then list a series that cell never counted. Note the
// return semantics that follow from this: these are the distinct tiers of the
// series' episode FILES, not of its episode ROWS.
//
// The empty-string tier is folded to "unknown" here, deliberately WITHOUT a
// filter excluding it — the two stored sentinels stay distinct, and both land
// in the same display bucket:
//
//	quality_tier = ''        -> never processed
//	quality_tier = 'unknown' -> processed, chain concluded Unknown
//
// The Dashboard's Storage Allocation grid folds both into one visible,
// clickable Unknown cell, so excluding the never-processed rows here would
// make that cell drill down to an empty list. The fold is display-only — the
// stored value is untouched, which is what keeps BackfillSizeAndTier
// idempotent.
//
// Episodes with an empty file_path are excluded: they are the deliberate
// "TMDB knows about this episode, it isn't tracked yet" rows and have no
// quality to report.
func (s *Store) EpisodeTiersBySeries(ctx context.Context) (map[int64][]string, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT DISTINCT series_id, tier
		  FROM (SELECT series_id, file_path,
		               CASE WHEN MAX(quality_tier) = '' THEN 'unknown' ELSE MAX(quality_tier) END AS tier
		          FROM library_episodes
		         WHERE file_path != ''
		         GROUP BY series_id, file_path) AS episode_files
		 ORDER BY series_id, tier
	`)
	if err != nil {
		return nil, fmt.Errorf("listing episode quality tiers: %w", err)
	}
	defer rows.Close()

	out := map[int64][]string{}
	for rows.Next() {
		var seriesID int64
		var tier string
		if err := rows.Scan(&seriesID, &tier); err != nil {
			return nil, fmt.Errorf("scanning episode quality tier: %w", err)
		}
		out[seriesID] = append(out[seriesID], tier)
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
		ON CONFLICT (series_id, (lower(tag))) DO NOTHING
	`, seriesID, tag)
	if err != nil {
		return fmt.Errorf("adding tag %q to series %d: %w", tag, seriesID, err)
	}
	return nil
}

// RemoveSeriesTag unassigns tag from seriesID. A no-op if it wasn't assigned.
func (s *Store) RemoveSeriesTag(ctx context.Context, seriesID int64, tag string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM library_series_tags WHERE series_id = ? AND lower(tag) = lower(?)`, seriesID, tag)
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
	var genresJSON, castJSON string
	if err := row.Scan(&series.ID, &series.TMDBID, &series.TVDBID, &series.Title, &series.Year,
		&series.RootFolderPath, &genresJSON, &castJSON,
		&series.CreatedAt, &series.UpdatedAt); err != nil {
		return Series{}, err
	}
	if err := json.Unmarshal([]byte(genresJSON), &series.Genres); err != nil {
		return Series{}, fmt.Errorf("decoding genres for series %d: %w", series.ID, err)
	}
	if err := json.Unmarshal([]byte(castJSON), &series.Cast); err != nil {
		return Series{}, fmt.Errorf("decoding cast for series %d: %w", series.ID, err)
	}
	return series, nil
}

func scanEpisode(row rowScanner) (Episode, error) {
	var ep Episode
	err := row.Scan(&ep.ID, &ep.SeriesID, &ep.SeasonNumber, &ep.EpisodeNumber,
		&ep.Title, &ep.AirDate, &ep.FilePath, &ep.PHash, &ep.PHashFileSize, &ep.PHashFileMTime,
		&ep.CreatedAt, &ep.UpdatedAt, &ep.Size, &ep.QualityTier)
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

// Claude 2026-08-06: rename-path-only loose fallback to the parent directory
// Reason: 34 real "The Path" library rows (diagnosis
//   .omc/artifacts/series-parse-failures-20260806.psv) have an opaque hash
//   basename with zero recognizable content, while the immediate parent
//   directory is a complete scene-release name
//   ("The.Path.S01E02.2160p.WEB.h265-NiXON") the EXISTING strict pattern
//   already parses correctly on its own — no new regex needed.
// Troubleshooting: "could not determine season/episode from %q" unmatched
//   reason for a hash-named file whose parent folder is a normal release name.
// Review if: ParseEpisodeNumbers' regex block (episodePattern/altEpisodePattern
//   and friends) changes shape — this must keep calling it unmodified, never
//   duplicate its logic, so the other three non-rename call sites
//   (import.go, releasematch.go, dedup_phash_primary.go) stay provably
//   unaffected (see .omc/plans/autopilot-impl.md §2.1's containment proof).

// ParseEpisodeNumbersLoose is ParseEpisodeNumbers' rename-path-only sibling:
// it tries basename with the unmodified strict parser first, and only when
// that fails, tries parentDir's own basename (i.e. the immediate containing
// directory's name, NOT the full path) with the SAME strict parser. Callers
// pass filepath.Dir(sourcePath) as parentDir. This is pure delegation — zero
// duplicated regex logic, and ParseEpisodeNumbers itself is untouched, so
// every other caller of ParseEpisodeNumbers is provably unaffected by this
// function's existence.
func ParseEpisodeNumbersLoose(basename, parentDir string) (season int, episodes []int, ok bool) {
	if season, episodes, ok = ParseEpisodeNumbers(basename); ok {
		return season, episodes, ok
	}
	return ParseEpisodeNumbers(filepath.Base(parentDir))
}

// Claude 2026-08-07: compact "eNNNN" episode-code siblings, rename-path-only (plan §1.1/§1.2)
// Reason: 13 real "The Red Skelton Show" rows name the episode as "eSSEE"
//   ("RED SKELTON e1524.mp4", "e614_720x480.mp4") with no SxxExx/NxNN marker in
//   the basename OR the parent directory, so ParseEpisodeNumbers(Loose) cannot
//   reach them. Kept OUT of episodePattern/ParseEpisodeNumbers so import.go,
//   releasematch.go, dedup_phash_primary.go and autograb (via
//   ParseEpisodeFilename) are provably unaffected — a WRONG (season, episode)
//   in Dedup's orphan matching is materially worse than no parse at all,
//   because Dedup deletes files.
// Troubleshooting: an eNNNN file still reports "could not determine
//   season/episode" — check compactEpisodeCodePattern's terminator class
//   before suspecting the search path.
// Review if: ParseEpisodeNumbers itself learns the compact shape, at which
//   point these three siblings become dead and must be deleted, not left.
// Related files: internal/rename/rename.go (the only consumer)

// compactEpisodeCodePattern matches the "eNNNN" compact episode-number
// convention: a word-boundary 'e' (case-insensitive) followed by exactly 3 or 4
// digits, terminated by end-of-string, a '_', a '.', a '-', or whitespace.
var compactEpisodeCodePattern = regexp.MustCompile(`(?i)(?:^|[\s._-])e(\d{3,4})(?:$|[\s._-])`)

// compactCodeResolutionBlocklist refuses a captured code whose digits are a
// standard video-height value. "e1080"/"e2160" are an ordinary release-name
// shape (a stray 'e' abutting a resolution), and reading them as eSSEE would
// assign S10E80 / S21E60 to perfectly ordinary library files. A value check,
// NOT a regex narrowing: right-anchoring the pattern refuses
// "Show.Name.e1080.WEB.mkv" but still parses "Movie_1080p_e2160.mkv", where the
// resolution IS the last token before the extension.
//
// ACCEPTED COST: a legitimate "e720" code — which under the eSSEE semantic
// would mean S07E20 — becomes unparseable. No such file exists in the current
// library, and a false positive on a real release name is materially worse (a
// WRONG Pending on an ordinary file) than no Pending on an unusual one. Such a
// file stays Unmatched and is closed by manual repick, the fail-closed outcome.
var compactCodeResolutionBlocklist = map[int]bool{
	480: true, 576: true, 720: true, 1080: true, 1440: true, 2160: true,
}

// compactEpisodeCodeMatch is the ONE match-selection helper both
// ParseCompactEpisodeCode and StripCompactEpisodeCode use, so the blocklist can
// never be applied by one and skipped by the other — a StripCompactEpisodeCode
// that stripped "e1080" while ParseCompactEpisodeCode refused it would corrupt
// the title seed of a file that was otherwise matching correctly.
//
// FAIL-CLOSED ON THE FIRST MATCH: only compactEpisodeCodePattern's FIRST match
// is considered. When that match's digits are blocklisted the helper reports
// "no token present" and does NOT scan rightward for a second candidate.
// Returns the full submatch-index slice (loc[2], loc[3] bracket the digits; the
// 'e' sits exactly one byte before loc[2]) alongside the parsed digit value.
func compactEpisodeCodeMatch(name string) ([]int, int, bool) {
	loc := compactEpisodeCodePattern.FindStringSubmatchIndex(name)
	if loc == nil {
		return nil, 0, false
	}
	digits, err := strconv.Atoi(name[loc[2]:loc[3]])
	if err != nil {
		return nil, 0, false
	}
	if compactCodeResolutionBlocklist[digits] {
		return nil, 0, false
	}
	return loc, digits, true
}

// ParseCompactEpisodeCode extracts a season and episode from the "eSSEE"
// compact convention ("RED SKELTON e1524.mp4" -> S15E24, "e614_720x480.mp4" ->
// S06E14). The LAST TWO digits are the episode; every leading digit is the
// season. ok is false when no such token is present, when the FIRST matched
// code's digits are in compactCodeResolutionBlocklist (fail-closed), when the
// derived season is 0, or when the derived episode is 0.
//
// Both zeros are refused rather than treated as Season 0 / Specials: unlike
// SxxE00, where an operator deliberately typed a Specials marker, a
// leading-zero-stripped compact code cannot distinguish "season 0" from
// "malformed", and proposals.Proposal.SeasonNumber's own doc comment promises
// that a Proposal's 0 unambiguously means "the filename encoded Specials".
//
// The semantic is NOT trusted on its own — it is catalog-validated for free by
// proposeOneEpisodeLibrary's existing SeasonDetails existence check, so an
// implausible parse ("e2516" -> S25E16 for a 20-season show) can only fail to
// find a candidate and fall through to Unmatched. That refusal IS the guarantee;
// do not "fix" it by loosening the season check.
func ParseCompactEpisodeCode(name string) (season, episode int, ok bool) {
	_, digits, matched := compactEpisodeCodeMatch(name)
	if !matched {
		return 0, 0, false
	}
	season, episode = digits/100, digits%100
	if season == 0 || episode == 0 {
		return 0, 0, false
	}
	return season, episode, true
}

// ParseCompactEpisodeCodeLoose is ParseCompactEpisodeCode's rename-path-only
// sibling: basename first, then parentDir's own basename, mirroring
// ParseEpisodeNumbersLoose's delegation shape exactly. Callers pass
// filepath.Dir(sourcePath) as parentDir.
func ParseCompactEpisodeCodeLoose(basename, parentDir string) (season, episode int, ok bool) {
	if season, episode, ok = ParseCompactEpisodeCode(basename); ok {
		return season, episode, ok
	}
	return ParseCompactEpisodeCode(filepath.Base(parentDir))
}

// StripCompactEpisodeCode removes a compactEpisodeCodePattern token (and
// nothing else) from name, leaving the show-title remainder. Returns name
// unchanged when no token is present, and unchanged when the first matched
// code's digits are blocklisted (it must never strip a resolution token).
//
// REMOVES ONLY THE `e\d{3,4}` SPAN, via submatch indices — it does NOT replace
// the whole match. The pattern's leading and trailing classes CONSUME a
// delimiter, so ReplaceAllString(name, "") on "RED SKELTON e1524.mp4" (whose
// match is " e1524.", space and dot INCLUDED) yields "RED SKELTONmp4" — a
// single fused token that shares only "red" with the real show title and
// therefore FAILS HasTitleTokenOverlap's `strong || shared >= 2` bar, silently
// breaking the very seed this function exists to repair. Replacing with a single
// space is also wrong: it yields "RED SKELTON mp4". Preserving the surrounding
// delimiters is the only correct behaviour.
//
// Consequence worth stating because it looks like an off-by-one and is not:
// "e614_720x480.mp4" yields "_720x480.mp4", with the LEADING UNDERSCORE
// PRESERVED. The leading class matches the zero-width `^` alternative there, so
// name[:loc[2]-1] is the empty string and the terminator '_' at loc[3] survives
// into the tail. Do NOT "fix" this to strip the underscore — consuming it would
// make the behaviour position-dependent (trimming a delimiter at string start
// but not mid-string), which is precisely the inconsistency submatch-index
// removal exists to avoid.
func StripCompactEpisodeCode(name string) string {
	loc, _, matched := compactEpisodeCodeMatch(name)
	if !matched {
		return name
	}
	return name[:loc[2]-1] + name[loc[3]:]
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

// Claude 2026-08-06: StripEpisodeMarker lockstep for the ParseEpisodeNumbersLoose fallback
// Reason: proposeOneEpisodeLibrary (internal/rename/rename.go) builds its
//   TMDB search term, and its GuessTitle/bravePhase2Series identity seed,
//   from StripEpisodeMarker(basename) — for the "The Path" hash-basename
//   shape that string carries zero title information, and no marker to
//   strip, so the plain strict call is a silent no-op that would carry the
//   opaque hash straight into searchterm.SearchQueries/GuessTitle, either
//   failing the TMDB search outright or (worse) letting an AI hallucinate a
//   title from the hash and land a confidently wrong match.
// Troubleshooting: season/episode now parses via the parent dir but the file
//   still ends up Unmatched with a search-failure reason, not Pending.
// Review if: StripEpisodeMarker's own regex block changes shape — this must
//   keep calling it unmodified, never duplicate its logic.

// StripEpisodeMarkerLoose is StripEpisodeMarker's rename-path-only sibling:
// it strips basename with the unmodified strict function first; only when
// that is a no-op (no marker found in basename, so the result is unchanged)
// does it fall back to stripping parentDir's own basename instead — the
// parent directory's own title, once its own episode marker is removed.
// Callers pass filepath.Dir(sourcePath) as parentDir, same as
// ParseEpisodeNumbersLoose. Pure delegation — StripEpisodeMarker itself is
// untouched, so every other caller is provably unaffected.
func StripEpisodeMarkerLoose(basename, parentDir string) string {
	if stripped := StripEpisodeMarker(basename); stripped != basename {
		return stripped
	}
	parentBase := filepath.Base(parentDir)
	if stripped := StripEpisodeMarker(parentBase); stripped != parentBase {
		return stripped
	}
	return basename
}

// ResolveEpisodeVideoFiles is ResolveVideoFile's season-pack-aware sibling:
// if path is an allowlisted video file, returns just that file; if it's a
// directory, returns EVERY allowlisted video file inside (not just the
// largest — a season pack legitimately contains many different-sized
// episodes). Non-video loose files return an error.
func ResolveEpisodeVideoFiles(path string) ([]string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("stat %s: %w", path, err)
	}
	if !info.IsDir() {
		if !config.IsVideoFile(path) {
			return nil, fmt.Errorf("not a video file: %s", path)
		}
		return []string{path}, nil
	}

	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() || !config.IsVideoExt(filepath.Ext(e.Name())) {
			continue
		}
		out = append(out, filepath.Join(path, e.Name()))
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no video files found under %s", path)
	}
	return out, nil
}
