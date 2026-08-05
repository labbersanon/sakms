// Package searchterm turns a messy orphaned-file/folder name (release-scene
// naming, hash-named extraction artifacts, plain clean names) into a
// reasonable search term for Sonarr/Radarr's own TVDB/TMDB lookup proxy.
//
// This is deliberately a best-effort heuristic, not a full parser — Sonarr/
// Radarr's own internal filename parser isn't exposed via their API, so this
// tool can't reuse it directly. Names this doesn't clean up well (e.g. an
// opaque hash-named folder, or a title with no recognizable noise to strip)
// are expected to fall through to manual review instead.
package searchterm

import (
	"path/filepath"
	"regexp"
	"strings"

	"github.com/labbersanon/sakms/internal/config"
)

// noiseTokens are common release-scene tags that don't belong in a title
// search — resolution/source/codec/audio/edition markers. Matched as whole
// words, case-insensitively.
var noiseTokens = []string{
	"1080p", "720p", "2160p", "480p", "4k", "uhd", "hd",
	"web-dl", "webdl", "webrip", "web", "bluray", "blu-ray", "brrip", "bdrip",
	"hdtv", "dvdrip", "remux",
	"x264", "x265", "h264", "h265", "hevc", "avc", "av1",
	"aac", "dts", "dts-hd", "ddp5", "atmos", "truehd",
	"proper", "repack", "extended", "unrated", "theatrical", "limited",
	"multi", "hdr", "hdr10", "10bit", "internal", "uncut", "sample",
}

var (
	noiseTokenRe = regexp.MustCompile(`(?i)\b(` + strings.Join(noiseTokens, "|") + `)\b`)
	// releaseGroupRe only matches compact -GROUP tags (no spaces), e.g. -spamTV.
	releaseGroupRe = regexp.MustCompile(`-[A-Za-z0-9]+$`)
	multiSpaceRe   = regexp.MustCompile(`\s{2,}`)
	bracketedRe    = regexp.MustCompile(`[\[\(][^\[\]\(\)]*[\]\)]`)
	// dottedCodecRe collapses H.265 / x.264 before dots become spaces.
	dottedCodecRe = regexp.MustCompile(`(?i)\b([xh])\.(26[45])\b`)
	// yearBracketRe turns a year-only [1968] into (1968) so it survives as a year.
	yearBracketRe = regexp.MustCompile(`\[(\d{4})\]`)
	// leadingIndexRe drops zero-padded playlist prefixes like "01 " / "02 " —
	// not bare numbers (would mangle "9 11 …" / "12 Angry Men").
	leadingIndexRe = regexp.MustCompile(`(?i)^0\d\s+`)
	// creditBeforeYearRe drops " - credit/actor …" between title and (year).
	// Claude 2026-08-05: "Which Way Is Up - Richard Pryor (1977)" → title+(year)
	// Reason: TMDB returns zero hits for actor-padded terms after ext strip alone
	// Troubleshooting: unmatched no TMDB match for "… - Richard Pryor (1977)"
	// Review if: a real title containing " - " before a year is over-stripped
	creditBeforeYearRe = regexp.MustCompile(`(?i)^(.+?)\s*-\s+.+\((\d{4})\)\s*$`)
	cqNoiseRe          = regexp.MustCompile(`(?i)\bcq\d+\b`)
	trailingJunkRe     = regexp.MustCompile(`[\s\-]+$`)
)

// FromName derives a search term from a raw orphaned file/folder name.
//
// Claude 2026-08-05: strip config.VideoExts before dot→space cleaning
// Reason: Rename passed filenames with .mp4; dots became spaces → TMDB query ended in "mp4"
// Troubleshooting: unmatched "no TMDB match for … mp4" — ensure IsVideoExt covers the extension
// Review if: callers strip extensions themselves and FromName should stop
func FromName(name string) string {
	if ext := filepath.Ext(name); config.IsVideoExt(ext) {
		name = strings.TrimSuffix(name, ext)
	}

	// Normalize dotted codecs before "." → " " so "H.265" stays one noise token.
	name = dottedCodecRe.ReplaceAllString(name, "${1}${2}")
	name = yearBracketRe.ReplaceAllString(name, "($1)")

	s := strings.ReplaceAll(name, ".", " ")
	s = strings.ReplaceAll(s, "_", " ")
	s = leadingIndexRe.ReplaceAllString(s, "")
	if m := creditBeforeYearRe.FindStringSubmatch(s); m != nil {
		s = m[1] + " (" + m[2] + ")"
	}

	// Strip a trailing "-RELEASEGROUP" tag before removing bracketed/paren
	// content, since a release group can itself look bracket-free.
	s = releaseGroupRe.ReplaceAllString(s, "")

	// Strip bracketed/parenthesized quality tags like [1080p] or (WEBRip) —
	// but only when they contain recognizable noise, not e.g. a genuine
	// "(2001)" year, which is left alone since it's useful for search.
	s = bracketedRe.ReplaceAllStringFunc(s, func(m string) string {
		if noiseTokenRe.MatchString(m) {
			return " "
		}
		return m
	})

	s = noiseTokenRe.ReplaceAllString(s, " ")
	s = cqNoiseRe.ReplaceAllString(s, " ")
	s = multiSpaceRe.ReplaceAllString(s, " ")
	s = trailingJunkRe.ReplaceAllString(s, "")
	return strings.TrimSpace(s)
}

// IsSampleVideo reports filenames that are release "sample" clips, not the
// main feature — Rename/Dedup should not propose them as library orphans.
func IsSampleVideo(name string) bool {
	base := strings.ToLower(filepath.Base(name))
	stem := strings.TrimSuffix(base, filepath.Ext(base))
	if strings.Contains(base, ".sample.") || strings.Contains(base, "-sample.") {
		return true
	}
	return strings.HasSuffix(stem, "-sample") || strings.HasSuffix(stem, ".sample") || stem == "sample"
}

var (
	yearParenStripRe = regexp.MustCompile(`\s*\((19\d{2}|20\d{2})\)`)
	yearBareEndRe    = regexp.MustCompile(`(?i)\s+\b(19\d{2}|20\d{2})\b\s*$`)
	commaPersonRe    = regexp.MustCompile(`(?i)^(.+?)\s+([^,]+),\s*([^,]+)$`)
)

// StripYear removes parenthesized and trailing bare years from a cleaned term
// so TMDB search is title-only; year stays available via rename.FileSignals.
func StripYear(term string) string {
	s := yearParenStripRe.ReplaceAllString(term, " ")
	s = yearBareEndRe.ReplaceAllString(s, "")
	s = multiSpaceRe.ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
}

// SearchQueries returns distinct TMDB/TVDB query strings derived from a raw
// orphan name, ordered best-first. Year is stripped from queries (corroborated
// separately). Later variants try comma-inverted person names and shorter titles.
//
// Claude 2026-08-05: iterate filename-derived queries — year-in-query returned 0 TMDB hits
// Reason: "Minority Report (2002)" / "Which Way Is Up (1977)" often match nothing; bare title works
// Troubleshooting: unmatched "no TMDB match for Title (year)" — use SearchQueries not FromName alone
// Review if: TMDB starts preferring year-in-query and bare title becomes worse
func SearchQueries(name string) []string {
	base := FromName(name)
	var out []string
	add := func(s string) {
		s = strings.TrimSpace(s)
		if s == "" {
			return
		}
		for _, e := range out {
			if strings.EqualFold(e, s) {
				return
			}
		}
		out = append(out, s)
	}

	stripped := StripYear(base)
	add(stripped)
	add(base) // last-resort with year still in string

	// "Copacabana Marx, Groucho" → bare "Copacabana" (TMDB finds the 1947 film)
	// and "Copacabana Groucho Marx" (not bare "Groucho Marx", which matches the person).
	if m := commaPersonRe.FindStringSubmatch(stripped); m != nil {
		add(strings.TrimSpace(m[1]))
		add(strings.TrimSpace(m[1] + " " + m[3] + " " + m[2]))
	}

	// Drop trailing " - leftover" fragments after codec strip left a dash.
	if i := strings.LastIndex(stripped, " - "); i > 0 {
		add(strings.TrimSpace(stripped[:i]))
	}

	// Do not truncate long titles by word count — "Austin Powers International Man of"
	// / three-word shortenings matched the wrong TMDB hits after the primary query failed
	// corroboration on missing metadata.
	return out
}

