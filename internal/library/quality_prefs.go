package library

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

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
	Mode             mode.Mode
	TMDBID           int
	Tiers            []quality.Tier
	MinResolution    int
	MinResolutionSet bool
	MaxResolution    int
	MaxResolutionSet bool
	HasOverride      bool
	// Claude 2026-10-15: title/year snapshot for NNTP crawl-time admission
	// matching (migration 0043). Title=="" means not yet resolvable at PUT
	// time (e.g. a movie override set from Discover before TMDB lookup
	// succeeded) — api.NewNNTPAdmissionPolicy skips such rows, falling back
	// to mode defaults at crawl time, same as before this column existed.
	// Review if: a title PUT path is added that still can't resolve a title.
	Title string
	Year  int // 0 when unknown
	// ResolutionFloors is the per-rung bitrate floor (480/720/1080/2160).
	// Nil/empty means synthesize UniformFloors(Lowest(Tiers)) at the caller.
	ResolutionFloors map[int]quality.Tier
	// CodecRank leftover from when titles could override Settings order.
	// API PUT writes empty; grade time ignores stored values.
	CodecRank []string
	// FeatureUpgrade nil means inherit mode Settings.
	FeatureUpgrade *bool
}

// GetTitleQualityPrefs loads a title override. ErrNotFound when no row exists
// (not an error for callers that treat absence as inherit).
func (s *Store) GetTitleQualityPrefs(ctx context.Context, m mode.Mode, tmdbID int) (TitleQualityPrefs, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT quality_tiers, min_resolution, resolution_floors,
		       max_resolution, codec_rank, feature_upgrade, title, year
		FROM library_quality_prefs
		WHERE mode = ? AND tmdb_id = ?
	`, string(m), tmdbID)
	var tiersRaw string
	var minRes sql.NullInt64
	var floorsRaw string
	var maxRes sql.NullInt64
	var codecRankRaw string
	var featureUp sql.NullInt64
	var title string
	var year sql.NullInt64
	if err := row.Scan(&tiersRaw, &minRes, &floorsRaw, &maxRes, &codecRankRaw, &featureUp, &title, &year); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return TitleQualityPrefs{}, ErrNotFound
		}
		return TitleQualityPrefs{}, fmt.Errorf("loading quality prefs for %s/%d: %w", m, tmdbID, err)
	}
	out := TitleQualityPrefs{
		Mode: m, TMDBID: tmdbID, HasOverride: true,
		Tiers:            parseStoredTiers(tiersRaw),
		ResolutionFloors: parseStoredFloors(floorsRaw),
		CodecRank:        parseStoredCodecRank(codecRankRaw),
		Title:            title,
	}
	if minRes.Valid {
		out.MinResolution = int(minRes.Int64)
		out.MinResolutionSet = true
	}
	if maxRes.Valid {
		out.MaxResolution = int(maxRes.Int64)
		out.MaxResolutionSet = true
	}
	if featureUp.Valid {
		v := featureUp.Int64 != 0
		out.FeatureUpgrade = &v
	}
	if year.Valid {
		out.Year = int(year.Int64)
	}
	return out, nil
}

// ListTitleQualityPrefs returns every stored title override row — the
// NNTP crawl-admission policy builder's input set (api.
// NewNNTPAdmissionPolicy, Step 4 of the crawl-admission plan).
func (s *Store) ListTitleQualityPrefs(ctx context.Context) ([]TitleQualityPrefs, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT mode, tmdb_id, quality_tiers, min_resolution, resolution_floors,
		       max_resolution, codec_rank, feature_upgrade, title, year
		FROM library_quality_prefs
	`)
	if err != nil {
		return nil, fmt.Errorf("listing quality prefs: %w", err)
	}
	defer rows.Close()
	var out []TitleQualityPrefs
	for rows.Next() {
		var modeRaw, tiersRaw, floorsRaw, codecRankRaw, title string
		var tmdbID int
		var minRes, maxRes, year sql.NullInt64
		var featureUp sql.NullInt64
		if err := rows.Scan(&modeRaw, &tmdbID, &tiersRaw, &minRes, &floorsRaw, &maxRes, &codecRankRaw, &featureUp, &title, &year); err != nil {
			return nil, fmt.Errorf("scanning quality prefs: %w", err)
		}
		p := TitleQualityPrefs{
			Mode: mode.Mode(modeRaw), TMDBID: tmdbID, HasOverride: true,
			Tiers:            parseStoredTiers(tiersRaw),
			ResolutionFloors: parseStoredFloors(floorsRaw),
			CodecRank:        parseStoredCodecRank(codecRankRaw),
			Title:            title,
		}
		if minRes.Valid {
			p.MinResolution = int(minRes.Int64)
			p.MinResolutionSet = true
		}
		if maxRes.Valid {
			p.MaxResolution = int(maxRes.Int64)
			p.MaxResolutionSet = true
		}
		if featureUp.Valid {
			v := featureUp.Int64 != 0
			p.FeatureUpgrade = &v
		}
		if year.Valid {
			p.Year = int(year.Int64)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// SetTitleQualityPrefs upserts a title override. tiers must be non-empty.
// minResolutionSet false clears the column (inherit — treated as any/0 for
// title-min semantics when no override row exists).
// Claude 2026-10-12: floors is the per-rung bitrate map (same keys as mode
//
//	Settings). Empty/nil stores "" and GET synthesizes from Lowest(tiers).
//
// Reason: title override must grade 480p vs 1080p independently.
// Troubleshooting: library_quality_prefs.resolution_floors; TitleQualityPrefs pills.
func (s *Store) SetTitleQualityPrefs(ctx context.Context, prefs TitleQualityPrefs) error {
	if prefs.TMDBID <= 0 {
		return fmt.Errorf("library: tmdb id required for quality prefs")
	}
	if len(prefs.Tiers) == 0 {
		return fmt.Errorf("library: quality tiers required for quality prefs")
	}
	raw, err := json.Marshal(tiersToStrings(prefs.Tiers))
	if err != nil {
		return fmt.Errorf("encoding quality tiers: %w", err)
	}
	floorsRaw, err := floorsToJSON(prefs.ResolutionFloors)
	if err != nil {
		return fmt.Errorf("encoding resolution floors: %w", err)
	}
	codecRaw, err := codecRankToJSON(prefs.CodecRank)
	if err != nil {
		return fmt.Errorf("encoding codec rank: %w", err)
	}
	var minArg any
	if prefs.MinResolutionSet {
		minArg = prefs.MinResolution
	}
	var maxArg any
	if prefs.MaxResolutionSet {
		maxArg = prefs.MaxResolution
	}
	var featArg any
	if prefs.FeatureUpgrade != nil {
		if *prefs.FeatureUpgrade {
			featArg = 1
		} else {
			featArg = 0
		}
	}
	var yearArg any
	if prefs.Year > 0 {
		yearArg = prefs.Year
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO library_quality_prefs (
			mode, tmdb_id, quality_tiers, min_resolution, resolution_floors,
			max_resolution, codec_rank, feature_upgrade, title, year
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(mode, tmdb_id) DO UPDATE SET
			quality_tiers = excluded.quality_tiers,
			min_resolution = excluded.min_resolution,
			resolution_floors = excluded.resolution_floors,
			max_resolution = excluded.max_resolution,
			codec_rank = excluded.codec_rank,
			feature_upgrade = excluded.feature_upgrade,
			title = excluded.title,
			year = excluded.year,
			updated_at = sakms_now()
	`, string(prefs.Mode), prefs.TMDBID, string(raw), minArg, floorsRaw, maxArg, codecRaw, featArg, prefs.Title, yearArg)
	if err != nil {
		return fmt.Errorf("upserting quality prefs for %s/%d: %w", prefs.Mode, prefs.TMDBID, err)
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

func parseStoredFloors(raw string) map[int]quality.Tier {
	if raw == "" {
		return nil
	}
	var ss map[string]string
	if json.Unmarshal([]byte(raw), &ss) != nil || len(ss) == 0 {
		return nil
	}
	out := make(map[int]quality.Tier, len(ss))
	for k, v := range ss {
		res, err := strconv.Atoi(k)
		if err != nil {
			continue
		}
		switch res {
		case 480, 720, 1080, 2160:
		default:
			continue
		}
		t := quality.Tier(v)
		if quality.Rank(t) <= 0 {
			continue
		}
		out[res] = t
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func parseStoredCodecRank(raw string) []string {
	if raw == "" {
		return nil
	}
	var ss []string
	if json.Unmarshal([]byte(raw), &ss) != nil {
		return nil
	}
	return quality.NormalizeCodecRank(ss)
}

func codecRankToJSON(rank []string) (string, error) {
	if len(rank) == 0 {
		return "", nil
	}
	b, err := json.Marshal(quality.NormalizeCodecRank(rank))
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func floorsToJSON(floors map[int]quality.Tier) (string, error) {
	if len(floors) == 0 {
		return "", nil
	}
	ss := make(map[string]string, len(floors))
	for r, t := range floors {
		if quality.Rank(t) <= 0 {
			continue
		}
		ss[strconv.Itoa(r)] = string(t)
	}
	if len(ss) == 0 {
		return "", nil
	}
	b, err := json.Marshal(ss)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
