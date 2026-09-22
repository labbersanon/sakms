package api

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"path/filepath"
	"strconv"
	"time"

	"github.com/labbersanon/sakms/internal/apidto"
	"github.com/labbersanon/sakms/internal/autograb"
	"github.com/labbersanon/sakms/internal/grabs"
	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/quality"
	"github.com/labbersanon/sakms/internal/release"
	"github.com/labbersanon/sakms/internal/settings"
)

// Claude 2026-09-22: per-title quality prefs for monitor/track surfaces.
// Reason: series drain + movie auto-grab must honor title overrides; raising
//   prefs queues upgrades for on-disk files below the new minimum floor.
// Troubleshooting: PUT .../quality-prefs; library_quality_prefs; upgradeQueued.
// Review if: mode Settings max_resolution also becomes a hard minimum.
// Related files: library/quality_prefs.go; SeasonsPanel TitleQualityPrefs.

const qualityUpgradeReason = "quality prefs raised — searching for a better release"

type titleQualityDeps struct {
	lib      *library.Store
	settings *settings.Store
	grabs    *grabs.Store
}

func getSeriesQualityPrefsByIDHandler(deps titleQualityDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		seriesID, ok := seriesIDPathValue(w, r)
		if !ok {
			return
		}
		series, ok := lookupSeries(r.Context(), w, deps.lib, seriesID)
		if !ok {
			return
		}
		writeJSON(w, effectiveTitleQualityPrefs(r.Context(), deps, mode.Series, series.TMDBID))
	}
}

func getSeriesQualityPrefsByTMDBHandler(deps titleQualityDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tmdbID, ok := tmdbIDPathValue(w, r)
		if !ok {
			return
		}
		writeJSON(w, effectiveTitleQualityPrefs(r.Context(), deps, mode.Series, tmdbID))
	}
}

func putSeriesQualityPrefsByIDHandler(deps titleQualityDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		seriesID, ok := seriesIDPathValue(w, r)
		if !ok {
			return
		}
		series, ok := lookupSeries(r.Context(), w, deps.lib, seriesID)
		if !ok {
			return
		}
		putTitleQualityPrefs(w, r, deps, mode.Series, series.TMDBID, series)
	}
}

func putSeriesQualityPrefsByTMDBHandler(deps titleQualityDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tmdbID, ok := tmdbIDPathValue(w, r)
		if !ok {
			return
		}
		catalog := seasonCatalog{lib: deps.lib, settings: deps.settings}
		series, err := catalog.ensureSeriesByTMDB(r.Context(), tmdbID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		putTitleQualityPrefs(w, r, deps, mode.Series, tmdbID, series)
	}
}

func getMovieQualityPrefsByTMDBHandler(deps titleQualityDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tmdbID, ok := tmdbIDPathValue(w, r)
		if !ok {
			return
		}
		writeJSON(w, effectiveTitleQualityPrefs(r.Context(), deps, mode.Movies, tmdbID))
	}
}

func putMovieQualityPrefsByTMDBHandler(deps titleQualityDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tmdbID, ok := tmdbIDPathValue(w, r)
		if !ok {
			return
		}
		putTitleQualityPrefs(w, r, deps, mode.Movies, tmdbID, nil)
	}
}

func putTitleQualityPrefs(w http.ResponseWriter, r *http.Request, deps titleQualityDeps, m mode.Mode, tmdbID int, series *library.Series) {
	var req apidto.TitleQualityPrefsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	prev := effectiveTitleQualityPrefs(ctx, deps, m, tmdbID)

	if req.Clear {
		if err := deps.lib.DeleteTitleQualityPrefs(ctx, m, tmdbID); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, effectiveTitleQualityPrefs(ctx, deps, m, tmdbID))
		return
	}

	tiers := titlePrefsTiersFromRequest(req)
	if len(tiers) == 0 {
		http.Error(w, "floor or tiers must include at least one of low, medium, high, lossless", http.StatusBadRequest)
		return
	}
	switch req.MinResolution {
	case 0, 480, 720, 1080, 2160:
	default:
		http.Error(w, "minResolution must be one of 480, 720, 1080, 2160, or 0 for any", http.StatusBadRequest)
		return
	}

	if err := deps.lib.SetTitleQualityPrefs(ctx, m, tmdbID, tiers, req.MinResolution, true); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	out := effectiveTitleQualityPrefs(ctx, deps, m, tmdbID)
	if qualityPrefsRaised(prev, out) {
		out.UpgradeQueued = queueQualityUpgrades(ctx, deps, m, tmdbID, series, out)
	}
	writeJSON(w, out)
}

// titlePrefsTiersFromRequest prefers Floor (minimum tier, expanded upward);
// otherwise uses the explicit Tiers list, still normalized to floor-and-above.
func titlePrefsTiersFromRequest(req apidto.TitleQualityPrefsRequest) []quality.Tier {
	if req.Floor != "" {
		floor := quality.Tier(req.Floor)
		if quality.Rank(floor) <= 0 {
			return nil
		}
		return quality.TiersAtOrAbove(floor)
	}
	parsed := parseQualityTiers(req.Tiers)
	if len(parsed) == 0 {
		return nil
	}
	return quality.TiersAtOrAbove(quality.Lowest(parsed))
}

func effectiveTitleQualityPrefs(ctx context.Context, deps titleQualityDeps, m mode.Mode, tmdbID int) apidto.TitleQualityPrefsResponse {
	modeTiers := resolveQualityTiers(ctx, deps.settings, m)

	stored, err := deps.lib.GetTitleQualityPrefs(ctx, m, tmdbID)
	if err != nil || !stored.HasOverride {
		return titlePrefsResponse(modeTiers, 0, true)
	}
	tiers := stored.Tiers
	if len(tiers) == 0 {
		tiers = modeTiers
	}
	minRes := 0
	if stored.MinResolutionSet {
		minRes = stored.MinResolution
	}
	return titlePrefsResponse(tiers, minRes, false)
}

func titlePrefsResponse(tiers []quality.Tier, minRes int, inherited bool) apidto.TitleQualityPrefsResponse {
	floor := quality.Lowest(tiers)
	return apidto.TitleQualityPrefsResponse{
		Tiers: qualityTiersToStrings(tiers), Floor: string(floor),
		MinResolution: minRes, Inherited: inherited,
	}
}

func resolveMaxResolution(ctx context.Context, settingsStore *settings.Store, m mode.Mode) int {
	raw, err := settingsStore.Get(ctx, maxResolutionKey(m))
	if err != nil || raw == "" {
		return 0
	}
	n, convErr := strconv.Atoi(raw)
	if convErr != nil || n < 0 {
		return 0
	}
	return n
}

// resolveAutoGrabTiersForTitle returns title override tiers when present,
// otherwise mode defaults. tmdbID 0 always uses mode defaults.
func resolveAutoGrabTiersForTitle(ctx context.Context, lib *library.Store, settingsStore *settings.Store, m mode.Mode, tmdbID int) []quality.Tier {
	if lib != nil && tmdbID > 0 {
		if stored, err := lib.GetTitleQualityPrefs(ctx, m, tmdbID); err == nil && stored.HasOverride && len(stored.Tiers) > 0 {
			return stored.Tiers
		}
	}
	return resolveQualityTiers(ctx, settingsStore, m)
}

// resolveMinResolutionForTitle returns the hard resolution floor for a title
// override (0 = any). Mode Settings soft-max is NOT inherited here — different
// semantics; unattended title grabs only enforce an explicit title minimum.
func resolveMinResolutionForTitle(ctx context.Context, lib *library.Store, m mode.Mode, tmdbID int) int {
	if lib != nil && tmdbID > 0 {
		if stored, err := lib.GetTitleQualityPrefs(ctx, m, tmdbID); err == nil && stored.HasOverride && stored.MinResolutionSet {
			return stored.MinResolution
		}
	}
	return 0
}

func qualityPrefsRaised(prev, next apidto.TitleQualityPrefsResponse) bool {
	prevTiers := parseQualityTiers(prev.Tiers)
	nextTiers := parseQualityTiers(next.Tiers)
	if len(nextTiers) == 0 {
		return false
	}
	if quality.Rank(quality.Lowest(nextTiers)) > quality.Rank(quality.Lowest(prevTiers)) {
		return true
	}
	if quality.Rank(quality.Highest(nextTiers)) > quality.Rank(quality.Highest(prevTiers)) {
		return true
	}
	// Raising the resolution floor (any → 1080, or 720 → 1080).
	if next.MinResolution > prev.MinResolution {
		return true
	}
	return false
}

func queueQualityUpgrades(ctx context.Context, deps titleQualityDeps, m mode.Mode, tmdbID int, series *library.Series, prefs apidto.TitleQualityPrefsResponse) int {
	if deps.grabs == nil || deps.lib == nil || tmdbID <= 0 {
		return 0
	}
	floor := quality.Lowest(parseQualityTiers(prefs.Tiers))
	if quality.Rank(floor) <= 0 {
		return 0
	}
	minRes := prefs.MinResolution
	queued := 0
	switch m {
	case mode.Series:
		if series == nil {
			s, err := deps.lib.GetSeriesByTMDBID(ctx, tmdbID)
			if err != nil {
				return 0
			}
			series = s
		}
		eps, err := deps.lib.ListEpisodes(ctx, series.ID)
		if err != nil {
			log.Printf("title quality: listing episodes for upgrade of %q: %v", series.Title, err)
			return 0
		}
		for _, ep := range eps {
			if ep.FilePath == "" {
				continue
			}
			if !fileNeedsQualityUpgrade(ep.FilePath, ep.QualityTier, floor, minRes) {
				continue
			}
			if parkQualityUpgrade(ctx, deps.grabs, m, series.Title, tmdbID, ep.SeasonNumber, ep.EpisodeNumber) {
				queued++
			}
		}
	case mode.Movies:
		item, err := deps.lib.GetByTMDBID(ctx, mode.Movies, tmdbID)
		if err != nil {
			if !errors.Is(err, library.ErrNotFound) {
				log.Printf("title quality: loading movie %d for upgrade: %v", tmdbID, err)
			}
			return 0
		}
		if item.FilePath == "" || !fileNeedsQualityUpgrade(item.FilePath, item.QualityTier, floor, minRes) {
			return 0
		}
		if parkQualityUpgrade(ctx, deps.grabs, m, item.Title, tmdbID, 0, 0) {
			queued = 1
		}
	}
	return queued
}

func fileNeedsQualityUpgrade(filePath, stampedTier string, floor quality.Tier, minRes int) bool {
	info := release.Parse(filepath.Base(filePath))
	if minRes > 0 && info.Resolution > 0 && info.Resolution < minRes {
		return true
	}
	if inferred, ok := quality.InferTier(info); ok {
		return quality.Rank(inferred) < quality.Rank(floor)
	}
	stamped := quality.Tier(stampedTier)
	if quality.Rank(stamped) > 0 {
		return quality.Rank(stamped) < quality.Rank(floor)
	}
	return quality.Rank(floor) > quality.Rank(quality.Default)
}

func parkQualityUpgrade(ctx context.Context, grabsStore *grabs.Store, m mode.Mode, title string, tmdbID, season, episode int) bool {
	list, err := grabsStore.List(ctx, m)
	if err != nil {
		return false
	}
	for _, g := range list {
		if g.TMDBID != tmdbID {
			continue
		}
		if m == mode.Series && (g.SeasonNumber != season || g.EpisodeNumber != episode) {
			continue
		}
		switch g.Status {
		case grabs.Queued, grabs.Downloading, grabs.Completed, grabs.PendingRetry:
			return false
		}
	}
	now := time.Now()
	g := grabs.Grab{
		Mode: m, Title: title, TMDBID: tmdbID,
		SeasonNumber: season, EpisodeNumber: episode,
		SeasonSpecified: m == mode.Series,
		Status:          grabs.PendingRetry,
		RetryAfter:      grabs.FormatTime(now),
		RetryReason:     qualityUpgradeReason,
	}
	if _, err := grabsStore.Create(ctx, g); err != nil {
		log.Printf("title quality: parking upgrade for %q: %v", title, err)
		return false
	}
	return true
}

// requireMinResolution hard-drops candidates below minRes. minRes <= 0 means
// any resolution. Unknown resolution (0) is kept — title parse may have failed;
// the bitrate floor still gates those. No fallback to lower-res releases.
func requireMinResolution(cands []autograb.Candidate, minRes int) []autograb.Candidate {
	if minRes <= 0 || len(cands) == 0 {
		return cands
	}
	var out []autograb.Candidate
	for _, c := range cands {
		if c.Resolution > 0 && c.Resolution < minRes {
			continue
		}
		out = append(out, c)
	}
	return out
}
