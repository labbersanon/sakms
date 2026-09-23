package library

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/labbersanon/sakms/internal/mode"
)

// PosterSource values written to poster_source. Empty means unset.
const (
	PosterSourceTMDB = "tmdb"
	PosterSourceTVDB = "tvdb"
	PosterSourceAI   = "ai"
)

// PosterArt is the cached absolute poster URL (and lookup helpers) for one
// tracked Movies/Series row. URL is https; Source is PosterSource*.
// Claude 2026-09-22: dedicated read/write so List/Get scanItem stay unchanged.
// Reason: poster fallback chain persists on tracked rows without rewriting
//   every SELECT that feeds scanItem/scanSeries.
// Troubleshooting: /poster re-resolving every card on every Library load.
// Review if: poster columns are folded into Item/Series and List SELECTs.
type PosterArt struct {
	RowID  int64
	TMDBID int
	TVDBID int // series only; 0 on movies
	Title  string
	Year   int
	URL    string
	Source string
}

// MoviePosterArt loads poster cache + title/year for one library_items row.
// ErrNotFound when no row exists for (mode, tmdbID).
func (s *Store) MoviePosterArt(ctx context.Context, m mode.Mode, tmdbID int) (PosterArt, error) {
	var art PosterArt
	err := s.db.QueryRowContext(ctx, `
		SELECT id, tmdb_id, title, year, poster_url, poster_source
		FROM library_items
		WHERE mode = ? AND tmdb_id = ?
	`, string(m), tmdbID).Scan(
		&art.RowID, &art.TMDBID, &art.Title, &art.Year, &art.URL, &art.Source,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return PosterArt{}, ErrNotFound
	}
	if err != nil {
		return PosterArt{}, fmt.Errorf("loading movie poster art for tmdb %d: %w", tmdbID, err)
	}
	return art, nil
}

// SeriesPosterArt loads poster cache + title/year/tvdb for one library_series row.
func (s *Store) SeriesPosterArt(ctx context.Context, tmdbID int) (PosterArt, error) {
	var art PosterArt
	err := s.db.QueryRowContext(ctx, `
		SELECT id, tmdb_id, tvdb_id, title, year, poster_url, poster_source
		FROM library_series
		WHERE tmdb_id = ?
	`, tmdbID).Scan(
		&art.RowID, &art.TMDBID, &art.TVDBID, &art.Title, &art.Year, &art.URL, &art.Source,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return PosterArt{}, ErrNotFound
	}
	if err != nil {
		return PosterArt{}, fmt.Errorf("loading series poster art for tmdb %d: %w", tmdbID, err)
	}
	return art, nil
}

// SetMoviePosterArt writes poster_url/source on library_items by (mode, tmdbID).
// Does not clear an existing URL when url is empty.
func (s *Store) SetMoviePosterArt(ctx context.Context, m mode.Mode, tmdbID int, url, source string) error {
	url = strings.TrimSpace(url)
	source = strings.TrimSpace(source)
	if url == "" {
		return nil
	}
	res, err := s.db.ExecContext(ctx, `
		UPDATE library_items
		SET poster_url = ?, poster_source = ?, updated_at = sakms_now()
		WHERE mode = ? AND tmdb_id = ?
	`, url, source, string(m), tmdbID)
	if err != nil {
		return fmt.Errorf("setting movie poster art for tmdb %d: %w", tmdbID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("setting movie poster art for tmdb %d: %w", tmdbID, err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetSeriesPosterArt writes poster_url/source on library_series by tmdb_id.
func (s *Store) SetSeriesPosterArt(ctx context.Context, tmdbID int, url, source string) error {
	url = strings.TrimSpace(url)
	source = strings.TrimSpace(source)
	if url == "" {
		return nil
	}
	res, err := s.db.ExecContext(ctx, `
		UPDATE library_series
		SET poster_url = ?, poster_source = ?, updated_at = sakms_now()
		WHERE tmdb_id = ?
	`, url, source, tmdbID)
	if err != nil {
		return fmt.Errorf("setting series poster art for tmdb %d: %w", tmdbID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("setting series poster art for tmdb %d: %w", tmdbID, err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// MoviePosterURLMap returns tmdb_id → poster_url for movies with cached art.
func (s *Store) MoviePosterURLMap(ctx context.Context, m mode.Mode) (map[int]string, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT tmdb_id, poster_url FROM library_items
		WHERE mode = ? AND poster_url <> ''
	`, string(m))
	if err != nil {
		return nil, fmt.Errorf("listing movie poster urls: %w", err)
	}
	defer rows.Close()
	out := map[int]string{}
	for rows.Next() {
		var id int
		var url string
		if err := rows.Scan(&id, &url); err != nil {
			return nil, fmt.Errorf("scanning movie poster url: %w", err)
		}
		out[id] = url
	}
	return out, rows.Err()
}

// SeriesPosterURLMap returns tmdb_id → poster_url for series with cached art.
func (s *Store) SeriesPosterURLMap(ctx context.Context) (map[int]string, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT tmdb_id, poster_url FROM library_series
		WHERE poster_url <> ''
	`)
	if err != nil {
		return nil, fmt.Errorf("listing series poster urls: %w", err)
	}
	defer rows.Close()
	out := map[int]string{}
	for rows.Next() {
		var id int
		var url string
		if err := rows.Scan(&id, &url); err != nil {
			return nil, fmt.Errorf("scanning series poster url: %w", err)
		}
		out[id] = url
	}
	return out, rows.Err()
}
