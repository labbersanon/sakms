package api

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strconv"

	"github.com/labbersanon/sakms/internal/adultnewest"
	"github.com/labbersanon/sakms/internal/apidto"
	"github.com/labbersanon/sakms/internal/autograb"
	"github.com/labbersanon/sakms/internal/connections"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/prowlarr"
	"github.com/labbersanon/sakms/internal/quality"
	"github.com/labbersanon/sakms/internal/serviceconn"
	"github.com/labbersanon/sakms/internal/settings"
)

// discoverAvailabilityResolutions is the fixed resolution axis the preview
// grid partitions candidates into (see apidto.AvailabilityPreview). A
// candidate whose parsed resolution falls outside this set (including
// release.Parse's zero value for "unrecognized") lands in NO bucket — the
// same neutral treatment autograb.GradeCandidate already gives an unknown
// resolution.
var discoverAvailabilityResolutions = []int{2160, 1080, 720, 480}

// discoverAvailabilityTiers is the fixed tier axis: every quality.Tier the
// popup's 4-way tier selector offers, each graded independently (never
// derived from another tier — see internal/autograb's package doc).
var discoverAvailabilityTiers = []quality.Tier{quality.Low, quality.Medium, quality.High, quality.Lossless}

// discoverAvailabilityHandler backs GET /api/modes/{mode}/discover/availability
// — the Discover detail popup's one upfront preview fetch. It runs a single,
// user-click-triggered Prowlarr search (the same trigger shape and cost as
// the existing manual Search screen — one query, once, on explicit action,
// NOT a reintroduction of the removed automatic per-card Discover probe; see
// CLAUDE.md's "Discover never queries Prowlarr" note), filters the results
// through releasematch's title/language pass, then grades the survivors 32
// ways (4 resolutions × 4 tiers × 2 protocols) via the SAME
// internal/autograb bitrate-quality-floor scorer auto-grab already uses —
// never a fresh classifier (see the plan's "Quality-tier evaluation" section).
//
// Reuses autoGrabSearch (autograb.go) almost entirely for the fetch step —
// it already resolves the correct per-mode Prowlarr query AND the pre-grab
// RuntimeSeconds the scorer needs (Movies: tmdb.MovieDetails; Series: the
// picked episode's runtime via seriesEpisodeRuntimeSeconds/SeasonDetails —
// NOT tmdb.TVDetails.Runtime, which is only the show's nominal per-episode
// duration, not the specific picked episode's; Adult: the request's
// DurationSeconds query param) — so this handler's only genuinely new logic
// is the release-match filter and the 32-bucket partition/grade.
//
// UNVERIFIED ASSUMPTION (flagged per this project's honesty-about-
// unverified-assumptions convention): the plan's query-param list for
// Movies/Series is tmdbId (+ season/episode for Series) only, but
// releasematch.FilterReleases' fast title-match path needs a known canonical
// title to compare each release title against, and every mode's Discover
// card already has that title client-side (it's what's rendered on the
// card). This handler therefore requires a `title` query param for every
// mode (Adult already needed one per the plan) rather than issuing a second
// TMDB call purely to recover a title the frontend already has in hand.
//
// releaseStore backs Adult's cache-first resolution; nil degrades to a live
// search, which is why non-Adult tests can pass nil.
func discoverAvailabilityHandler(httpClient *http.Client, connStore *connections.Store, scStore *serviceconn.Store, settingsStore *settings.Store, releaseStore *adultnewest.ReleaseStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		m := mode.Mode(r.PathValue("mode"))
		ctx := r.Context()
		q := r.URL.Query()

		title := q.Get("title")
		if title == "" {
			http.Error(w, "title is required", http.StatusBadRequest)
			return
		}

		sess, err := mode.Build(ctx, connStore, scStore, settingsStore, httpClient, nil, m)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if sess.Prowlarr == nil {
			http.Error(w, "prowlarr isn't configured yet — add it in Settings first", http.StatusBadRequest)
			return
		}
		// Movies/Series both resolve runtime through TMDB; Adult never does
		// (its runtime comes from the durationSeconds query param instead) —
		// same guard autoGrabHandler already applies before calling
		// autoGrabSearch.
		if m != mode.Adult && sess.TMDB == nil {
			http.Error(w, "tmdb isn't configured yet — add it in Settings first", http.StatusBadRequest)
			return
		}

		req := apidto.AutoGrabRequest{Title: title}
		switch m {
		case mode.Adult:
			// box/sceneId feed resolveAdultReleases' cache key; performers is a
			// SOFT signal only — it never hard-rejects a candidate here.
			req.Studio = q.Get("studio")
			req.ReleaseTitle = q.Get("releaseTitle")
			req.Performers = splitCommaTrim(q.Get("performers"))
			req.Box = q.Get("box")
			req.SceneID = q.Get("sceneId")
			req.DurationSeconds = queryInt(q, "durationSeconds", 0)
		case mode.Series:
			req.TMDBID = queryInt(q, "tmdbId", 0)
			req.SeasonNumber = queryInt(q, "season", 0)
			req.EpisodeNumber = queryInt(q, "episode", 0)
			// Claude 2026-08-03: set SeasonSpecified from whether the
			// `season` query key is present at all, not from SeasonNumber !=
			// 0.
			// Reason: SeasonEpisodePicker (frontend) only ever sends
			// `season`/`episode` together once an operator has actually
			// picked a season — a genuine Season-0/Specials pick would
			// otherwise be indistinguishable from "no season picked yet"
			// (see grabs.Grab.SeasonSpecified's doc for the same
			// zero-value ambiguity). Load-bearing for TWO things downstream:
			// autoGrabSearch's SearchByID call needs it to include the
			// {Season:N} brace token at all (see prowlarr.SearchByID), and
			// this handler's own FilterSeasonScope call below needs it to
			// know whether a season scope applies.
			// Troubleshooting: root-cause fix for "picking Season 4 grabs
			// S1E1" — see FilterSeasonScope's doc in releasematch.go.
			req.SeasonSpecified = q.Has("season")
		default: // Movies
			req.TMDBID = queryInt(q, "tmdbId", 0)
		}

		// Claude 2026-08-11: Adult resolves cache-first via resolveAdultReleases
		// with adultConsumerPicker; Movies/Series still go through autoGrabSearch.
		// Reason: "search once, persist, reuse" — DetailPopup must not re-query
		// Prowlarr on every re-open of the same scene. The picker consumer gets
		// the RAW list so FilterReleases below can still run its AI escalation
		// path over the full set.
		// Troubleshooting: empty grid despite a fresh cache → check HIT/MISS log
		// lines via o2cli; the grid filters via FilterReleases, not the resolver,
		// so a cache hit with 0 title-matching candidates is expected.
		// Review if: a non-Adult mode ever needs release persistence.
		var releases []prowlarr.Release
		var runtimeSeconds float64
		if m == mode.Adult {
			res, err := resolveAdultReleases(ctx, sess, releaseStore, adultConsumerPicker, req)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadGateway)
				return
			}
			releases = res.Releases
			runtimeSeconds = float64(req.DurationSeconds)
			log.Printf("discover availability: mode=adult title=%q servedFromCache=%v", title, res.FromCache)
		} else {
			var err error
			releases, runtimeSeconds, err = autoGrabSearch(ctx, sess, m, releaseStore, req)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadGateway)
				return
			}
		}
		log.Printf("discover availability: mode=%s title=%q tmdbId=%d — Prowlarr returned %d raw releases, runtimeSeconds=%.0f",
			m, title, req.TMDBID, len(releases), runtimeSeconds)

		// autoGrabSearch/seriesEpisodeRuntimeSeconds deliberately returns 0
		// runtime for a Series whole-season request (EpisodeNumber == 0) —
		// correct for auto-grab's safety posture (a single episode's runtime
		// can't validly grade a pack's total size, so "unknown" is the safe
		// call there). But for THIS endpoint, 0 runtime means every candidate
		// grades as unknown-bitrate and can never qualify at any tier — a
		// whole-season availability check would always show an empty grid,
		// a real bug (found via a "nothing is being found to grab" report).
		// Substituted here with the season's TOTAL runtime instead — a
		// legitimate, different computation for a genuinely different
		// purpose: a season pack's total file size genuinely does correspond
		// to the SUM of every episode's runtime, unlike one episode's
		// runtime alone. Deliberately does NOT touch autoGrabSearch/
		// seriesEpisodeRuntimeSeconds itself — auto-grab's existing
		// season-pack safety behavior is untouched.
		if m == mode.Series && req.EpisodeNumber == 0 {
			runtimeSeconds = seriesSeasonTotalRuntimeSeconds(ctx, sess, req.TMDBID, req.SeasonNumber)
		}

		filtered := FilterReleases(ctx, releases, title, m, sess.MainstreamAI)
		if m == mode.Series {
			// See FilterSeasonScope's doc (releasematch.go) — drops any
			// release whose title carries a recognizable, CONFLICTING season
			// marker (wrong season, or an episode marker excluding the
			// requested episode). A no-op when req.SeasonSpecified is
			// false (no season picked at all).
			filtered = FilterSeasonScope(filtered, req.SeasonNumber, req.EpisodeNumber, req.SeasonSpecified)
		}

		// A real per-episode runtime (Series single-episode grab) mustn't be
		// applied to season packs the indexer returned for the episode
		// query — same neutralization guard autoGrabHandler applies. Gated on
		// req.EpisodeNumber > 0 specifically (not just runtimeSeconds > 0):
		// the whole-season substitution above also produces a nonzero
		// runtimeSeconds, but for THAT case every candidate is meant to use
		// the season-total runtime uniformly, not get zeroed out for looking
		// like a pack — a whole-season query overwhelmingly returns packs,
		// that's the expected, useful result, not something to neutralize.
		neutralizeSeasonPacks := m == mode.Series && req.EpisodeNumber > 0 && runtimeSeconds > 0
		candidates := buildAutoGrabCandidates(filtered, runtimeSeconds, neutralizeSeasonPacks)

		unrecognizedResolution := 0
		unknownBitrate := 0
		for _, c := range candidates {
			if !isDiscoverAvailabilityResolution(c.Resolution) {
				unrecognizedResolution++
			}
			if c.RuntimeSeconds <= 0 || c.SizeBytes <= 0 {
				unknownBitrate++
			}
		}
		if len(candidates) > 0 && (unrecognizedResolution > 0 || unknownBitrate > 0) {
			log.Printf("discover availability: mode=%s title=%q — of %d filtered candidates, %d have an unrecognized resolution (land in no bucket) and %d have unknown bitrate (size or runtime missing, can never qualify at any tier)",
				m, title, len(candidates), unrecognizedResolution, unknownBitrate)
		}

		rejections := &rejectionSet{}
		preview := buildAvailabilityPreview(candidates, filtered, minSeedersFor(m), rejections)
		// A candidate whose resolution isn't one of the 4 buckets never
		// reaches any autograb.Select call at all, so it can't contribute a
		// grade — record it under the SAME status code Select would have used
		// (autograb.StatusUnknownResolution) so the set dedupes to one reason
		// rather than two phrasings of it.
		if unrecognizedResolution > 0 {
			rejections.add(autograb.StatusUnknownResolution)
		}
		preview.Diagnostics = apidto.AvailabilityDiagnostics{
			RawReleaseCount:     len(releases),
			MatchedReleaseCount: len(candidates),
			RejectionReasons:    rejections.sorted(),
		}
		log.Printf("discover availability: mode=%s title=%q — final grid has %d/32 populated cells", m, title, countPopulatedCells(preview))

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(preview)
	}
}

// isDiscoverAvailabilityResolution reports whether resolution is one of the
// 4 buckets the preview grid actually partitions on — anything else
// (including release.Parse's 0 "unrecognized" value) lands in no bucket at
// all, which reads as "unavailable" even though a real release was found.
// Diagnostic-logging helper only (see the handler's unrecognizedResolution
// counter above), not used by the actual grading path.
func isDiscoverAvailabilityResolution(resolution int) bool {
	for _, r := range discoverAvailabilityResolutions {
		if resolution == r {
			return true
		}
	}
	return false
}

// countPopulatedCells counts non-nil Usenet/Torrent picks across the whole
// 4x4x2 grid (max 32) — a quick, loggable summary of how empty or full the
// final response actually is, without dumping the whole structure.
func countPopulatedCells(preview apidto.AvailabilityPreview) int {
	n := 0
	for _, res := range []apidto.ResolutionAvailability{preview.Res2160, preview.Res1080, preview.Res720, preview.Res480} {
		for _, tier := range []apidto.TierAvailability{res.Low, res.Medium, res.High, res.Lossless} {
			if tier.Usenet != nil {
				n++
			}
			if tier.Torrent != nil {
				n++
			}
		}
	}
	return n
}

// seriesSeasonTotalRuntimeSeconds sums every episode's runtime for a whole
// season, for this endpoint's own whole-season runtime substitution (see the
// handler's doc comment above the call site) — never used by autoGrabSearch/
// seriesEpisodeRuntimeSeconds (autograb.go), which intentionally returns 0
// for a season-pack request instead, for a different reason (that function
// grades ONE release against ONE episode's runtime; this one is meant to
// grade a release against a WHOLE SEASON's runtime, a different and equally
// valid computation). A SeasonDetails failure or an empty season degrades to
// 0, same as seriesEpisodeRuntimeSeconds's own failure handling — the
// resulting candidates just grade as unknown-bitrate rather than erroring
// the whole request.
func seriesSeasonTotalRuntimeSeconds(ctx context.Context, sess *mode.Session, tmdbID, seasonNumber int) float64 {
	episodes, err := sess.TMDB.SeasonDetails(ctx, tmdbID, seasonNumber)
	if err != nil {
		return 0
	}
	var total float64
	for _, e := range episodes {
		total += float64(e.Runtime) * 60
	}
	return total
}

// queryInt parses q's named parameter as an int, returning def when absent
// or unparseable (never erroring the request over an optional numeric param).
func queryInt(q url.Values, key string, def int) int {
	v := q.Get(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

// buildAvailabilityPreview partitions candidates by resolution (any
// resolution outside discoverAvailabilityResolutions falls into NO bucket,
// the same neutral treatment autograb.GradeCandidate gives an unrecognized
// resolution) and grades every (resolution, tier, protocol) combination —
// the plan's literal 32-Select-calls grid. candidates and releases MUST be
// the same length and index-paired (see buildAutoGrabCandidates's existing
// convention, which this function's caller already produces). minSeeders is
// the per-mode floor (see minSeedersFor, autograb.go) — threaded all the way
// down to selectAvailabilityCandidate's autograb.Select call. rejections
// collects why cells came up empty (see rejectionSet) — nil-tolerant, so a
// caller that only wants the grid can pass nil.
func buildAvailabilityPreview(candidates []autograb.Candidate, releases []prowlarr.Release, minSeeders int, rejections *rejectionSet) apidto.AvailabilityPreview {
	return apidto.AvailabilityPreview{
		Res2160: buildResolutionAvailability(candidates, releases, 2160, minSeeders, rejections),
		Res1080: buildResolutionAvailability(candidates, releases, 1080, minSeeders, rejections),
		Res720:  buildResolutionAvailability(candidates, releases, 720, minSeeders, rejections),
		Res480:  buildResolutionAvailability(candidates, releases, 480, minSeeders, rejections),
	}
}

// rejectionSet accumulates the DISTINCT autograb.Status values seen across
// every (resolution, tier, protocol) cell that had candidates but qualified
// none — the same grade data logAvailabilityRejections already writes to the
// log, kept so the response can carry it too (see
// apidto.AvailabilityDiagnostics). Deliberately a set of status codes rather
// than a per-candidate list: the popup only needs to name WHY nothing
// qualified, and one release is graded up to 16 times across the grid.
type rejectionSet struct {
	seen map[string]struct{}
}

// add records one rejection status. A nil receiver is a no-op so callers that
// don't want diagnostics can pass nil.
func (r *rejectionSet) add(s autograb.Status) {
	if r == nil || s == "" {
		return
	}
	if r.seen == nil {
		r.seen = map[string]struct{}{}
	}
	r.seen[string(s)] = struct{}{}
}

// sorted returns the distinct statuses in a stable order (nil when none were
// recorded, which marshals the omitempty field away entirely).
func (r *rejectionSet) sorted() []string {
	if r == nil || len(r.seen) == 0 {
		return nil
	}
	out := make([]string, 0, len(r.seen))
	for s := range r.seen {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// buildResolutionAvailability filters candidates/releases (index-paired,
// see buildAvailabilityPreview) down to exactly resolution, then grades that
// subset against every tier.
func buildResolutionAvailability(candidates []autograb.Candidate, releases []prowlarr.Release, resolution, minSeeders int, rejections *rejectionSet) apidto.ResolutionAvailability {
	resCandidates, resReleases := partitionByResolution(candidates, releases, resolution)
	return apidto.ResolutionAvailability{
		Low:      buildTierAvailability(resCandidates, resReleases, quality.Low, minSeeders, rejections),
		Medium:   buildTierAvailability(resCandidates, resReleases, quality.Medium, minSeeders, rejections),
		High:     buildTierAvailability(resCandidates, resReleases, quality.High, minSeeders, rejections),
		Lossless: buildTierAvailability(resCandidates, resReleases, quality.Lossless, minSeeders, rejections),
	}
}

func partitionByResolution(candidates []autograb.Candidate, releases []prowlarr.Release, resolution int) ([]autograb.Candidate, []prowlarr.Release) {
	var outCandidates []autograb.Candidate
	var outReleases []prowlarr.Release
	for i, c := range candidates {
		if c.Resolution == resolution {
			outCandidates = append(outCandidates, c)
			outReleases = append(outReleases, releases[i])
		}
	}
	return outCandidates, outReleases
}

// buildTierAvailability grades one resolution bucket's candidates (already
// index-paired with releases) against tier, once per protocol.
func buildTierAvailability(candidates []autograb.Candidate, releases []prowlarr.Release, tier quality.Tier, minSeeders int, rejections *rejectionSet) apidto.TierAvailability {
	return apidto.TierAvailability{
		Usenet:  selectAvailabilityCandidate(candidates, releases, tier, string(prowlarr.Usenet), minSeeders, rejections),
		Torrent: selectAvailabilityCandidate(candidates, releases, tier, string(prowlarr.Torrent), minSeeders, rejections),
	}
}

// selectAvailabilityCandidate filters candidates/releases (index-paired) down
// to exactly protocol, then runs ONE autograb.Select over that filtered
// subset. CRITICAL: Select's PickIndex is relative to the slice passed to
// it, not to the original candidates/releases — so the winning release MUST
// be resolved from the SAME protocol-filtered subReleases slice, never by
// indexing back into the original candidates/releases with a subset-relative
// index (mirrors internal/api/autograb.go's existing
// buildAutoGrabCandidates/rankedAutoGrabCandidates index-pairing pattern,
// applied per-bucket here instead of once globally).
func selectAvailabilityCandidate(candidates []autograb.Candidate, releases []prowlarr.Release, tier quality.Tier, protocol string, minSeeders int, rejections *rejectionSet) *apidto.AvailabilityCandidate {
	var subCandidates []autograb.Candidate
	var subReleases []prowlarr.Release
	for i, c := range candidates {
		if c.Protocol == protocol {
			subCandidates = append(subCandidates, c)
			subReleases = append(subReleases, releases[i])
		}
	}
	if len(subCandidates) == 0 {
		return nil
	}

	sel := autograb.Select(subCandidates, tier, minSeeders)
	if sel.Fallback {
		logAvailabilityRejections(tier, protocol, subReleases, sel.Grades)
		for _, g := range sel.Grades {
			rejections.add(g.Status)
		}
		return nil
	}

	rel := subReleases[sel.PickIndex]
	grade := sel.Grades[sel.PickIndex]
	return &apidto.AvailabilityCandidate{
		GUID: rel.GUID, Title: rel.Title, Indexer: rel.Indexer, Protocol: string(rel.Protocol),
		Size: rel.Size, Seeders: rel.Seeders, DownloadURL: rel.DownloadURL, PublishDate: rel.PublishDate,
		Score: grade.Score,
	}
}

// logAvailabilityRejections explains why a (tier, protocol) cell had
// candidates but none qualified — diagnostic logging added 2026-07-15 after
// a real report where a scene found exactly 1 raw release from Prowlarr,
// with a recognized resolution and known bitrate/runtime, yet the popup's
// grid still showed 0/32 populated cells: autograb.Select was rejecting a
// real candidate for a reason this handler previously never surfaced (below
// the bitrate floor? too few seeders? mislabeled?). Logs each grade's Status
// plus the numbers that drove it, so a miscalibrated threshold can be
// confirmed from evidence rather than guessed at. Only called when
// candidates existed but none qualified (sel.Fallback), so this never fires
// for the overwhelmingly common "no release at all" case already covered by
// the handler's own "Prowlarr returned N raw releases" log line.
func logAvailabilityRejections(tier quality.Tier, protocol string, releases []prowlarr.Release, grades []autograb.Grade) {
	for i, g := range grades {
		title := ""
		if i < len(releases) {
			title = releases[i].Title
		}
		log.Printf("discover availability: rejected candidate tier=%s protocol=%s title=%q status=%s score=%.2fMbps floor=%.2fMbps impliedMbps=%.2f seeders=%d resolution=%d",
			tier, protocol, title, g.Status, g.Score, g.FloorMbps, g.ImpliedMbps, g.Candidate.Seeders, g.Candidate.Resolution)
	}
}
