// Package quality maps a user-facing quality tier (Low/Medium/High/
// Lossless) plus a maximum-resolution cap onto internal/release's Profile —
// the preference ordering Search's scoring ranks results against.
//
// Tier and maximum resolution are deliberately separate, independent
// settings: a tier is a bitrate/compression preference (how heavily
// compressed a release is — source and codec), which has nothing to do
// with what resolution it's at. Conflating the two (as an earlier version
// of this package did, ranking 1080p/2160p by tier) was a mistake — a user
// choosing "Low" wants smaller, more-compressed files at whatever
// resolution they're capped at, not necessarily a lower resolution.
package quality

import "github.com/labbersanon/sakms/internal/release"

// Tier is a coarse, user-facing bitrate/compression preference.
type Tier string

const (
	Low      Tier = "low"
	Medium   Tier = "medium"
	High     Tier = "high"
	Lossless Tier = "lossless"
)

// Default is High, matching release.DefaultProfile()'s source/codec
// ordering exactly — an install that never touches this setting sees no
// behavior change from before tiers existed.
const Default = High

// resolutionLadder is every resolution internal/release.Parse recognizes,
// highest first.
var resolutionLadder = []int{2160, 1080, 720, 480}

// ProfileFor maps t and maxResolution onto the release.Profile Search
// scores against. maxResolution == 0 means "no cap" — resolutions fall back
// to release.DefaultProfile()'s own ordering (1080p preferred over 2160p, a
// zero-config bandwidth/size balance), matching today's behavior exactly.
//
// A nonzero cap is soft, not a hard filter: it reorders the preference list
// to favor resolutions at or below the cap (closest to the cap first), but
// a release above the cap still scores — just as the worst-ranked option —
// rather than being excluded outright. If nothing at or below the cap is
// available, Search still shows what actually exists instead of an empty
// result set.
func ProfileFor(t Tier, maxResolution int) release.Profile {
	return release.Profile{
		PreferredResolutions: resolutionsFor(maxResolution),
		PreferredSources:     sourcesFor(t),
		PreferredCodecs:      codecsFor(t),
	}
}

func resolutionsFor(maxResolution int) []int {
	if maxResolution <= 0 {
		return release.DefaultProfile().PreferredResolutions
	}
	out := make([]int, 0, len(resolutionLadder))
	for _, r := range resolutionLadder {
		if r <= maxResolution {
			out = append(out, r)
		}
	}
	return out
}

// sourcesFor expresses each tier as a compression preference — Low favors
// the smaller, more-compressed shapes (WEBRip/HDTV); Lossless favors the
// least-compressed source available (a remux, or an untouched Blu-ray)
// regardless of what resolution it happens to be at.
func sourcesFor(t Tier) []string {
	switch t {
	case Low:
		return []string{"webrip", "hdtv", "web", "web-dl"}
	case Medium:
		return []string{"web-dl", "webrip", "web", "hdtv"}
	case Lossless:
		return []string{"remux", "bluray", "web-dl"}
	default: // High
		return release.DefaultProfile().PreferredSources
	}
}

// InferTier guesses which Tier a parsed release most likely belongs to, by
// reverse-mapping its source (and, where the source alone is ambiguous, its
// codec) against the tier definitions sourcesFor/codecsFor express. Used
// ONLY by the one-time size/tier backfill (internal/library) to reconstruct
// a tier for items that entered the library before the tier column existed
// — never on a scoring path, where the operator's configured tier is
// authoritative and no guessing is wanted. ok is false when nothing matches
// confidently; the caller records "unknown" rather than inventing a value.
//
// Two things this deliberately does NOT do:
//
//   - It never consults Resolution. This package's doc comment is explicit
//     that tier and resolution are independent settings and that conflating
//     them was a mistake; a reverse mapping that reintroduced the
//     conflation would be reintroducing the bug.
//   - It never maps a bare "bluray" to Lossless. sourcesFor(Lossless)
//     lists bluray, but so does High's list (release.DefaultProfile().
//     PreferredSources), so the source alone cannot discriminate a
//     remux-quality Blu-ray rip from an ordinary re-encode. High is the
//     conservative default for that case, which is why High is reachable
//     here only via bluray+non-x265 — see
//     TestInferTierNeverReturnsHighForNonBlurayInput, which pins that
//     property so a future edit cannot make High common (or impossible)
//     unnoticed.
//
// Honest note on the two tie-breaks below, since neither is derivable from
// sourcesFor/codecsFor and a future reader will look for a derivation:
// codecsFor(Lossless) returns nil (no codec preference at all), so
// bluray+x265 -> Lossless is a chosen convention for "someone deliberately
// spent an efficient codec on a Blu-ray source," not a lookup; and dvdrip
// does appear in DefaultProfile().PreferredSources, so dvdrip -> Low is
// likewise a convention (it is the most-compressed shape release.Parse
// recognizes), not an exclusion derived from the tier lists.
func InferTier(info release.Info) (Tier, bool) {
	switch info.Source {
	case "remux":
		// Unique to sourcesFor(Lossless) — the one unambiguous signal.
		return Lossless, true
	case "bluray":
		if info.Codec == "x265" {
			return Lossless, true
		}
		return High, true
	case "webrip", "hdtv":
		// Both head sourcesFor(Low); Low is also the only tier with a codec
		// preference (codecsFor(Low) == ["x265"]), so an x265 pairing is the
		// closest inferable Low signal and anything else falls to Medium.
		if info.Codec == "x265" {
			return Low, true
		}
		return Medium, true
	case "web-dl", "web":
		// web-dl heads sourcesFor(Medium).
		return Medium, true
	case "dvdrip":
		return Low, true
	default:
		// Only reachable for Source == "" (an unrecognized/unparseable
		// title): the seven cases above cover every label release.Parse can
		// emit. The caller records "unknown".
		return "", false
	}
}

// codecsFor expresses a per-tier codec preference. Low prefers x265 (more
// efficient compression — a smaller file at a given visual quality, the
// closest analogue to "lower bitrate" a release title can actually
// encode). Lossless has no codec preference at all: a remux is typically
// not re-encoded, so there's nothing meaningful to prefer.
func codecsFor(t Tier) []string {
	switch t {
	case Low:
		return []string{"x265"}
	case Lossless:
		return nil
	default: // Medium, High
		return release.DefaultProfile().PreferredCodecs
	}
}
