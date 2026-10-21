package usenetsearch

import (
	"context"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/labbersanon/sakms/internal/autograb"
	"github.com/labbersanon/sakms/internal/quality"
	"github.com/labbersanon/sakms/internal/release"
	"github.com/labbersanon/sakms/internal/searchterm"
	"github.com/labbersanon/sakms/internal/usenet"
)

// Claude 2026-10-15: NNTP crawl-time quality admission — pure core.
// Reason: approved plan .omc/plans/nntp-crawl-quality-admission.md. Everything
//   in this file is a pure function/type with no DB, no NNTP I/O, and no
//   knowledge of Service/Crawler state — crawler.go (Step 3) owns the wiring,
//   api.NewNNTPAdmissionPolicy (Step 4) owns building a *PolicySnapshot from
//   Settings + library_quality_prefs.
// Troubleshooting: admission_test.go table-tests every branch below.
// Review if: a non-title-token signal (bitrate, mislabel) needs to gate at
//   crawl time — those stay search/autograb-time per the plan's Context.
// Related files: usenetsearch/crawler.go, api/usenetadmission.go (Step 4)

// Claude 2026-10-15: admission core imports beyond internal/usenet.
// Reason: doc.go's "stdlib + internal/usenet only" rule predates this plan;
//   title-token quality gating needs release.Parse / quality.InferTier /
//   autograb.SnapFloorResolution / searchterm.FromName+StripYear. None of
//   these (nor internal/config, searchterm's own dependency) import
//   internal/api, internal/grabs, internal/prowlarr, or internal/mode, so
//   the cycle/layering doc.go actually guards against does not reappear.
// Troubleshooting: doc.go's import-discipline paragraph lists these four
//   packages explicitly (Step 5 commit).
// Review if: a future addition to any of those four packages adds an
//   internal/api (etc.) dependency — re-check for cycles before merging.

// statSamplePerRelease is how many msgids a sampled STAT checks per release.
// Claude 2026-10-15: see plan "Sampled STAT" section for the full rationale.
// Review if: provider STAT cost measured in production shows 3 is too many/few.
const statSamplePerRelease = 3

// statSkipMissing is the missing-of-sample count that skips (does not index)
// an otherwise quality-matching release.
// Claude 2026-10-15: 2-of-3 chosen over 1-of-3 because a skip is irreversible
// (the watermark advances past it) and STAT alone is known to lie on some
// providers; a real DMCA takedown removes the whole release, so 2-of-3
// still catches it while tolerating one provider's false 430.
// Review if: a false-skip is confirmed in `admit-skip-stat` logs plus a later
// successful grab of the same release from a different provider.
const statSkipMissing = 2

// Standard is one quality bar to check a parsed release against — either a
// specific title override (Policy=="title") or the mode-default / loosest-of
// fallback (Policy=="mode"). Match records this Standard's own title-match
// outcome so the admit log line can report it without a second lookup.
type Standard struct {
	Mode   string // "movies" | "series" | "adult"
	Policy string // "title" | "mode"
	TMDBID int    // 0 when Policy=="mode"
	// Match is "exact-year" | "exact" | "none" | "ambiguous" — see the plan's
	// "Match rule" section. "none"/"ambiguous" only ever appear on a
	// Policy=="mode" Standard (the fallback), never on Policy=="title".
	Match  string
	Tiers  []quality.Tier
	MinRes int
	MaxRes int
}

// TitleStd is one library_quality_prefs override row, pre-resolved into the
// same Tiers/MinRes/MaxRes shape a Standard uses. Built by
// api.NewNNTPAdmissionPolicy (Step 4) from resolveAutoGrabTiersForTitle /
// resolveResolutionBandForTitle plus the row's own Title/Year columns.
type TitleStd struct {
	Mode   string
	TMDBID int
	Title  string
	Year   int // 0 when unknown (e.g. a series title with no air-date year)
	Tiers  []quality.Tier
	MinRes int
	MaxRes int
}

// PolicySnapshot is the whole admission policy at one point in time: mode
// defaults plus every title override, keyed by normalizeTitleKey(Title) so
// Resolve never has to scan every override for every release.
type PolicySnapshot struct {
	// Modes holds the mode-default Standard for "movies"/"series"/"adult".
	Modes map[string]Standard
	// Titles maps a normalized title key to every override row that shares
	// that key — usually one, but US/UK series remakes land several under
	// the same key (disambiguated by Year, see resolveForMode).
	Titles map[string][]TitleStd
}

// AdmissionPolicy supplies a PolicySnapshot, refreshed on a timer and
// invalidated immediately on a title/mode quality-prefs PUT or DELETE (see
// the plan's Settings/config knobs table). A nil AdmissionPolicy, or a
// Config with AdmissionEnabled==false, means the legacy index-everything
// crawl path (Step 3) — admission.go itself has no "disabled" concept.
type AdmissionPolicy interface {
	Snapshot(ctx context.Context) (*PolicySnapshot, error)
}

// MatchInfo is the release-name classification shared by every Standard
// Resolve returns for one release — the normalized key/year/episode
// detection that decided which mode(s) were even candidates, independent of
// whether a title override happened to match any one of them.
type MatchInfo struct {
	Key       string
	Year      int // 0 when the release carries no year token
	IsEpisode bool
	// CandModes is the mode(s) actually tried, in the order Standards (the
	// Resolve return value) line up with — CandModes[i] is Standards[i].Mode.
	CandModes []string
}

var (
	// Claude 2026-10-15: episode-marker detection for splitReleaseTitle's
	// series/movie classification. Checked in this order (SxxExx firstmost)
	// because a release carrying SxxExx should never fall through to the
	// bare Sxx check finding a coincidental match deeper in the string.
	epSxxExxRe = regexp.MustCompile(`(?i)\bS\d{1,2}E\d{1,3}\b`)
	epSeasonRe = regexp.MustCompile(`(?i)\bSeason[.\s_]?\d{1,3}\b`)
	epNxNRe    = regexp.MustCompile(`(?i)\b\d{1,2}x\d{2}\b`)
	epSxxRe    = regexp.MustCompile(`(?i)\bS\d{1,2}\b`)
	yearRe     = regexp.MustCompile(`\b(?:19|20)\d{2}\b`)
)

// splitReleaseTitle cuts name at the earliest of an episode marker or a
// year token (whichever appears first in the string — see the plan's
// "Doctor.Who.2005.S01E01" example, where the year is positionally first and
// so is captured even though the release is a series). isEpisode is decided
// independently by scanning the WHOLE name for any episode marker, since a
// year appearing earlier must not suppress that classification.
func splitReleaseTitle(name string) (prefix string, year int, isEpisode bool) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", 0, false
	}

	isEpisode = epSxxExxRe.MatchString(name) || epSeasonRe.MatchString(name) ||
		epNxNRe.MatchString(name) || epSxxRe.MatchString(name)

	cutIdx := len(name)
	yearIsCut := false
	consider := func(loc []int, fromYear bool) {
		if loc == nil {
			return
		}
		if loc[0] < cutIdx {
			cutIdx = loc[0]
			yearIsCut = fromYear
		}
	}
	consider(epSxxExxRe.FindStringIndex(name), false)
	consider(epSeasonRe.FindStringIndex(name), false)
	consider(epNxNRe.FindStringIndex(name), false)
	consider(epSxxRe.FindStringIndex(name), false)
	consider(yearRe.FindStringIndex(name), true)

	prefix = strings.TrimSpace(name[:cutIdx])
	if yearIsCut {
		year, _ = strconv.Atoi(yearRe.FindString(name[cutIdx:]))
	}
	return prefix, year, isEpisode
}

var titleTokenRe = regexp.MustCompile(`[a-z0-9]+`)

// normalizeTitleKey lowercases, turns "&" into "and", drops a leading "the",
// and keeps only [a-z0-9] tokens — the same normalization on both the
// release-prefix side and the library-title side so the match is exact-key
// equality only (no substring/ILIKE/fuzzy, per the plan's Match rule).
func normalizeTitleKey(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.ReplaceAll(s, "&", " and ")
	tokens := titleTokenRe.FindAllString(s, -1)
	if len(tokens) > 1 && tokens[0] == "the" {
		tokens = tokens[1:]
	}
	return strings.Join(tokens, " ")
}

// NormalizeTitleKey exposes normalizeTitleKey for api.NewNNTPAdmissionPolicy
// (Step 4) to key PolicySnapshot.Titles by a library_quality_prefs row's own
// Title column, matching cleanTitleKey's release-prefix-side normalization
// so Resolve's exact-key lookup lines up (see the comment on PolicySnapshot).
func NormalizeTitleKey(title string) string {
	return normalizeTitleKey(title)
}

// cleanTitleKey runs the release-side prefix through the same cleanup
// FromName/StripYear already give orphan search terms, then normalizes.
func cleanTitleKey(prefix string) string {
	cleaned := searchterm.StripYear(searchterm.FromName(prefix))
	return normalizeTitleKey(cleaned)
}

// classifyMode decides which mode(s) a release is a candidate for: an
// episode marker means series; otherwise a group that belongs to adult mode
// means adult; otherwise movies. The result is intersected with groupModes
// (the modes this newsgroup is actually crawled under); an empty
// intersection (e.g. an episode-marked release in a movies-only group) falls
// back to trying every groupModes entry rather than silently admitting
// nothing — see the plan's "shared groups" risk mitigation.
func classifyMode(isEpisode bool, groupModes []string) []string {
	hasAdult := false
	for _, m := range groupModes {
		if m == "adult" {
			hasAdult = true
			break
		}
	}
	var classified string
	switch {
	case isEpisode:
		classified = "series"
	case hasAdult:
		classified = "adult"
	default:
		classified = "movies"
	}
	var cand []string
	for _, m := range groupModes {
		if m == classified {
			cand = append(cand, m)
		}
	}
	if len(cand) == 0 {
		cand = append([]string{}, groupModes...)
	}
	return cand
}

func absInt(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

func stdFromTitle(mode, match string, e TitleStd) Standard {
	return Standard{Mode: mode, Policy: "title", TMDBID: e.TMDBID, Match: match, Tiers: e.Tiers, MinRes: e.MinRes, MaxRes: e.MaxRes}
}

// resolveForMode applies the plan's per-mode Match rule. matched==false
// means "fall back to loosest-of"; label is "none" (no key match at all) or
// "ambiguous" (key matched but year/count couldn't pick exactly one) and is
// only meaningful when matched==false.
func (p *PolicySnapshot) resolveForMode(mode, key string, year int) (std Standard, matched bool, label string) {
	if mode == "adult" {
		return Standard{}, false, "none" // adult has no title override rows (locked decision)
	}
	var entries []TitleStd
	for _, e := range p.Titles[key] {
		if e.Mode == mode {
			entries = append(entries, e)
		}
	}
	if len(entries) == 0 {
		return Standard{}, false, "none"
	}
	switch mode {
	case "movies":
		// Claude 2026-10-15: a movie with no year on the release never
		// matches, even when the key matches — "Dune" (1984) vs "Dune"
		// (2021) bleed is exactly the case this blocks (plan Match rule).
		if year == 0 {
			return Standard{}, false, "ambiguous"
		}
		var hit []TitleStd
		for _, e := range entries {
			if e.Year != 0 && absInt(e.Year-year) <= 1 {
				hit = append(hit, e)
			}
		}
		if len(hit) == 1 {
			return stdFromTitle(mode, "exact-year", hit[0]), true, ""
		}
		return Standard{}, false, "ambiguous"
	case "series":
		if len(entries) == 1 {
			return stdFromTitle(mode, "exact", entries[0]), true, ""
		}
		if year != 0 {
			var hit []TitleStd
			for _, e := range entries {
				if e.Year == year {
					hit = append(hit, e)
				}
			}
			if len(hit) == 1 {
				return stdFromTitle(mode, "exact-year", hit[0]), true, ""
			}
		}
		return Standard{}, false, "ambiguous"
	default:
		return Standard{}, false, "none"
	}
}

// loosestOf merges mode's default Standard with every title override in
// that same mode into the most PERMISSIVE possible band — used only as the
// no-title-match fallback (locked decision #3: "per-title prefs when
// matched, else loosest-of").
//
// Claude 2026-10-15: the plan's literal wording ("min_res = min of the mins
// that are set; max_res = max of the maxes that are set") undersells one
// case: if ANY contributing Standard (the mode default itself, or any title
// override in this mode) has MinRes==0 ("no floor" — its most permissive
// possible value), the union's MinRes must ALSO be 0. "Min of the sets" over
// only the nonzero values would otherwise let a 1080p-floor title override
// make the union MORE restrictive than the mode default alone when the mode
// default has no floor — the opposite of "loosest." Same logic for
// MaxRes==0 ("no cap"). This is a deliberate generalization of the plan's
// wording, not a deviation from its intent (see the plan's open question 2
// clarification: "a 480p title override doesn't get skipped").
// Troubleshooting: TestLoosestOf_AnyNoFloorWins pins this.
// Review if: a future knob wants "min of configured floors, ignoring
// no-floor defaults" instead — that would need its own flag, not a change
// here.
func (p *PolicySnapshot) loosestOf(mode string) Standard {
	def := p.Modes[mode]
	tierSet := map[quality.Tier]bool{}
	for _, t := range def.Tiers {
		tierSet[t] = true
	}
	minRes, maxRes := def.MinRes, def.MaxRes
	anyNoMin, anyNoMax := def.MinRes == 0, def.MaxRes == 0
	for _, entries := range p.Titles {
		for _, e := range entries {
			if e.Mode != mode {
				continue
			}
			for _, t := range e.Tiers {
				tierSet[t] = true
			}
			if e.MinRes == 0 {
				anyNoMin = true
			} else if minRes == 0 || e.MinRes < minRes {
				minRes = e.MinRes
			}
			if e.MaxRes == 0 {
				anyNoMax = true
			} else if e.MaxRes > maxRes {
				maxRes = e.MaxRes
			}
		}
	}
	if anyNoMin {
		minRes = 0
	}
	if anyNoMax {
		maxRes = 0
	}
	tiers := make([]quality.Tier, 0, len(tierSet))
	for _, t := range quality.AllTiers {
		if tierSet[t] {
			tiers = append(tiers, t)
		}
	}
	return Standard{Mode: mode, Policy: "mode", Tiers: tiers, MinRes: minRes, MaxRes: maxRes}
}

// Resolve returns one Standard per candidate mode for releaseName in group
// (crawled under groupModes), plus the MatchInfo describing how the release
// name itself was classified. The caller (crawlGroup, Step 3) runs gate()
// against each Standard in order and admits on the first one that passes —
// "admitted if it passes the standard for any candidate mode" (plan,
// "Mode choice").
func (p *PolicySnapshot) Resolve(group string, groupModes []string, releaseName string) ([]Standard, MatchInfo) {
	prefix, year, isEpisode := splitReleaseTitle(releaseName)
	key := cleanTitleKey(prefix)
	candModes := classifyMode(isEpisode, groupModes)
	match := MatchInfo{Key: key, Year: year, IsEpisode: isEpisode, CandModes: candModes}

	stds := make([]Standard, 0, len(candModes))
	for _, mode := range candModes {
		std, ok, label := p.resolveForMode(mode, key, year)
		if ok {
			stds = append(stds, std)
			continue
		}
		fb := p.loosestOf(mode)
		fb.Match = label
		stds = append(stds, fb)
	}
	return stds, match
}

// Claude 2026-10-15: gate's reason strings — aggregate-only (locked decision
// #2: no per-item reject log), used by crawlGroup's per-chunk summary line.
const (
	reasonResUnknown    = "res-unknown"
	reasonResBelowMin   = "res-below-min"
	reasonResAboveMax   = "res-above-max"
	reasonTierOutOfBand = "tier-out-of-band"
)

// gate is the pure quality-gate decision (plan "Quality gate" steps 2-3;
// step 1, the unparsed/obfuscated check, happens before Parse in the
// caller). name is passed through only for autograb.SnapFloorResolution's
// title-string SD fallback (vhs/dvd-rip/divx substrings release.Info itself
// doesn't carry) — a small, documented widening of the plan's "gate(info,
// std)" signature to the inputs SnapFloorResolution actually needs.
func gate(name string, info release.Info, std Standard) (bool, string) {
	snapped := autograb.SnapFloorResolution(autograb.Candidate{
		Title:      name,
		Resolution: info.Resolution,
		Source:     info.Source,
		Codec:      info.Codec,
	})
	if std.MinRes > 0 {
		if snapped == 0 {
			// Claude 2026-10-15: strict reject on unknown resolution + a
			// configured floor — matches fileNeedsQualityUpgrade's convention
			// (open question 3, locked: strict, not neutral).
			return false, reasonResUnknown
		}
		if snapped < std.MinRes {
			return false, reasonResBelowMin
		}
	}
	if std.MaxRes > 0 && snapped > std.MaxRes {
		return false, reasonResAboveMax
	}
	if tier, ok := quality.InferTier(info); ok {
		if !tierAllowed(tier, std.Tiers) {
			return false, reasonTierOutOfBand
		}
	}
	// Unknown source (ok==false): neutral pass, matching autograb's own
	// "unknown is neutral" convention (plan step 3).
	return true, ""
}

// skipAdmissionGate lets spaced HD ident titles ingest under a 2160p movies
// floor (quoted 1080p fansubs). SD ident still faces MinRes.
// Claude 2026-10-20: HD-only (snapped >= 720).
// Reason: live `[WhyNot] … [DVD 480p AAC]` was admitted with min_res=720
// because any quotedReleaseName with ` _[` skipped the floor.
// Troubleshooting: admit res=480 min_res=720 → this returned true.
// Review if: 576p PAL ident should skip a 720 floor (today: no).
func skipAdmissionGate(name string) bool {
	if !quotedReleaseName(name) || !strings.ContainsAny(name, " _[") {
		return false
	}
	info := release.Parse(name)
	snapped := autograb.SnapFloorResolution(autograb.Candidate{
		Title:      name,
		Resolution: info.Resolution,
		Source:     info.Source,
		Codec:      info.Codec,
	})
	return snapped >= 720
}

func tierAllowed(t quality.Tier, allowed []quality.Tier) bool {
	for _, a := range allowed {
		if a == t {
			return true
		}
	}
	return false
}

// pickSample picks up to statSamplePerRelease msgids to STAT for one
// release's rows in this chunk: first/median/last of the payload rows
// (!IsMeta) ordered by (Filename, PartN, MsgNum), or the meta rows in the
// same order if there are no payload rows yet (PAR2 arrived first).
func pickSample(rows []HeaderRow) []string {
	source := filterRows(rows, false)
	if len(source) == 0 {
		source = filterRows(rows, true)
	}
	if len(source) == 0 {
		return nil
	}
	sort.Slice(source, func(i, j int) bool {
		if source[i].Filename != source[j].Filename {
			return source[i].Filename < source[j].Filename
		}
		if source[i].PartN != source[j].PartN {
			return source[i].PartN < source[j].PartN
		}
		return source[i].MsgNum < source[j].MsgNum
	})
	if len(source) <= statSamplePerRelease {
		out := make([]string, 0, len(source))
		for _, r := range source {
			out = append(out, r.MsgID)
		}
		return out
	}
	mid := len(source) / 2
	return []string{source[0].MsgID, source[mid].MsgID, source[len(source)-1].MsgID}
}

func filterRows(rows []HeaderRow, meta bool) []HeaderRow {
	out := make([]HeaderRow, 0, len(rows))
	for _, r := range rows {
		if r.IsMeta == meta {
			out = append(out, r)
		}
	}
	return out
}

// statDecision applies the plan's STAT decision table to one release's
// sampled StatArticleAll results, in the order checked: a hard skip
// threshold first (never bypassed by an inconclusive result), then
// inconclusive (fail-open on provider load), then partial, then ok.
func statDecision(results []usenet.StatVerdict) (keep bool, label string) {
	n := len(results)
	var missing, inconclusive int
	for _, r := range results {
		switch r {
		case usenet.StatMissing:
			missing++
		case usenet.StatInconclusive:
			inconclusive++
		}
	}
	switch {
	case missing >= statSkipMissing:
		return false, "missing"
	case n > 0 && n < statSamplePerRelease && missing == n:
		return false, "missing"
	case inconclusive > 0:
		return true, "inconclusive"
	case missing == 1:
		return true, "partial"
	default:
		return true, "ok"
	}
}
