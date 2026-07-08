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
	"regexp"
	"strings"
)

// noiseTokens are common release-scene tags that don't belong in a title
// search — resolution/source/codec/audio/edition markers. Matched as whole
// words, case-insensitively.
var noiseTokens = []string{
	"1080p", "720p", "2160p", "480p", "4k", "uhd",
	"web-dl", "webdl", "webrip", "web", "bluray", "blu-ray", "brrip", "bdrip",
	"hdtv", "dvdrip", "remux",
	"x264", "x265", "h264", "h265", "hevc", "avc", "av1",
	"aac", "dts", "dts-hd", "ddp5", "atmos", "truehd",
	"proper", "repack", "extended", "unrated", "theatrical", "limited",
	"multi", "hdr", "hdr10", "10bit", "internal", "uncut",
}

var (
	noiseTokenRe   = regexp.MustCompile(`(?i)\b(` + strings.Join(noiseTokens, "|") + `)\b`)
	releaseGroupRe = regexp.MustCompile(`-[A-Za-z0-9]+$`)
	multiSpaceRe   = regexp.MustCompile(`\s{2,}`)
	bracketedRe    = regexp.MustCompile(`[\[\(][^\[\]\(\)]*[\]\)]`)
)

// FromName derives a search term from a raw orphaned file/folder name (no
// extension — strip that first if present).
func FromName(name string) string {
	s := strings.ReplaceAll(name, ".", " ")
	s = strings.ReplaceAll(s, "_", " ")

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
	s = multiSpaceRe.ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
}
