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
//
// Claude 2026-09-22: min_resolution is a hard floor (0 = any), not a soft max.
// Reason: operator "1080p" means reject 480/720; soft-prefer-below was wrong.
// Troubleshooting: requireMinResolution; migration 0030 renamed the column.
// Review if: mode Settings max_resolution flips to the same minimum semantics.

// TitleQualityPrefs is the stored override for one (mode, tmdb_id).
// HasOverride is false when no row exists — callers must inherit mode settings.
// MinResolutionSet distinguishes "inherit" from an explicit 0 (any resolution).
type TitleQualityPrefs struct {
	Mode            mode.Mode
	TMDBID          int
	Tiers           []quality.Tier
	MinResolution   int
	MinResolutionSet bool
	HasOverride     bool
}

// GetTitleQualityPrefs loads a title override. ErrNotFound when no row exists
// (not an error for callers that treat absence as inherit).
func (s *Store) GetTitleQualityPrefs(ctx context.Context, m mode.Mode, tmdbID int) (TitleQualityPrefs, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT quality_tiers, min_resolution
		FROM library_quality_prefs
		WHERE mode = ? AND tmdb_id = ?
	`, string(m), tmdbID)
	var tiersRaw string
	var minRes sql.NullInt64
	if err := row.Scan(&tiersRaw, &minRes); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return TitleQualityPrefs{}, ErrNotFound
		}
		return TitleQualityPrefs{}, fmt.Errorf("loading quality prefs for %s/%d: %w", m, tmdbID, err)
	}
	out := TitleQualityPrefs{
		Mode: m, TMDBID: tmdbID, HasOverride: true,
		Tiers: parseStoredTiers(tiersRaw),
	}
	if minRes.Valid {
		out.MinResolution = int(minRes.Int64)
		out.MinResolutionSet = true
	}
	return out, nil
}

// SetTitleQualityPrefs upserts a title override. tiers must be non-empty.
// minResolutionSet false clears the column (inherit — treated as any/0 for
// title-min semantics when no override row exists).
func (s *Store) SetTitleQualityPrefs(ctx context.Context, m mode.Mode, tmdbID int, tiers []quality.Tier, minResolution int, minResolutionSet bool) error {
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
	var minArg any
	if minResolutionSet {
		minArg = minResolution
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO library_quality_prefs (mode, tmdb_id, quality_tiers, min_resolution)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(mode, tmdb_id) DO UPDATE SET
			quality_tiers = excluded.quality_tiers,
			min_resolution = excluded.min_resolution,
			updated_at = sakms_now()
	`, string(m), tmdbID, string(raw), minArg)
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
