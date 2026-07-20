// Package autograb is the bitrate-quality-floor scorer used ONLY by
// Discover's unattended one-click auto-grab — a deliberately separate,
// permanently-coexisting scorer from internal/release.ScoreCandidate.
//
// The two solve different problems for different UIs and neither replaces
// the other (see the plan's explicit reconciliation):
//
//   - release.ScoreCandidate ranks the MANUAL Search view, where a human
//     reviews every result. It's a fast title-parse scorer with no extra
//     fetch — the right tool when there's a person in the loop.
//   - autograb (this package) gatekeeps UNATTENDED auto-grab, where there is
//     no human safety net. It needs the release's real implied bitrate
//     (Size×8/runtime), which only pays for itself precisely because nothing
//     else catches a bad grab. It reuses internal/quality.Tier (the existing
//     per-mode quality-tier setting) rather than introducing a second tier,
//     and indexes its floor table by [tier, resolution] as INDEPENDENT axes
//     (never "scale a 1080p number by resolution") to honor quality.go's own
//     decoupling principle.
//
// Everything here is a pure function: no HTTP, no DB, no ffprobe. The caller
// (internal/api) parses titles (via internal/release.Parse), supplies the
// TMDB/TPDB runtime, and — post-grab — supplies a probed duration. That
// keeps this package trivially unit-testable, which matters more than usual:
// auto-grab has no human review by design, so the scoring math is the only
// thing standing between a mislabeled release and the library.
package autograb

import (
	"sort"
	"strings"

	"github.com/labbersanon/sakms/internal/quality"
)

// Tunable defaults. These are the plan's proposed starting points, explicitly
// documented as tunable (validate against the real library once live), not
// locked constants.
const (
	// DefaultMinSeeders is the minimum seeder/health floor a torrent must
	// clear before it's eligible for unattended auto-grab. Usenet candidates
	// (no seeders) skip this check entirely.
	DefaultMinSeeders = 5

	// NonAV1PaddingFactor is the 25% margin a non-AV1 (x264/x265) release must
	// clear its tier floor by: since every import is re-encoded to AV1
	// downstream by FileFlows, a non-AV1 source has to survive that second
	// lossy generation. AV1 sources are graded at face value (already the
	// terminal format).
	NonAV1PaddingFactor = 1.25

	// MislabelFactor sets the pre-grab sanity line: an x264-equivalent implied
	// bitrate below MislabelFactor × the Low-tier floor for the CLAIMED
	// resolution is implausibly low for that resolution (e.g. a "2160p"
	// release carrying a 480p-typical bitrate) and is treated as mislabeled —
	// excluded from auto-grab, surfaced to manual review instead. Deliberately
	// lenient: it catches fake labels, not honestly-small low-tier releases.
	MislabelFactor = 0.4

	// RuntimeLowRatio and RuntimeHighRatio bound the post-grab runtime
	// comparison. A probed duration is only flagged as a mismatch when it
	// falls OUTSIDE this band relative to the known runtime — catching sample
	// files and wrong-content grabs, while tolerating legitimate
	// theatrical-vs-extended cuts, PAL speedup, and metadata rounding. Errs
	// hard toward not-flagging (there is no human review to reverse a false
	// flag).
	RuntimeLowRatio  = 0.70
	RuntimeHighRatio = 1.30
)

// CodecMultipliers expresses each codec's efficiency relative to x264
// (baseline 1.0): a codec at multiplier m achieves x264-equivalent quality at
// m× the bitrate. To NORMALIZE a real release's implied bitrate to an
// x264-equivalent, divide by the multiplier — an efficient codec's smaller
// file is genuinely worth more than its raw bitrate suggests (x265 at 5 Mbps
// ≈ x264 at 10 Mbps). An unrecognized codec falls back to 1.0 (no efficiency
// credit — conservative, never over-grades an unknown encode).
var CodecMultipliers = map[string]float64{
	"x264": 1.0,
	"x265": 0.5,
	"av1":  0.35,
}

// floorTable is the tier×resolution minimum x264-equivalent bitrate (Mbps) a
// release must clear to auto-qualify. Indexed by two INDEPENDENT axes per
// quality.go's decoupling principle — not derived by scaling one row.
// Lossless additionally qualifies on a remux/bluray source flag alone,
// bypassing the bitrate floor (see gradeQuality).
//
// The 480p row is a later addition (the Discover detail-popup plan's
// resolution axis) — explicitly documented as tunable starting points,
// matching this file's own DefaultMinSeeders/NonAV1PaddingFactor convention,
// NOT a claim that these exact numbers are validated against real releases.
// Confirmed with the project owner that adding a real 480p row (rather than
// excluding 480p from any caller) is correct: before this, a 480p candidate
// always graded StatusUnknownResolution regardless of tier — a real gap in
// production auto-grab-gating data, not just a new-feature-only concern.
// Derived by following the same relationship the existing 720→1080 step
// already establishes (pixel-area ratio ≈0.444/0.445 at both 480p:720p and
// 720p:1080p): divide each tier's 720p floor by ~2.5, preserving the same
// cross-tier ratios (~2.5×/2×/1.8× between adjacent tiers) the table already
// has at every other resolution — see
// TestFloorTable480pRatiosConsistentWithExistingRows.
var floorTable = map[quality.Tier]map[int]float64{
	quality.Low:      {480: 0.3, 720: 0.8, 1080: 2, 2160: 8},
	quality.Medium:   {480: 0.8, 720: 2, 1080: 5, 2160: 20},
	quality.High:     {480: 1.6, 720: 4, 1080: 10, 2160: 40},
	quality.Lossless: {480: 2.8, 720: 7, 1080: 18, 2160: 70},
}

// Status is why a candidate did or didn't auto-qualify — carried through so
// the UI-wiring wave can label the manual pick list ("mislabeled", "below
// floor", etc.) rather than showing a bare rejected/accepted flag.
type Status string

const (
	// StatusQualified — clears every gate; eligible for unattended auto-grab.
	StatusQualified Status = "qualified"
	// StatusBelowFloor — bitrate is known and plausible but under the tier
	// floor. Not auto-grabbed; appears in the manual pick list.
	StatusBelowFloor Status = "below-floor"
	// StatusMislabeled — implied bitrate is wildly inconsistent with the
	// claimed resolution/codec. Excluded from auto-grab, surfaced to manual
	// review.
	StatusMislabeled Status = "mislabeled"
	// StatusLowSeeders — a torrent under the minimum seeder floor. Not
	// auto-grabbed regardless of quality.
	StatusLowSeeders Status = "low-seeders"
	// StatusUnknownBitrate — Size==0 or runtime unknown, so implied bitrate
	// can't be computed. NEUTRAL: never mislabeled, never auto-qualified —
	// lands in the manual pick list rather than being false-positive rejected.
	StatusUnknownBitrate Status = "unknown-bitrate"
	// StatusUnknownResolution — bitrate is known but the claimed resolution
	// isn't one the floor table covers, so it can't be graded against a floor.
	// Neutral, same as unknown bitrate: manual list, not auto-grabbed.
	StatusUnknownResolution Status = "unknown-resolution"
)

// Candidate is one release's full auto-grab scoring input. Resolution/Codec/
// Source are the already-parsed fields from internal/release.Parse; Runtime
// comes from TMDB/TPDB metadata (known before grabbing for Movies/Series).
type Candidate struct {
	// Title is echoed through for the caller to correlate back to its own
	// search result; it is not parsed here.
	Title string
	// Protocol is "torrent" or "usenet" (matching prowlarr.Protocol's values).
	// Only torrents are seeder-floored.
	Protocol string
	// Seeders is a torrent's seeder count; ignored for usenet.
	Seeders int
	// SizeBytes is Prowlarr's reported release size. 0 → implied bitrate
	// unknown (neutral, per StatusUnknownBitrate).
	SizeBytes int64
	// RuntimeSeconds is the TMDB/TPDB known runtime. 0/unknown → implied
	// bitrate unknown (neutral).
	RuntimeSeconds float64
	// Resolution is 720/1080/2160 (from release.Parse); 0 or any other value
	// is treated as unknown resolution (neutral).
	Resolution int
	// Codec is "x264"/"x265"/"av1" (from release.Parse); anything else,
	// including "", falls back to the x264 baseline multiplier.
	Codec string
	// Source is "remux"/"bluray"/... (from release.Parse) — the Lossless-tier
	// flag that alone qualifies a candidate regardless of bitrate.
	Source string
}

// Grade is a single candidate's evaluation. Score is the ranking key for the
// manual pick list (the graded, padding-adjusted x264-equivalent bitrate in
// Mbps) — the SAME score that gates auto-grab, so a fallback list's ordering
// is consistent with why nothing auto-qualified. Score is 0 when the bitrate
// is unknown (those sort last).
type Grade struct {
	Candidate     Candidate
	BitrateKnown  bool
	ImpliedMbps   float64 // Size×8/runtime, raw; 0 if unknown
	X264EquivMbps float64 // implied normalized to x264-equivalent; 0 if unknown
	Score         float64 // X264EquivMbps after non-AV1 padding; the ranking key
	FloorMbps     float64 // the tier×resolution floor applied; 0 if resolution unknown
	Status        Status
	Qualified     bool
}

// Selection is the outcome of evaluating every candidate for one title/
// season/scene. PickIndex is the auto-grab choice (highest Score among
// qualified), or -1 when nothing qualifies. Fallback == (PickIndex == -1):
// the signal for the UI to present the manual pick list — Ranked gives that
// list's order (all candidates, best Score first), consistent with the same
// bitrate score that rejected them.
type Selection struct {
	Grades    []Grade
	PickIndex int
	Fallback  bool
	Ranked    []int // indices into Grades, best Score first (stable)
}

func codecMultiplier(codec string) float64 {
	if m, ok := CodecMultipliers[strings.ToLower(codec)]; ok {
		return m
	}
	return 1.0
}

func isAV1(codec string) bool { return strings.EqualFold(codec, "av1") }

// GradeCandidate evaluates one candidate against tier's floor. minSeeders <= 0
// falls back to DefaultMinSeeders.
func GradeCandidate(c Candidate, tier quality.Tier, minSeeders int) Grade {
	if minSeeders <= 0 {
		minSeeders = DefaultMinSeeders
	}
	g := Grade{Candidate: c}

	// Missing-input handling: Size==0 or runtime unknown → implied bitrate
	// unknown. NEUTRAL — skip the sanity/floor checks entirely rather than
	// false-positive reject. Not auto-qualified (nothing to grade), so it
	// lands in the manual pick list.
	if c.SizeBytes <= 0 || c.RuntimeSeconds <= 0 {
		g.BitrateKnown = false
		g.Status = StatusUnknownBitrate
		return g
	}
	g.BitrateKnown = true
	g.ImpliedMbps = float64(c.SizeBytes) * 8 / c.RuntimeSeconds / 1e6
	g.X264EquivMbps = g.ImpliedMbps / codecMultiplier(c.Codec)

	// 25% padding for non-AV1 (dividing the value by 1.25 is equivalent to
	// requiring it clear the floor by that margin); AV1 graded at face value.
	g.Score = g.X264EquivMbps
	if !isAV1(c.Codec) {
		g.Score = g.X264EquivMbps / NonAV1PaddingFactor
	}

	floors, ok := floorTable[tier]
	if !ok {
		floors = floorTable[quality.Default] // unrecognized/empty tier → default
	}
	floor, hasFloor := floors[c.Resolution]
	if !hasFloor {
		// Bitrate known, but the claimed resolution isn't gradeable — neutral.
		g.Status = StatusUnknownResolution
		return g
	}
	g.FloorMbps = floor

	// Pre-grab mislabel check: implausibly low x264-equivalent bitrate for the
	// claimed resolution → excluded (fake label, not a preference miss).
	if g.X264EquivMbps < MislabelFactor*floorTable[quality.Low][c.Resolution] {
		g.Status = StatusMislabeled
		return g
	}

	// Minimum seeder/health floor (torrents only).
	if c.Protocol == "torrent" && c.Seeders < minSeeders {
		g.Status = StatusLowSeeders
		return g
	}

	// Lossless: a remux/bluray source flag alone qualifies, bypassing the
	// bitrate floor.
	if tier == quality.Lossless && (strings.EqualFold(c.Source, "remux") || strings.EqualFold(c.Source, "bluray")) {
		g.Status = StatusQualified
		g.Qualified = true
		return g
	}

	if g.Score >= floor {
		g.Status = StatusQualified
		g.Qualified = true
		return g
	}
	g.Status = StatusBelowFloor
	return g
}

// Select grades every candidate and picks the highest-scored qualifying one
// for auto-grab. When nothing qualifies, Fallback is true and the caller
// presents Ranked (all candidates, best bitrate score first) as the manual
// pick list — ranked by this same score, never by release.ScoreCandidate.
func Select(candidates []Candidate, tier quality.Tier, minSeeders int) Selection {
	grades := make([]Grade, len(candidates))
	for i, c := range candidates {
		grades[i] = GradeCandidate(c, tier, minSeeders)
	}

	pick := -1
	for i := range grades {
		if !grades[i].Qualified {
			continue
		}
		if pick == -1 || grades[i].Score > grades[pick].Score {
			pick = i
		}
	}

	ranked := make([]int, len(grades))
	for i := range ranked {
		ranked[i] = i
	}
	sort.SliceStable(ranked, func(a, b int) bool {
		return grades[ranked[a]].Score > grades[ranked[b]].Score
	})

	return Selection{
		Grades:    grades,
		PickIndex: pick,
		Fallback:  pick == -1,
		Ranked:    ranked,
	}
}

// RuntimeMismatch is the pure post-grab mislabel check: it compares the
// downloaded file's probed duration against the known TMDB/TPDB runtime (both
// in seconds). checked == false when EITHER is unknown (<= 0) — the caller
// must treat that as "unknown, skip," never a mismatch (a zero ffprobe
// duration is a valid probe result, not evidence of a bad file). When
// checked, mismatch is true only for a gross discrepancy outside the
// [RuntimeLowRatio, RuntimeHighRatio] band; legitimate cut/rounding
// differences stay within it.
func RuntimeMismatch(probedSeconds, expectedSeconds float64) (mismatch, checked bool) {
	if probedSeconds <= 0 || expectedSeconds <= 0 {
		return false, false
	}
	ratio := probedSeconds / expectedSeconds
	if ratio < RuntimeLowRatio || ratio > RuntimeHighRatio {
		return true, true
	}
	return false, true
}
