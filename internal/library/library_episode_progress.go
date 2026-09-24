package library

import (
	"context"
	"fmt"
)

// EpisodeProgress is the last in-app play position for one library episode.
// Claude 2026-09-24: Series-only this pass; Movies/Adult stay untouched.
// Reason: Resume Show and episode bars need a stored row, not a client guess.
// Troubleshooting: without this table, Play Show cannot resume after reload.
// Review if: Movies/Adult join this table.
type EpisodeProgress struct {
	EpisodeID       int64   `json:"episodeId"`
	PositionSeconds float64 `json:"positionSeconds"`
	DurationSeconds float64 `json:"durationSeconds"`
	Watched         bool    `json:"watched"`
	UpdatedAt       string  `json:"updatedAt,omitempty"`
}

// UpsertEpisodeProgress writes one episode's play head. episodeID must exist
// (FK). Watched is stored as given — the API marks ~90% / ended.
func (s *Store) UpsertEpisodeProgress(ctx context.Context, p EpisodeProgress) (EpisodeProgress, error) {
	if p.EpisodeID < 1 {
		return EpisodeProgress{}, fmt.Errorf("library: UpsertEpisodeProgress requires episode_id")
	}
	if p.PositionSeconds < 0 {
		p.PositionSeconds = 0
	}
	if p.DurationSeconds < 0 {
		p.DurationSeconds = 0
	}
	row := s.db.QueryRowContext(ctx, `
		INSERT INTO library_episode_progress (
			episode_id, position_seconds, duration_seconds, watched
		) VALUES (?, ?, ?, ?)
		ON CONFLICT (episode_id) DO UPDATE SET
			position_seconds = excluded.position_seconds,
			duration_seconds = excluded.duration_seconds,
			watched = excluded.watched,
			updated_at = sakms_now()
		RETURNING updated_at
	`, p.EpisodeID, p.PositionSeconds, p.DurationSeconds, p.Watched)
	if err := row.Scan(&p.UpdatedAt); err != nil {
		return EpisodeProgress{}, fmt.Errorf("upserting episode progress %d: %w", p.EpisodeID, err)
	}
	return p, nil
}

// ListEpisodeProgressForSeries returns progress rows for every episode of
// seriesID that has been played, keyed by episode id.
func (s *Store) ListEpisodeProgressForSeries(ctx context.Context, seriesID int64) (map[int64]EpisodeProgress, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT p.episode_id, p.position_seconds, p.duration_seconds, p.watched, p.updated_at
		FROM library_episode_progress p
		INNER JOIN library_episodes e ON e.id = p.episode_id
		WHERE e.series_id = ?
	`, seriesID)
	if err != nil {
		return nil, fmt.Errorf("listing episode progress for series %d: %w", seriesID, err)
	}
	defer rows.Close()
	out := map[int64]EpisodeProgress{}
	for rows.Next() {
		var p EpisodeProgress
		if err := rows.Scan(&p.EpisodeID, &p.PositionSeconds, &p.DurationSeconds, &p.Watched, &p.UpdatedAt); err != nil {
			return nil, err
		}
		out[p.EpisodeID] = p
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
