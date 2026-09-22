package release

import (
	"regexp"
	"strings"
)

// Claude 2026-09-22: configurable grab language include list (Global setting).
// Reason: hard-coded English-only exclude missed autograb; German NZBs imported.
// Troubleshooting: Settings → Advanced → Global → Preferred grab languages.
// Review if: indexer APIs expose structured audio language (drop title tokens).
// Related: api/grablanguage.go; FilterReleases; RunAutoGrab.

// knownLanguageTags are word-boundary tokens scene releases use for audio/sub
// language. Unmarked titles (no tag) are treated as the English-assumed default.
// MULTI is deliberately absent — it means bundled tracks, often including English.
var knownLanguageTags = []string{
	"english", "eng",
	"french", "german", "spanish", "italian", "vostfr",
	"russian", "hindi", "korean", "japanese",
	"dutch", "polish", "swedish", "norwegian", "danish",
	"portuguese", "brazilian", "latino", "castellano",
	"chinese", "mandarin", "cantonese", "arabic", "turkish",
	"hebrew", "thai", "vietnamese", "nordic",
}

var knownLanguagePattern = regexp.MustCompile(`(?i)\b(` + strings.Join(knownLanguageTags, "|") + `)\b`)

// FindLanguageTags returns lowercased known language tokens present in title
// (deduped, scene order not preserved).
func FindLanguageTags(title string) []string {
	matches := knownLanguagePattern.FindAllString(title, -1)
	if len(matches) == 0 {
		return nil
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		k := strings.ToLower(m)
		if _, ok := seen[k]; ok {
			continue
		}
		seen[k] = struct{}{}
		out = append(out, k)
	}
	return out
}

// TitleLanguageAllowed reports whether a release title is acceptable for grabs
// given the operator's preferred include list.
//
// Rules (Global preferred-languages setting):
//   - No known language tag → allow (unmarked = English-assumed default).
//   - Any preferred tag appears in the title → allow.
//   - Title has only non-preferred language tags → reject.
//
// Empty preferred keeps the historical English-assumed behaviour: allow
// unmarked and english/eng tags; reject every other known language tag.
func TitleLanguageAllowed(title string, preferred []string) bool {
	tags := FindLanguageTags(title)
	if len(tags) == 0 {
		return true
	}
	pref := normalizePreferredLanguages(preferred)
	if len(pref) == 0 {
		for _, t := range tags {
			if t != "english" && t != "eng" {
				return false
			}
		}
		return true
	}
	for _, t := range tags {
		if _, ok := pref[t]; ok {
			return true
		}
	}
	return false
}

func normalizePreferredLanguages(in []string) map[string]struct{} {
	out := map[string]struct{}{}
	for _, p := range in {
		p = strings.ToLower(strings.TrimSpace(p))
		if p == "" {
			continue
		}
		out[p] = struct{}{}
	}
	return out
}
