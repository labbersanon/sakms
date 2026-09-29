package library

import (
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Claude 2026-09-28: daily air-date + anime absolute parsers.
// Reason: Sonarr daily/anime names have no SxxExx. Kept OUT of
//   ParseEpisodeNumbers so releasematch / dedup / autograb stay unchanged
//   until they opt in. Rename and grab-import call these after SxxExx fails.
// Troubleshooting: a date-named daily or "- 1089" anime stays unmatched —
//   confirm this parser returned ok and the TMDB slot resolve found one row.
// Review if: ParseEpisodeNumbers itself learns these shapes.

var airDatePattern = regexp.MustCompile(`(?i)\b((?:19|20)\d{2})[.\- ](\d{1,2})[.\- ](\d{1,2})\b`)

// absMarkedPattern is " - 12", "ep12", "e12", "episode 12", "#12".
var absMarkedPattern = regexp.MustCompile(`(?i)(?:[\s._]-\s*|\bep(?:isode)?[\s._#-]*|(?:^|[\s._])e[\s._-]?|#)(\d{1,4})\b`)

// absDottedPattern is "Show.1089.1080p" — 3–4 digits only, so "Show.12.mkv"
// does not claim episode 12 without a dash/ep marker.
var absDottedPattern = regexp.MustCompile(`(?i)[\s._](\d{3,4})[\s._]`)

func absoluteBlocked(n int) bool {
	switch n {
	case 264, 265, 480, 576, 720, 1080, 1440, 2160, 4320:
		return true
	}
	return n >= 1900 && n <= 2099
}

// ParseEpisodeAirDate extracts a calendar date (YYYY-MM-DD) from a daily-style
// release name. ok is false when no full date is present.
func ParseEpisodeAirDate(name string) (date string, ok bool) {
	m := airDatePattern.FindStringSubmatch(name)
	if m == nil {
		return "", false
	}
	year, _ := strconv.Atoi(m[1])
	month, _ := strconv.Atoi(m[2])
	day, _ := strconv.Atoi(m[3])
	if month < 1 || month > 12 || day < 1 || day > 31 {
		return "", false
	}
	return sprintfDate(year, month, day), true
}

func sprintfDate(year, month, day int) string {
	return strings.Join([]string{
		itoaPad(year, 4),
		itoaPad(month, 2),
		itoaPad(day, 2),
	}, "-")
}

func itoaPad(n, width int) string {
	s := strconv.Itoa(n)
	for len(s) < width {
		s = "0" + s
	}
	return s
}

// ParseEpisodeAirDateLoose tries basename, then the parent directory name.
func ParseEpisodeAirDateLoose(basename, parentDir string) (date string, ok bool) {
	if date, ok = ParseEpisodeAirDate(basename); ok {
		return date, true
	}
	return ParseEpisodeAirDate(filepath.Base(parentDir))
}

// ParseAbsoluteEpisode extracts an anime-style absolute episode number.
// SxxExx and air-date names return ok=false so those parsers stay first.
func ParseAbsoluteEpisode(name string) (n int, ok bool) {
	if _, _, has := ParseEpisodeNumbers(name); has {
		return 0, false
	}
	if _, has := ParseEpisodeAirDate(name); has {
		return 0, false
	}
	if m := absMarkedPattern.FindStringSubmatch(name); m != nil {
		v, _ := strconv.Atoi(m[1])
		if v > 0 && !absoluteBlocked(v) {
			return v, true
		}
	}
	for _, m := range absDottedPattern.FindAllStringSubmatch(name, -1) {
		v, _ := strconv.Atoi(m[1])
		if v > 0 && !absoluteBlocked(v) {
			return v, true
		}
	}
	return 0, false
}

// ParseAbsoluteEpisodeLoose tries basename, then the parent directory name.
func ParseAbsoluteEpisodeLoose(basename, parentDir string) (n int, ok bool) {
	if n, ok = ParseAbsoluteEpisode(basename); ok {
		return n, true
	}
	return ParseAbsoluteEpisode(filepath.Base(parentDir))
}

// StripDailyOrAbsolute removes the first air-date or absolute-episode token
// (and everything after a date; only the number for absolute) so the remainder
// can seed a show-title search.
func StripDailyOrAbsolute(name string) string {
	if loc := airDatePattern.FindStringIndex(name); loc != nil {
		return trimSeparators(name[:loc[0]])
	}
	if _, _, has := ParseEpisodeNumbers(name); has {
		return name
	}
	if loc := absMarkedPattern.FindStringIndex(name); loc != nil {
		return trimSeparators(name[:loc[0]])
	}
	if loc := absDottedPattern.FindStringIndex(name); loc != nil {
		return trimSeparators(name[:loc[0]])
	}
	return name
}
