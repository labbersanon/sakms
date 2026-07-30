package adultnewest

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

// SceneMatch is one confidently-matched Prowlarr RELEASE cached for the Adult
// "newest" drill-down's on-demand Show More (page>1) path — keyed by
// DownloadURL, a release's stable identity. It holds exactly the enriched
// display fields needed to reconstruct an AdultDiscoverItem's presentation
// without re-fetching + re-matching the box catalog on a repeated Show More.
// The grab-bearing fields (ReleaseTitle/DownloadURL/Protocol/SizeBytes) always
// come from the live raw Prowlarr result at request time, never from here.
//
// This is deliberately a separate table (adult_newest_scene_matches) from the
// entity pool (adult_newest_releases): the pool is keyed by ENTITY with its
// download_url locked to whichever release first discovered the entity, so it
// structurally cannot hold one row per matched release (see the 0050 migration
// doc comment).
type SceneMatch struct {
	DownloadURL     string
	Title           string
	Studio          string
	Image           string
	Date            string
	Genres          []string
	Performers      []string
	DurationSeconds int
	// Box is which box (stashdb|fansdb|tpdb) produced the match — recorded for
	// parity with the pool's entity_source and for diagnostics.
	Box       string
	MatchedAt string
}

// EntityBox returns the box (entity_source) that originally identified the
// studio/performer entity of rowType whose name is `name` — the single known
// box the drill-down's page>1 enrichment resolves against, instead of a 3-box
// fan-out. For studio/performer rows entity_id holds the entity's own name (see
// scan.go's identifyStudioPerformers), so the lookup keys on (row_type,
// entity_id). Returns "" (no error) when no pool row exists for this entity yet
// — the caller then has no known box and drops that drill's Prowlarr misses.
// Newest row wins if somehow more than one source recorded the same name.
func (s *ReleaseStore) EntityBox(ctx context.Context, rowType RowType, name string) (string, error) {
	var box string
	err := s.db.QueryRowContext(ctx, `
		SELECT entity_source FROM adult_newest_releases
		WHERE row_type = ? AND entity_id = ?
		ORDER BY first_seen_at DESC, id DESC
		LIMIT 1`, string(rowType), name).Scan(&box)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("looking up entity box for %s %q: %w", rowType, name, err)
	}
	return box, nil
}

// SceneMatchesByDownloadURLs returns the cached matches whose download_url is in
// urls, mapped by download_url — the batched cache lookup the drill-down's
// page>1 path runs before calling identify.EnrichNewestScenes, so already-matched
// releases skip the box catalog fetch entirely. Mirrors ByFeedItemKeys's shape
// (one IN query, not a lookup per url). A no-op for empty urls.
func (s *ReleaseStore) SceneMatchesByDownloadURLs(ctx context.Context, urls []string) (map[string]SceneMatch, error) {
	out := make(map[string]SceneMatch, len(urls))
	if len(urls) == 0 {
		return out, nil
	}
	placeholders := make([]byte, 0, len(urls)*2)
	args := make([]any, len(urls))
	for i, u := range urls {
		if i > 0 {
			placeholders = append(placeholders, ',')
		}
		placeholders = append(placeholders, '?')
		args[i] = u
	}
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`
		SELECT download_url, title, studio, image, date, genres, performers, duration_seconds, box, matched_at
		FROM adult_newest_scene_matches
		WHERE download_url IN (%s)`, placeholders), args...)
	if err != nil {
		return nil, fmt.Errorf("looking up cached scene matches: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			m                          SceneMatch
			genresJSON, performersJSON string
		)
		if err := rows.Scan(&m.DownloadURL, &m.Title, &m.Studio, &m.Image, &m.Date,
			&genresJSON, &performersJSON, &m.DurationSeconds, &m.Box, &m.MatchedAt); err != nil {
			return nil, fmt.Errorf("scanning cached scene match: %w", err)
		}
		if err := json.Unmarshal([]byte(genresJSON), &m.Genres); err != nil {
			return nil, fmt.Errorf("decoding genres for cached scene match %q: %w", m.DownloadURL, err)
		}
		if err := json.Unmarshal([]byte(performersJSON), &m.Performers); err != nil {
			return nil, fmt.Errorf("decoding performers for cached scene match %q: %w", m.DownloadURL, err)
		}
		out[m.DownloadURL] = m
	}
	return out, rows.Err()
}

// UpsertSceneMatch write-through caches one newly-matched release, keyed by
// download_url. ON CONFLICT it refreshes the enriched display fields (a later
// re-match with fresher box metadata wins) — the download_url identity is
// stable, so this only ever updates the same release's own row. genres/
// performers are JSON-encoded with the same "never store JSON null" convention
// as adult_newest_releases (a nil slice becomes "[]", not "null").
func (s *ReleaseStore) UpsertSceneMatch(ctx context.Context, m SceneMatch) error {
	genres := m.Genres
	if genres == nil {
		genres = []string{}
	}
	genresJSON, err := json.Marshal(genres)
	if err != nil {
		return fmt.Errorf("encoding genres for scene match %q: %w", m.DownloadURL, err)
	}
	performers := m.Performers
	if performers == nil {
		performers = []string{}
	}
	performersJSON, err := json.Marshal(performers)
	if err != nil {
		return fmt.Errorf("encoding performers for scene match %q: %w", m.DownloadURL, err)
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO adult_newest_scene_matches
			(download_url, title, studio, image, date, genres, performers, duration_seconds, box)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(download_url) DO UPDATE SET
			title            = excluded.title,
			studio           = excluded.studio,
			image            = excluded.image,
			date             = excluded.date,
			genres           = excluded.genres,
			performers       = excluded.performers,
			duration_seconds = excluded.duration_seconds,
			box              = excluded.box
	`, m.DownloadURL, m.Title, m.Studio, m.Image, m.Date, string(genresJSON), string(performersJSON),
		m.DurationSeconds, m.Box)
	if err != nil {
		return fmt.Errorf("upserting scene match %q: %w", m.DownloadURL, err)
	}
	return nil
}
