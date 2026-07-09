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

// Profile is a user's release preferences — phase 1 hardcodes DefaultProfile
// rather than exposing this through Settings; a real quality-profile UI is a
// later phase.
type Profile struct {
	// PreferredResolutions is ordered best-to-worst; a resolution not listed
	// scores as if it were worst of all.
	PreferredResolutions []int
	// PreferredSources is ordered best-to-worst, matching Info.Source's
	// labels; a source not listed (including "") scores as if worst of all.
	PreferredSources []string
	// BlockedGroups makes Score return a large negative number for any
	// release whose Group matches, case-insensitively — never the winner,
	// but still visible in results rather than silently dropped.
	BlockedGroups []string
}

// DefaultProfile is a reasonable default ordering: 1080p is preferred over
// 2160p (better balance of quality vs. size/bandwidth for most setups),
// WEB-DL/WEBRip preferred over BluRay/HDTV (no re-encode step, smaller,
// widely available).
func DefaultProfile() Profile {
	return Profile{
		PreferredResolutions: []int{1080, 2160, 720, 480},
		PreferredSources:     []string{"web-dl", "webrip", "bluray", "web", "hdtv", "dvdrip"},
	}
}

const blockedGroupScore = -1000

// Score ranks info against prefs — higher is better. Two releases with
// identical resolution+source rank are broken by preferring the more
// efficient codec (x265 over x264/other).
func Score(info Info, prefs Profile) int {
	for _, blocked := range prefs.BlockedGroups {
		if info.Group != "" && strings.EqualFold(info.Group, blocked) {
			return blockedGroupScore
		}
	}

	score := 0
	score += rank(prefs.PreferredResolutions, info.Resolution) * 100
	score += rankString(prefs.PreferredSources, info.Source) * 10
	if info.Codec == "x265" {
		score++
	}
	return score
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
