// Package release parses a release title into its quality attributes
// (resolution, source, codec, release group) and scores it against a
// preference profile — the decision logic a manual search view needs to
// rank Prowlarr's results, kept deliberately separate from any HTTP client
// (this package makes no outbound calls at all).
//
// Parse deliberately does NOT attempt Radarr/Sonarr's full scene-naming
// edge-case coverage (a famously deep problem those projects have spent
// years on) — it's a pragmatic subset covering the common patterns, good
// enough for phase 1's manual search-and-grab, not a claim of parity.
package release

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Info is what Parse extracts from a release title. Zero values (Resolution
// == 0, Source/Codec/Group == "") mean "not recognized," not "absent" — a
// release can genuinely have an unparseable or nonstandard name.
type Info struct {
	Resolution int    // e.g. 1080, 2160; 0 if not recognized
	Source     string // e.g. "web-dl", "bluray", "hdtv"; "" if not recognized
	Codec      string // e.g. "x265", "x264"; "" if not recognized
	Group      string // release group; "" if not recognized
}

var (
	resolutionPattern = regexp.MustCompile(`(?i)\b(480|540|576|720|1080|2160)p?\b`)
	uhd4kPattern      = regexp.MustCompile(`(?i)\b(4k|uhd)\b`)
	codecPattern      = regexp.MustCompile(`(?i)\b(x264|x265|h\.?264|h\.?265|hevc|xvid|av1)\b`)
	// groupPattern matches the scene-naming convention of a trailing
	// "-GROUPNAME" at the very end of a release title.
	groupPattern = regexp.MustCompile(`-([A-Za-z0-9]+)$`)
)

// sourcePatterns is ordered so a more specific match (e.g. "web-dl") is
// tried before a more general one that could otherwise shadow it (e.g. a
// bare "web" inside "web-dl" itself) — matched in order, first hit wins.
var sourcePatterns = []struct {
	label string
	re    *regexp.Regexp
}{
	// remux is checked before bluray: a remux release's title also always
	// contains "bluray" (e.g. "2160p.BluRay.REMUX"), so bluray's pattern
	// would otherwise shadow it.
	{"remux", regexp.MustCompile(`(?i)\bremux\b`)},
	{"web-dl", regexp.MustCompile(`(?i)\bweb[.\-_]?dl\b`)},
	{"webrip", regexp.MustCompile(`(?i)\bweb[.\-_]?rip\b`)},
	{"web", regexp.MustCompile(`(?i)\bweb\b`)},
	{"bluray", regexp.MustCompile(`(?i)\b(bluray|blu[.\-_]?ray|bdrip|brrip)\b`)},
	{"hdtv", regexp.MustCompile(`(?i)\bhdtv\b`)},
	{"dvdrip", regexp.MustCompile(`(?i)\bdvdrip\b`)},
}

// Parse extracts quality attributes from a release title. Unrecognized
// fields are left at their zero value rather than guessed.
func Parse(title string) Info {
	var info Info

	if m := resolutionPattern.FindStringSubmatch(title); m != nil {
		if n, err := strconv.Atoi(m[1]); err == nil {
			info.Resolution = n
		}
	} else if uhd4kPattern.MatchString(title) {
		info.Resolution = 2160
	}

	for _, sp := range sourcePatterns {
		if sp.re.MatchString(title) {
			info.Source = sp.label
			break
		}
	}

	if m := codecPattern.FindStringSubmatch(title); m != nil {
		codec := strings.ToLower(m[1])
		codec = strings.ReplaceAll(codec, ".", "")
		if codec == "h264" {
			codec = "x264"
		}
		if codec == "h265" || codec == "hevc" {
			codec = "x265"
		}
		info.Codec = codec
	}

	if m := groupPattern.FindStringSubmatch(title); m != nil {
		info.Group = m[1]
	}

	return info
}

// Profile is a user's release preferences. Resolution and source/codec are
// deliberately independent axes: PreferredResolutions is driven by a
// separate "maximum resolution" setting (see internal/quality's
// ProfileFor), while PreferredSources/PreferredCodecs express a quality
// TIER — a bitrate/compression preference (how heavily compressed a
// release is), which has nothing to do with what resolution it's at.
// Phase 1 hardcodes DefaultProfile as the zero-config fallback; internal/
// quality builds the Settings-driven version of this.
type Profile struct {
	// PreferredResolutions is ordered best-to-worst; a resolution not listed
	// scores as if it were worst of all.
	PreferredResolutions []int
	// PreferredSources is ordered best-to-worst, matching Info.Source's
	// labels; a source not listed (including "") scores as if worst of all.
	PreferredSources []string
	// PreferredCodecs is ordered best-to-worst, matching Info.Codec's
	// labels; a codec not listed (including "") scores as if worst of all.
	// Empty means no codec preference at all (e.g. a Lossless tier, where a
	// remux typically isn't re-encoded and so has no "preferred codec" to
	// speak of).
	PreferredCodecs []string
	// BlockedGroups makes Score return a large negative number for any
	// release whose Group matches, case-insensitively — never the winner,
	// but still visible in results rather than silently dropped.
	BlockedGroups []string
}

// DefaultProfile is a reasonable zero-config ordering: 1080p is preferred
// over 2160p (better balance of quality vs. size/bandwidth for most
// setups), WEB-DL/WEBRip preferred over BluRay/HDTV (no re-encode step,
// smaller, widely available), x265 preferred over x264 as a tiebreak (more
// efficient compression at equal resolution/source).
func DefaultProfile() Profile {
	return Profile{
		PreferredResolutions: []int{1080, 2160, 720, 480},
		PreferredSources:     []string{"web-dl", "webrip", "bluray", "web", "hdtv", "dvdrip"},
		PreferredCodecs:      []string{"x265", "x264"},
	}
}

const blockedGroupScore = -1000

// Score ranks info against prefs — higher is better. Resolution and source
// dominate; codec only ever breaks a tie between two releases already equal
// on both.
func Score(info Info, prefs Profile) int {
	for _, blocked := range prefs.BlockedGroups {
		if info.Group != "" && strings.EqualFold(info.Group, blocked) {
			return blockedGroupScore
		}
	}

	score := 0
	score += rank(prefs.PreferredResolutions, info.Resolution) * 100
	score += rankString(prefs.PreferredSources, info.Source) * 10
	score += rankString(prefs.PreferredCodecs, info.Codec)
	return score
}

// Candidate is one search result's full scoring input — Info plus the
// signals ScoreCandidate weighs beyond resolution/source/codec: how
// established a torrent is (seeders) or how fresh a usenet post is (age),
// and any indexer-reported trust signal. Kept separate from Info itself
// since Parse only ever extracts what's encoded in a title string, never
// these out-of-band fields.
type Candidate struct {
	Info Info
	// Protocol is "torrent" or "usenet" — a plain string (matching
	// prowlarr.Protocol's own values) rather than importing that type
	// directly, so this package keeps its existing zero-dependency shape.
	Protocol string
	// Seeders only matters for torrent candidates.
	Seeders int
	// PublishDate is RFC3339, as Prowlarr reports it; "" if unknown or
	// unparseable — scores as neutral (no age bonus), not penalized.
	PublishDate string
	// IndexerFlags is Prowlarr's per-result indexer metadata (e.g.
	// "freeleech", "internal") — sourced entirely from Prowlarr, no
	// additional lookup.
	IndexerFlags []string
}

const (
	// torrentSeederCap bounds how much raw seeder count can move the score —
	// enough to matter within a resolution/source tier (rank swings of 100
	// and 10 respectively) without letting seeders alone override a real
	// quality-tier difference.
	torrentSeederCap = 200
	// usenetAgeCapDays bounds how many days of freshness bonus a usenet
	// post can earn. Brand-new (0 days) gets the max; posts at/over the
	// cap get 0. Quality Score() still dominates (resolution 100 / source 10).
	usenetAgeCapDays  = 30
	usenetAgeWeight   = 3
	indexerTrustBonus = 50
)

// ScoreCandidate ranks c against prefs exactly like Score, then adds:
//   - torrent: a bonus for seeder count (capped), rewarding a well-seeded
//     release over a barely-seeded one of otherwise similar quality;
//   - usenet: a bonus for freshness within the same quality tier — newer
//     beats older up to usenetAgeCapDays (0 days = max bonus, 30+ = 0).
//     Claude 2026-09-22: inverted from older-is-safer (was days*weight).
//     Reason: operators prefer a just-posted NZB over a 30-day-old copy of
//     the same resolution/source/codec; retention is the real age risk and
//     is handled by the STAT precheck, not ranking.
//     Review if: live grab picks prefer stale copies of equal quality.
//   - either protocol: a flat bonus if IndexerFlags marks the release as
//     freeleech or internal — the one "reputation" signal, sourced
//     entirely from Prowlarr, no additional lookup.
func ScoreCandidate(c Candidate, prefs Profile, now time.Time) int {
	score := Score(c.Info, prefs)
	if score == blockedGroupScore {
		return score // a blocked group stays the worst possible score, full stop
	}

	switch c.Protocol {
	case "torrent":
		seeders := c.Seeders
		if seeders > torrentSeederCap {
			seeders = torrentSeederCap
		}
		if seeders > 0 {
			score += seeders
		}
	case "usenet":
		if days, ok := daysSince(c.PublishDate, now); ok {
			// Prefer newer: bonus = (usenetAgeCapDays - days) * weight when days <= cap
			// brand new (0 days) gets max bonus; 30+ days gets 0
			if days > usenetAgeCapDays {
				days = usenetAgeCapDays
			}
			// score += days * usenetAgeWeight // Claude 2026-09-22: older-is-safer inverted
			score += (usenetAgeCapDays - days) * usenetAgeWeight
		}
	}

	for _, flag := range c.IndexerFlags {
		if strings.EqualFold(flag, "freeleech") || strings.EqualFold(flag, "internal") {
			score += indexerTrustBonus
			break
		}
	}
	return score
}

// daysSince parses publishDate (RFC3339) and reports how many whole days
// before now it was, or ok=false if publishDate is empty/unparseable.
func daysSince(publishDate string, now time.Time) (days int, ok bool) {
	if publishDate == "" {
		return 0, false
	}
	t, err := time.Parse(time.RFC3339, publishDate)
	if err != nil {
		return 0, false
	}
	d := int(now.Sub(t).Hours() / 24)
	if d < 0 {
		return 0, false
	}
	return d, true
}

// rank returns how many positions from the end of order value is — the
// best (index 0) scores len(order), the worst scores 1, and a value not in
// order at all scores 0 (worse than every listed value).
func rank(order []int, value int) int {
	for i, v := range order {
		if v == value {
			return len(order) - i
		}
	}
	return 0
}

func rankString(order []string, value string) int {
	for i, v := range order {
		if v == value {
			return len(order) - i
		}
	}
	return 0
}
