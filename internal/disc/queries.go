package disc

// Claude 2026-10-02: SearchQueries strips volume/disc edition tokens.
// Reason: TMDB identify after ISO pick; labels like V5_D1 are not the title.
// Troubleshooting: empty hits — filename stem is also queried.
// Review if: Phase 5 episode naming uses the same cleaner.
//
// Claude 2026-10-02: WikiQuery keeps Volume N and drops Disc N.
// Reason: Wikipedia set pages are "Golden Collection: Volume 5", not the show
//   name TMDB wants; Disc N is a section on that page, not the search string.
// Troubleshooting: V5_D1 must become "… Volume 5" with DiscNumber=1.
// Review if: a disc DB starts returning per-title names (OVID does not).

import (
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

var (
	discEditionRe = regexp.MustCompile(`(?i)\b(?:volume|vol|disc|disk|dvd|v|d)\s*\d+\b`)
	discOnlyRe    = regexp.MustCompile(`(?i)\b(?:disc|disk|d)\s*\d+\b`)
	discNumRe     = regexp.MustCompile(`(?i)\b(?:disc|disk|d)\s*(\d+)\b`)
	volumeNumRe   = regexp.MustCompile(`(?i)\b(?:volume|vol|v)\s*(\d+)\b`)
)

// SearchQueries builds TMDB lookup strings from a volume label and filename.
func SearchQueries(volume, src string) []string {
	stem := strings.TrimSuffix(filepath.Base(src), filepath.Ext(src))
	seen := map[string]bool{}
	var out []string
	add := func(raw string) {
		q := strings.Join(strings.Fields(strings.ReplaceAll(raw, "_", " ")), " ")
		if q == "" {
			return
		}
		key := strings.ToLower(q)
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, q)
	}
	add(volume)
	add(stem)
	// Underscore is a word character, so strip edition tokens after
	// GOLDEN_V5_D1 becomes "GOLDEN V5 D1".
	spacedVol := strings.ReplaceAll(volume, "_", " ")
	spacedStem := strings.ReplaceAll(stem, "_", " ")
	add(discEditionRe.ReplaceAllString(spacedVol, " "))
	add(discEditionRe.ReplaceAllString(spacedStem, " "))
	return out
}

// WikiQuery is the Wikipedia search string: keep Volume N, drop Disc N.
func WikiQuery(volume, src string) string {
	raw := spaced(volume)
	if raw == "" {
		raw = spaced(strings.TrimSuffix(filepath.Base(src), filepath.Ext(src)))
	}
	raw = discOnlyRe.ReplaceAllString(raw, " ")
	raw = volumeNumRe.ReplaceAllString(raw, "Volume $1")
	return strings.Join(strings.Fields(raw), " ")
}

// DiscNumber is the 1-based disc index from the volume label or filename.
func DiscNumber(volume, src string) int {
	s := spaced(volume) + " " + spaced(strings.TrimSuffix(filepath.Base(src), filepath.Ext(src)))
	n := 1
	for _, m := range discNumRe.FindAllStringSubmatch(s, -1) {
		v, err := strconv.Atoi(m[1])
		if err == nil && v > 0 {
			n = v
		}
	}
	return n
}

func spaced(s string) string {
	return strings.Join(strings.Fields(strings.ReplaceAll(s, "_", " ")), " ")
}
