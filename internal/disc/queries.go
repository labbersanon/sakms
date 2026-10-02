package disc

// Claude 2026-10-02: SearchQueries strips volume/disc edition tokens.
// Reason: TMDB identify after ISO pick; labels like V5_D1 are not the title.
// Troubleshooting: empty hits — filename stem is also queried.
// Review if: Phase 5 episode naming uses the same cleaner.

import (
	"path/filepath"
	"regexp"
	"strings"
)

var discEditionRe = regexp.MustCompile(`(?i)\b(?:volume|vol|disc|disk|dvd|v|d)\s*\d+\b`)

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
