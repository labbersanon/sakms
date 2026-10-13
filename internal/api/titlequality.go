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
		writeTitleQualityPrefs(w, r.Context(), deps, mode.Series, series.TMDBID, effectiveTitleQualityPrefs(r.Context(), deps, mode.Series, series.TMDBID))
	}
}

func getSeriesQualityPrefsByTMDBHandler(deps titleQualityDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tmdbID, ok := tmdbIDPathValue(w, r)
		if !ok {
			return
		}
		writeTitleQualityPrefs(w, r.Context(), deps, mode.Series, tmdbID, effectiveTitleQualityPrefs(r.Context(), deps, mode.Series, tmdbID))
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
		out := effectiveTitleQualityPrefs(r.Context(), deps, mode.Movies, tmdbID)
		writeJSON(w, attachMovieUpgradeWatch(r.Context(), deps.lib, tmdbID, out))
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
		writeTitleQualityPrefs(w, ctx, deps, m, tmdbID, effectiveTitleQualityPrefs(ctx, deps, m, tmdbID))
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
	switch req.MaxResolution {
	case 0, 480, 720, 1080, 2160:
	default:
		http.Error(w, "maxResolution must be one of 480, 720, 1080, 2160, or 0 for no cap", http.StatusBadRequest)
		return
	}
	if req.MinResolution > 0 && req.MaxResolution > 0 && req.MinResolution > req.MaxResolution {
		http.Error(w, "minResolution must be <= maxResolution", http.StatusBadRequest)
		return
	}

	floors, err := parseResolutionFloors(req.ResolutionFloors)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if len(floors) == 0 {
		floors = autograb.UniformFloors(quality.Lowest(tiers))
	} else {
		merged := autograb.UniformFloors(quality.Lowest(tiers))
		for r, t := range floors {
			merged[r] = t
		}
		floors = merged
	}

	prefs := library.TitleQualityPrefs{
		Mode: m, TMDBID: tmdbID, Tiers: tiers,
		MinResolution: req.MinResolution, MinResolutionSet: true,
		MaxResolution: req.MaxResolution, MaxResolutionSet: true,
		ResolutionFloors: floors, CodecRank: req.CodecRank,
	}
	if req.FeatureUpgrade != nil {
		prefs.FeatureUpgrade = req.FeatureUpgrade
	}
	if err := deps.lib.SetTitleQualityPrefs(ctx, prefs); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	out := effectiveTitleQualityPrefs(ctx, deps, m, tmdbID)
	if qualityPrefsRaised(prev, out) {
		out.UpgradeQueued = queueQualityUpgrades(ctx, deps, m, tmdbID, series, out)
	}
	writeTitleQualityPrefs(w, ctx, deps, m, tmdbID, out)
}

func writeTitleQualityPrefs(w http.ResponseWriter, ctx context.Context, deps titleQualityDeps, m mode.Mode, tmdbID int, out apidto.TitleQualityPrefsResponse) {
	switch m {
	case mode.Movies:
		out = attachMovieUpgradeWatch(ctx, deps.lib, tmdbID, out)
	case mode.Series:
		out = attachSeriesUpgradeWatch(ctx, deps.lib, tmdbID, out)
	}
	writeJSON(w, out)
}

// titlePrefsTiersFromRequest prefers Floor (minimum tier, expanded upward);
// otherwise uses the explicit Tiers list, still normalized to floor-and-above.
func titlePrefsTiersFromRequest(req apidto.TitleQualityPrefsRequest) []quality.Tier {
	var floor, ceiling quality.Tier
	if req.Floor != "" {
		floor = quality.Tier(req.Floor)
		if quality.Rank(floor) <= 0 {
			return nil
		}
	} else {
		parsed := parseQualityTiers(req.Tiers)
		if len(parsed) == 0 {
			return nil
		}
		floor = quality.Lowest(parsed)
		ceiling = quality.Highest(parsed)
	}
	if req.Ceiling != "" {
		ceiling = quality.Tier(req.Ceiling)
		if quality.Rank(ceiling) <= 0 {
			return nil
		}
	}
	if quality.Rank(ceiling) <= 0 {
		return quality.TiersAtOrAbove(floor)
	}
	return quality.TiersBetween(floor, ceiling)
}

func effectiveTitleQualityPrefs(ctx context.Context, deps titleQualityDeps, m mode.Mode, tmdbID int) apidto.TitleQualityPrefsResponse {
	modeTiers := resolveQualityTiers(ctx, deps.settings, m)
	modeFloors := resolveResolutionFloors(ctx, deps.settings, m)

	stored, err := deps.lib.GetTitleQualityPrefs(ctx, m, tmdbID)
	modeMin, modeMax := resolveModeResolutionBand(ctx, deps.settings, m)
	modeRank := resolveCodecRank(ctx, deps.settings, m)
	modeFeat := resolveFeatureUpgrade(ctx, deps.settings, m)
	if err != nil || !stored.HasOverride {
		return titlePrefsResponse(modeTiers, modeMin, modeMax, true, modeFloors, modeRank, modeFeat)
	}
	tiers := stored.Tiers
	if len(tiers) == 0 {
		tiers = modeTiers
	}
	minRes := modeMin
	if stored.MinResolutionSet {
		minRes = stored.MinResolution
	}
	maxRes := modeMax
	if stored.MaxResolutionSet {
		maxRes = stored.MaxResolution
	}
	rank := modeRank
	if len(stored.CodecRank) > 0 {
		rank = stored.CodecRank
	}
	feat := modeFeat
	if stored.FeatureUpgrade != nil {
		feat = *stored.FeatureUpgrade
	}
	return titlePrefsResponse(tiers, minRes, maxRes, false, titleOverrideFloors(stored), rank, feat)
}

func titlePrefsResponse(tiers []quality.Tier, minRes, maxRes int, inherited bool, floors map[int]quality.Tier, codecRank []string, featureUpgrade bool) apidto.TitleQualityPrefsResponse {
	if len(floors) == 0 {
		floors = autograb.UniformFloors(quality.Lowest(tiers))
	}
	floor := quality.Lowest(tiers)
	var rungTiers []quality.Tier
	for _, r := range autograb.FloorRungs() {
		if t := floors[r]; quality.Rank(t) > 0 {
			rungTiers = append(rungTiers, t)
		}
	}
	if len(rungTiers) > 0 {
		floor = quality.Lowest(rungTiers)
	}
	return apidto.TitleQualityPrefsResponse{
		Tiers: qualityTiersToStrings(tiers), Floor: string(floor),
		Ceiling:       string(quality.Highest(tiers)),
		MinResolution: minRes, MaxResolution: maxRes, Inherited: inherited,
		ResolutionFloors: resolutionFloorsToStrings(floors),
		CodecRank:        quality.NormalizeCodecRank(codecRank), FeatureUpgrade: featureUpgrade,
	}
}

func titleOverrideFloors(stored library.TitleQualityPrefs) map[int]quality.Tier {
	out := autograb.UniformFloors(quality.Lowest(stored.Tiers))
	for r, t := range stored.ResolutionFloors {
		if quality.Rank(t) > 0 {
			out[r] = t
		}
	}
	return out
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

// Claude 2026-10-12: title override floors are per-rung, matching Settings.
// Reason: UniformFloors(Lowest(tiers)) made a Looney override Medium at 480p
//
//	and 1080p; the operator asked overrides to follow the same rules.
//
// Troubleshooting: library_quality_prefs.resolution_floors; TitleQualityPrefs
//
//	480p/1080p pills. Empty stored map (pre-0038 rows) still UniformFloors.
//
// Review if: Inherited GET stops copying mode floors onto the title card.
// Related files: migrations/0038_title_quality_resolution_floors.sql
//
// resolveAutoGrabFloorsForTitle is the per-rung bitrate floor map. A title
// override uses its stored per-resolution map (same rules as mode Settings);
// a pre-column override with empty floors still applies Lowest(tiers) to every
// rung. Inherited titles use the mode Settings map.
func resolveAutoGrabFloorsForTitle(ctx context.Context, lib *library.Store, settingsStore *settings.Store, m mode.Mode, tmdbID int) map[int]quality.Tier {
	if lib != nil && tmdbID > 0 {
		if stored, err := lib.GetTitleQualityPrefs(ctx, m, tmdbID); err == nil && stored.HasOverride {
			return titleOverrideFloors(stored)
		}
	}
	return resolveResolutionFloors(ctx, settingsStore, m)
}

// resolveAutoGrabAcceptedByResolution is the accepted set auto-grab walks
// highest-first, keyed by floorTable rung. Title overrides expand each rung's
// floor with TiersAtOrAbove, matching mode Settings.
func resolveAutoGrabAcceptedByResolution(ctx context.Context, lib *library.Store, settingsStore *settings.Store, m mode.Mode, tmdbID int) map[int][]quality.Tier {
	return autograb.AcceptedFromFloors(resolveAutoGrabFloorsForTitle(ctx, lib, settingsStore, m, tmdbID))
}

// resolveMinResolutionForTitle is the hard resolution floor (0 = any).
// Title override wins; otherwise the mode Settings min is used.
func resolveMinResolutionForTitle(ctx context.Context, lib *library.Store, settingsStore *settings.Store, m mode.Mode, tmdbID int) int {
	minRes, _ := resolveResolutionBandForTitle(ctx, lib, settingsStore, m, tmdbID)
	return minRes
}

func resolveResolutionBandForTitle(ctx context.Context, lib *library.Store, settingsStore *settings.Store, m mode.Mode, tmdbID int) (minRes, maxRes int) {
	minRes, maxRes = resolveModeResolutionBand(ctx, settingsStore, m)
	if lib == nil || tmdbID <= 0 {
		return minRes, maxRes
	}
	stored, err := lib.GetTitleQualityPrefs(ctx, m, tmdbID)
	if err != nil || !stored.HasOverride {
		return minRes, maxRes
	}
	if stored.MinResolutionSet {
		minRes = stored.MinResolution
	}
	if stored.MaxResolutionSet {
		maxRes = stored.MaxResolution
	}
	return minRes, maxRes
}

func resolveCodecRankForTitle(ctx context.Context, lib *library.Store, settingsStore *settings.Store, m mode.Mode, tmdbID int) []string {
	rank := resolveCodecRank(ctx, settingsStore, m)
	if lib != nil && tmdbID > 0 {
		if stored, err := lib.GetTitleQualityPrefs(ctx, m, tmdbID); err == nil && stored.HasOverride && len(stored.CodecRank) > 0 {
			return quality.NormalizeCodecRank(stored.CodecRank)
		}
	}
	return rank
}

func resolveFeatureUpgradeForTitle(ctx context.Context, lib *library.Store, settingsStore *settings.Store, m mode.Mode, tmdbID int) bool {
	feat := resolveFeatureUpgrade(ctx, settingsStore, m)
	if lib != nil && tmdbID > 0 {
		if stored, err := lib.GetTitleQualityPrefs(ctx, m, tmdbID); err == nil && stored.HasOverride && stored.FeatureUpgrade != nil {
			return *stored.FeatureUpgrade
		}
	}
	return feat
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
	if prev.MaxResolution > 0 && (next.MaxResolution == 0 || next.MaxResolution > prev.MaxResolution) {
		return true
	}
	if next.FeatureUpgrade && !prev.FeatureUpgrade {
		return true
	}
	for _, r := range autograb.FloorRungs() {
		k := strconv.Itoa(r)
		prevT := quality.Tier(prev.ResolutionFloors[k])
		nextT := quality.Tier(next.ResolutionFloors[k])
		if quality.Rank(nextT) > quality.Rank(prevT) && quality.Rank(nextT) > 0 {
			return true
		}
	}
	return false
}

func queueQualityUpgrades(ctx context.Context, deps titleQualityDeps, m mode.Mode, tmdbID int, series *library.Series, prefs apidto.TitleQualityPrefsResponse) int {
	if deps.grabs == nil || deps.lib == nil || tmdbID <= 0 {
		return 0
	}
	floors, _ := parseResolutionFloors(prefs.ResolutionFloors)
	if len(floors) == 0 {
		floors = autograb.UniformFloors(quality.Lowest(parseQualityTiers(prefs.Tiers)))
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
			if !fileNeedsQualityUpgrade(ep.FilePath, ep.QualityTier, fileQualityFloor(ep.FilePath, floors), minRes, prefs.FeatureUpgrade) {
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
		if item.FilePath == "" || !fileNeedsQualityUpgrade(item.FilePath, item.QualityTier, fileQualityFloor(item.FilePath, floors), minRes, prefs.FeatureUpgrade) {
			return 0
		}
		if parkQualityUpgrade(ctx, deps.grabs, m, item.Title, tmdbID, 0, 0) {
			queued = 1
		}
	}
	return queued
}

func fileQualityFloor(filePath string, floors map[int]quality.Tier) quality.Tier {
	info := release.Parse(filepath.Base(filePath))
	return autograb.FloorForResolution(floors, autograb.Candidate{
		Title: filepath.Base(filePath), Resolution: info.Resolution, Source: info.Source, Codec: info.Codec,
	})
}

func fileNeedsQualityUpgrade(filePath, stampedTier string, floor quality.Tier, minRes int, featureUpgrade bool) bool {
	info := release.Parse(filepath.Base(filePath))
	// Claude 2026-09-30: unknown parse resolution still fails a set minRes.
	// Reason: a 720p file named without a height token skipped the floor and
	//   never entered upgrade-watch. Treat unresolved height as below floor.
	// Troubleshooting: Movie.mkv with min 1080 never queued.
	// Review if: ffprobe height is used when the basename has no token.
	if minRes > 0 && (info.Resolution == 0 || info.Resolution < minRes) {
		return true
	}
	if inferred, ok := quality.InferTier(info); ok {
		if quality.Rank(inferred) < quality.Rank(floor) {
			return true
		}
	} else {
		stamped := quality.Tier(stampedTier)
		if quality.Rank(stamped) > 0 {
			if quality.Rank(stamped) < quality.Rank(floor) {
				return true
			}
		} else if quality.Rank(floor) > quality.Rank(quality.Default) {
			return true
		}
	}
	if featureUpgrade && (!info.HDR || info.Channels < release.Channels71) {
		return true
	}
	return false
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
	out, _ := requireResolutionBand(cands, minRes, 0)
	return out
}

func requireResolutionBand(cands []autograb.Candidate, minRes, maxRes int) ([]autograb.Candidate, []int) {
	if (minRes <= 0 && maxRes <= 0) || len(cands) == 0 {
		idx := make([]int, len(cands))
		for i := range cands {
			idx[i] = i
		}
		return cands, idx
	}
	var out []autograb.Candidate
	var keep []int
	for i, c := range cands {
		if minRes > 0 && c.Resolution > 0 && c.Resolution < minRes {
			continue
		}
		if maxRes > 0 && c.Resolution > maxRes {
			continue
		}
		out = append(out, c)
		keep = append(keep, i)
	}
	return out, keep
}
