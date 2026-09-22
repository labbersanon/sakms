package library

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/quality"
)

// Claude 2026-09-22: per-title quality prefs for unattended grabs.
// Reason: mode-level series_quality_tiers alone cannot stop SD fallbacks on one
//   show after HD STAT misses; monitor UI needs a persisted override.
// Troubleshooting: library_quality_prefs; drain resolves via TitleQualityPrefs.
// Review if: adult titles share this table (mode column already allows it).
// Related files: api/titlequality.go; migrations/0029_library_quality_prefs.sql

// TitleQualityPrefs is the stored override for one (mode, tmdb_id).
// HasOverride is false when no row exists — callers must inherit mode settings.
// MaxResolutionSet distinguishes "inherit" from an explicit 0 (no cap).
type TitleQualityPrefs struct {
	Mode             mode.Mode
	TMDBID           int
	Tiers            []quality.Tier
	MaxResolution    int
	MaxResolutionSet bool
	HasOverride      bool
}

// GetTitleQualityPrefs loads a title override. ErrNotFound when no row exists
// (not an error for callers that treat absence as inherit).
func (s *Store) GetTitleQualityPrefs(ctx context.Context, m mode.Mode, tmdbID int) (TitleQualityPrefs, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT quality_tiers, max_resolution
		FROM library_quality_prefs
		WHERE mode = ? AND tmdb_id = ?
	`, string(m), tmdbID)
	var tiersRaw string
	var maxRes sql.NullInt64
	if err := row.Scan(&tiersRaw, &maxRes); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return TitleQualityPrefs{}, ErrNotFound
		}
		return TitleQualityPrefs{}, fmt.Errorf("loading quality prefs for %s/%d: %w", m, tmdbID, err)
	}
	out := TitleQualityPrefs{
		Mode: m, TMDBID: tmdbID, HasOverride: true,
		Tiers: parseStoredTiers(tiersRaw),
	}
	if maxRes.Valid {
		out.MaxResolution = int(maxRes.Int64)
		out.MaxResolutionSet = true
	}
	return out, nil
}

// SetTitleQualityPrefs upserts a title override. tiers must be non-empty.
// maxResolutionSet false clears the column (inherit max res from mode).
func (s *Store) SetTitleQualityPrefs(ctx context.Context, m mode.Mode, tmdbID int, tiers []quality.Tier, maxResolution int, maxResolutionSet bool) error {
	if tmdbID <= 0 {
		return fmt.Errorf("library: tmdb id required for quality prefs")
	}
	if len(tiers) == 0 {
		return fmt.Errorf("library: quality tiers required for quality prefs")
	}
	raw, err := json.Marshal(tiersToStrings(tiers))
	if err != nil {
		return fmt.Errorf("encoding quality tiers: %w", err)
	}
	var maxArg any
	if maxResolutionSet {
		maxArg = maxResolution
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO library_quality_prefs (mode, tmdb_id, quality_tiers, max_resolution)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(mode, tmdb_id) DO UPDATE SET
			quality_tiers = excluded.quality_tiers,
			max_resolution = excluded.max_resolution,
			updated_at = sakms_now()
	`, string(m), tmdbID, string(raw), maxArg)
	if err != nil {
		return fmt.Errorf("upserting quality prefs for %s/%d: %w", m, tmdbID, err)
	}
	return nil
}

// DeleteTitleQualityPrefs removes a title override (back to mode defaults).
func (s *Store) DeleteTitleQualityPrefs(ctx context.Context, m mode.Mode, tmdbID int) error {
	_, err := s.db.ExecContext(ctx, `
		DELETE FROM library_quality_prefs WHERE mode = ? AND tmdb_id = ?
	`, string(m), tmdbID)
	if err != nil {
		return fmt.Errorf("deleting quality prefs for %s/%d: %w", m, tmdbID, err)
	}
	return nil
}

func parseStoredTiers(raw string) []quality.Tier {
	if raw == "" {
		return nil
	}
	var ss []string
	if err := json.Unmarshal([]byte(raw), &ss); err != nil {
		return nil
	}
	out := make([]quality.Tier, 0, len(ss))
	seen := map[quality.Tier]bool{}
	for _, s := range ss {
		t := quality.Tier(s)
		if quality.Rank(t) <= 0 || seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
	}
	return out
}

func tiersToStrings(ts []quality.Tier) []string {
	out := make([]string, len(ts))
	for i, t := range ts {
		out[i] = string(t)
	}
	return out
}
