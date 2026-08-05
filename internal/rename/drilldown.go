// Claude 2026-08-05: Rename drilldown matcher — title ranks TMDB hits; year/actor/duration corroborate
// Reason: Dice confidence alone accepted weak top hits; operators want multi-signal verification
// Troubleshooting: unmatched/wrong Pending — check FileSignals.HasAny and MatchConfig N/tolerance
// Review if: Adult Rename adopts the same drilldown or a different identify path remains sole Adult matcher
// Related files: rename.go, confidence.go (legacy Dice retained for transitional TVDB paths until removed)
// Context: see .omc/specs/deep-interview-rename-match-drilldown.md
package rename

import (
	"context"
	"math"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/labbersanon/sakms/internal/tmdb"
)

// DefaultCandidateN / DefaultDurationTolerancePct are Advanced Settings defaults
// when per-mode keys are unset.
const (
	DefaultCandidateN           = 5
	DefaultDurationTolerancePct = 5
	MaxCandidateN               = 20
	MaxDurationTolerancePct     = 50
)

// MatchConfig is the operator-tunable drilldown cut for Movies/Series Rename.
type MatchConfig struct {
	CandidateN           int
	DurationTolerancePct int
}

// DefaultMatchConfig returns N=5 and ±5% duration tolerance.
func DefaultMatchConfig() MatchConfig {
	return MatchConfig{
		CandidateN:           DefaultCandidateN,
		DurationTolerancePct: DefaultDurationTolerancePct,
	}
}

// Normalize clamps empty/invalid fields to defaults (and max caps).
func (c MatchConfig) Normalize() MatchConfig {
	if c.CandidateN < 1 {
		c.CandidateN = DefaultCandidateN
	}
	if c.CandidateN > MaxCandidateN {
		c.CandidateN = MaxCandidateN
	}
	if c.DurationTolerancePct < 0 {
		c.DurationTolerancePct = DefaultDurationTolerancePct
	}
	if c.DurationTolerancePct > MaxDurationTolerancePct {
		c.DurationTolerancePct = MaxDurationTolerancePct
	}
	return c
}

// FileSignals are corroborating hints extracted from an orphan name/path.
// Zero / empty means the signal is absent (ignore for that check).
type FileSignals struct {
	Year        int
	Actor       string
	DurationSec float64
}

// HasAny reports whether at least one corroborating signal is available.
func (s FileSignals) HasAny() bool {
	return s.Year != 0 || s.Actor != "" || s.DurationSec > 0
}

var (
	creditActorYearRe = regexp.MustCompile(`(?i)^(.+?)\s*-\s+(.+)\((\d{4})\)\s*$`)
	fileYearParenRe   = regexp.MustCompile(`\((19\d{2}|20\d{2})\)`)
	fileYearBareRe    = regexp.MustCompile(`\b(19\d{2}|20\d{2})\b`)
	fileYearBracketRe = regexp.MustCompile(`\[(19\d{2}|20\d{2})\]`)
)

// ExtractFileSignals pulls year/actor from the orphan display name and optional
// ffprobe duration from videoPath. A nil prober skips duration.
func ExtractFileSignals(ctx context.Context, name, videoPath string, prober Prober) FileSignals {
	var sig FileSignals
	base := name
	if filepath.Ext(name) != "" {
		base = strings.TrimSuffix(filepath.Base(name), filepath.Ext(filepath.Base(name)))
	} else {
		base = filepath.Base(name)
	}
	if m := creditActorYearRe.FindStringSubmatch(base); m != nil {
		sig.Actor = strings.TrimSpace(m[2])
		if y, err := strconv.Atoi(m[3]); err == nil {
			sig.Year = y
		}
	}
	if sig.Year == 0 {
		sig.Year = extractFileYear(base)
	}
	if prober != nil && videoPath != "" {
		if probe, err := prober.Probe(ctx, videoPath); err == nil && probe != nil && probe.Duration > 0 {
			sig.DurationSec = probe.Duration
		}
	}
	return sig
}

func extractFileYear(s string) int {
	if m := fileYearParenRe.FindStringSubmatch(s); m != nil {
		y, _ := strconv.Atoi(m[1])
		return y
	}
	if m := fileYearBracketRe.FindStringSubmatch(s); m != nil {
		y, _ := strconv.Atoi(m[1])
		return y
	}
	if m := fileYearBareRe.FindStringSubmatch(s); m != nil {
		y, _ := strconv.Atoi(m[1])
		return y
	}
	return 0
}

// SignalsPass reports whether file signals agree with the candidate.
// Missing candidate metadata for a signal is ignored (not a hard fail), but
// at least one signal must positively corroborate.
//
// Year mismatch normally fails the candidate. Exception: when file duration
// is within tolerance of TMDB runtime, duration may override a wrong filename
// year (actor match alone cannot).
//
// When year already matches exactly, a duration disagreement does not veto —
// TMDB runtimes are often short vs the file on disk (e.g. Austin Powers 89 vs ~95).
func SignalsPass(sig FileSignals, candYear, runtimeMin int, cast []string, tolPct int) bool {
	if !sig.HasAny() {
		return false
	}
	corroborated := false
	yearMatched := false
	yearMismatch := false
	// Claude 2026-08-05: missing candidate metadata ≠ mismatch (ignore that signal)
	// Reason: empty search release_date + failed MovieDetails made year fail with candYear=0
	// Troubleshooting: "top N passed corroboration" on titles with correct year on file — check MovieDetails
	// Review if: accepting candidates with no year metadata causes wrong Pending too often
	if sig.Year != 0 {
		if candYear != 0 {
			if sig.Year == candYear {
				yearMatched = true
				corroborated = true
			} else {
				yearMismatch = true
			}
		}
	}
	if sig.Actor != "" {
		// Empty cast = credits unavailable — ignore actor signal (same as candYear==0).
		if len(cast) > 0 {
			if !actorInCast(sig.Actor, cast) {
				return false
			}
			corroborated = true
		}
	}
	durationOK := false
	if sig.DurationSec > 0 {
		if runtimeMin > 0 {
			if durationWithinTolerance(sig.DurationSec, runtimeMin, tolPct) {
				durationOK = true
				corroborated = true
			} else if !yearMatched {
				// Claude 2026-08-05: year match wins over duration disagreement
				// Reason: Austin Powers file ~95min vs TMDB 89 failed ±5% despite exact year
				// Troubleshooting: Pending missing with correct year — check whether duration vetoed
				// Review if: wrong same-year remakes get accepted when duration should have blocked
				return false
			}
		}
	}
	// Claude 2026-08-05: duration-only soft override for wrong filename year
	// Reason: JoJo Dancer tagged (2007) but film is 1986; actor alone must not override year
	// Troubleshooting: wrong-year file matches with actor but no ffprobe — should stay Unmatched
	// Review if: duration override accepts wrong titles with similar runtimes too often
	if yearMismatch && !durationOK {
		return false
	}
	return corroborated
}

func actorInCast(credit string, cast []string) bool {
	want := strings.ToLower(strings.TrimSpace(credit))
	if want == "" {
		return false
	}
	for _, name := range cast {
		n := strings.ToLower(strings.TrimSpace(name))
		if n == "" {
			continue
		}
		if n == want || strings.Contains(want, n) {
			// Require a real person token (avoid tiny substring false positives).
			if n == want || len(strings.Fields(n)) >= 2 || len(n) >= 8 {
				return true
			}
		}
	}
	return false
}

func durationWithinTolerance(fileSec float64, runtimeMin, tolPct int) bool {
	if runtimeMin <= 0 || fileSec <= 0 {
		return false
	}
	want := float64(runtimeMin) * 60
	diff := math.Abs(fileSec - want)
	return diff <= want*float64(tolPct)/100
}

// movieCandidateMeta loads year/runtime/cast for corroboration. When the file
// has a year or duration signal, MovieDetails is fetched so an empty search
// result release_date does not falsely fail the year gate.
func movieCandidateMeta(ctx context.Context, client *tmdb.Client, id int, searchReleaseDate string, sig FileSignals) (candYear, runtimeMin int, cast []string) {
	candYear = yearFromReleaseDate(searchReleaseDate)
	if client == nil {
		return candYear, 0, nil
	}
	if sig.Year != 0 || sig.DurationSec > 0 {
		if det, err := client.MovieDetails(ctx, id); err == nil {
			if y := yearFromReleaseDate(det.ReleaseDate); y != 0 {
				candYear = y
			}
			runtimeMin = det.Runtime
		}
	}
	if sig.Actor != "" {
		if names, err := client.MovieCredits(ctx, id); err == nil {
			cast = names
		}
	}
	return candYear, runtimeMin, cast
}

func seriesCandidateMeta(
	ctx context.Context, client *tmdb.Client,
	id, season, episode int, searchReleaseDate string, sig FileSignals,
) (candYear, runtimeMin int, cast []string) {
	candYear = yearFromReleaseDate(searchReleaseDate)
	if client == nil {
		return candYear, 0, nil
	}
	if sig.DurationSec > 0 {
		eps, err := client.SeasonDetails(ctx, id, season)
		if err == nil {
			for _, ep := range eps {
				if ep.EpisodeNumber == episode {
					runtimeMin = ep.Runtime
					break
				}
			}
		}
	}
	// TVDetails has no first-air field today; when year is needed and search
	// omitted it, leave candYear as 0 so SignalsPass fails that candidate
	// rather than inventing a year — search first_air_date usually present.
	if sig.Actor != "" {
		if names, err := client.TVAggregateCredits(ctx, id); err == nil {
			cast = names
		}
	}
	return candYear, runtimeMin, cast
}

func pickLimit(n, total int) int {
	if total < n {
		return total
	}
	return n
}
