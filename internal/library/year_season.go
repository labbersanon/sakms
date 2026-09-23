package library

import (
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Claude 2026-09-23: year-as-season for theatrical shorts, catalog/rename only.
// Reason: TVDB/Plex shorts libraries use Season 1929 / S1929E01. SxxExx with
//   two-digit seasons was already the scene default by 2000; years 2000+ stay
//   on sequential S01E01. Kept OUT of ParseEpisodeNumbers so import and
//   releasematch never treat S1958E14 as season 19.
// Troubleshooting: a 1958 Looney Tunes file stays unparsed — check the
//   1928–1999 window and that Dedup is not the caller.
// Review if: a post-1999 shorts series is filed as year-seasons on purpose.
const (
	YearSeasonMin = 1928
	YearSeasonMax = 1999
)

var (
	yearSeasonPattern     = regexp.MustCompile(`(?i)S(\d{4})E(\d{1,3})`)
	yearAltSeasonPattern  = regexp.MustCompile(`(?i)\b(\d{4})x(\d{1,3})\b`)
	yearSeasonFolderRe    = regexp.MustCompile(`(?i)^(?:season[ ._-]*)?(\d{4})$`)
	skippableNestFolderRe = regexp.MustCompile(`(?i)^(?:(?:disc|disk|cd|dvd)[ ._-]*\d+|uncompressed|video|extras)$`)
	leadingEpisodeRe      = regexp.MustCompile(`(?i)^(?:e|episode[ ._-]*)?(\d{1,3})(?:$|[\s._-])`)
)

// IsYearSeason reports whether n is a pre-SxxExx-standard calendar year used
// as a season number (theatrical shorts), not a sequential season index.
func IsYearSeason(n int) bool {
	return n >= YearSeasonMin && n <= YearSeasonMax
}

// ParseYearSeasonNumbers extracts S1958E14 / 1958x14 (and ranges) when the
// season is inside YearSeasonMin..YearSeasonMax. ok is false for S2000E01.
func ParseYearSeasonNumbers(name string) (season int, episodes []int, ok bool) {
	if loc := yearSeasonPattern.FindStringSubmatchIndex(name); loc != nil {
		season, _ = strconv.Atoi(name[loc[2]:loc[3]])
		if !IsYearSeason(season) {
			return 0, nil, false
		}
		first, _ := strconv.Atoi(name[loc[4]:loc[5]])
		rest := name[loc[1]:]
		return yearSeasonRest(season, first, rest, true)
	}
	if loc := yearAltSeasonPattern.FindStringSubmatchIndex(name); loc != nil {
		season, _ = strconv.Atoi(name[loc[2]:loc[3]])
		if !IsYearSeason(season) {
			return 0, nil, false
		}
		first, _ := strconv.Atoi(name[loc[4]:loc[5]])
		rest := name[loc[1]:]
		return yearSeasonRest(season, first, rest, false)
	}
	return 0, nil, false
}

func yearSeasonRest(season, first int, rest string, sxx bool) (int, []int, bool) {
	if sxx {
		switch {
		case concatPrefixPattern.MatchString(rest):
			prefix := concatPrefixPattern.FindString(rest)
			nums := []int{first}
			for _, m := range concatNumPattern.FindAllStringSubmatch(prefix, -1) {
				n, _ := strconv.Atoi(m[1])
				nums = append(nums, n)
			}
			return season, dedupSorted(nums), true
		case rangeSuffixPattern.MatchString(rest):
			m := rangeSuffixPattern.FindStringSubmatch(rest)
			last, _ := strconv.Atoi(m[1])
			return season, expandRange(first, last), true
		}
	} else if altRangeSuffixPattern.MatchString(rest) {
		m := altRangeSuffixPattern.FindStringSubmatch(rest)
		last, _ := strconv.Atoi(m[1])
		return season, expandRange(first, last), true
	}
	return season, []int{first}, true
}

// YearSeasonFolder reports a shorts year folder ("1958", "Season 1958").
func YearSeasonFolder(name string) (year int, ok bool) {
	m := yearSeasonFolderRe.FindStringSubmatch(strings.TrimSpace(name))
	if m == nil {
		return 0, false
	}
	year, _ = strconv.Atoi(m[1])
	if !IsYearSeason(year) {
		return 0, false
	}
	return year, true
}

// StripYearSeasonMarker removes the first S1958E14 / 1958x14 token (and
// everything after it) so the leftover is the show title.
func StripYearSeasonMarker(name string) string {
	if loc := yearSeasonPattern.FindStringIndex(name); loc != nil {
		return trimSeparators(name[:loc[0]])
	}
	if loc := yearAltSeasonPattern.FindStringIndex(name); loc != nil {
		return trimSeparators(name[:loc[0]])
	}
	return name
}

// ParseEpisodeNumbersNested is the catalog/rename walker for series-style
// shorts trees: Show / 1958 / Disc 1 / E14 Title.mkv. Regular SxxExx still
// wins first. Does not walk SxxExx out of a grandparent (The Path /subs case).
func ParseEpisodeNumbersNested(videoPath, root string) (season int, episodes []int, ok bool) {
	base := filepath.Base(videoPath)
	parent := filepath.Dir(videoPath)
	if season, episodes, ok = ParseEpisodeNumbersLoose(base, parent); ok {
		return season, episodes, true
	}
	if root == "" {
		return 0, nil, false
	}
	year := 0
	dir := parent
	root = filepath.Clean(root)
	for dir != "" && dir != "." && dir != root {
		name := filepath.Base(dir)
		if y, yok := YearSeasonFolder(name); yok {
			year = y
			break
		}
		if !isSkippableNest(name) {
			break
		}
		next := filepath.Dir(dir)
		if next == dir {
			break
		}
		dir = next
	}
	if year == 0 {
		return 0, nil, false
	}
	ep, eok := parseLeadingEpisode(base)
	if !eok {
		return 0, nil, false
	}
	return year, []int{ep}, true
}

func isSkippableNest(name string) bool {
	return skippableNestFolderRe.MatchString(strings.TrimSpace(name))
}

func parseLeadingEpisode(name string) (int, bool) {
	name = strings.TrimSpace(name)
	m := leadingEpisodeRe.FindStringSubmatch(name)
	if m == nil {
		return 0, false
	}
	n, err := strconv.Atoi(m[1])
	if err != nil || n <= 0 {
		return 0, false
	}
	switch n {
	case 480, 720, 1080, 2160:
		return 0, false
	}
	return n, true
}
