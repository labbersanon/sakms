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

// LanguageGroup is one dropdown choice: selecting ID matches every Alias token
// in a release title (abbreviations are implied, not separate options).
type LanguageGroup struct {
	ID      string   // canonical value stored + shown in Settings
	Aliases []string // scene tokens that count as this language (includes ID)
}

// LanguageGroups is the Settings dropdown catalog. Order is display order.
// MULTI is deliberately absent — it means bundled tracks, often including English.
var LanguageGroups = []LanguageGroup{
	{ID: "english", Aliases: []string{"english", "eng"}},
	{ID: "french", Aliases: []string{"french"}},
	{ID: "vostfr", Aliases: []string{"vostfr"}},
	{ID: "german", Aliases: []string{"german"}},
	{ID: "spanish", Aliases: []string{"spanish", "latino", "castellano"}},
	{ID: "italian", Aliases: []string{"italian"}},
	{ID: "russian", Aliases: []string{"russian"}},
	{ID: "hindi", Aliases: []string{"hindi"}},
	{ID: "korean", Aliases: []string{"korean"}},
	{ID: "japanese", Aliases: []string{"japanese"}},
	{ID: "dutch", Aliases: []string{"dutch"}},
	{ID: "polish", Aliases: []string{"polish"}},
	{ID: "swedish", Aliases: []string{"swedish"}},
	{ID: "norwegian", Aliases: []string{"norwegian"}},
	{ID: "danish", Aliases: []string{"danish"}},
	{ID: "portuguese", Aliases: []string{"portuguese", "brazilian"}},
	{ID: "chinese", Aliases: []string{"chinese", "mandarin", "cantonese"}},
	{ID: "arabic", Aliases: []string{"arabic"}},
	{ID: "turkish", Aliases: []string{"turkish"}},
	{ID: "hebrew", Aliases: []string{"hebrew"}},
	{ID: "thai", Aliases: []string{"thai"}},
	{ID: "vietnamese", Aliases: []string{"vietnamese"}},
	{ID: "nordic", Aliases: []string{"nordic"}},
}

// KnownLanguageTags is the flat token list used for title matching (all aliases).
// Prefer LanguageGroups / LanguageOptionIDs for UI.
var KnownLanguageTags = flattenLanguageAliases()

// LanguageOptionIDs returns canonical dropdown values (no bare abbreviations).
func LanguageOptionIDs() []string {
	out := make([]string, len(LanguageGroups))
	for i, g := range LanguageGroups {
		out[i] = g.ID
	}
	return out
}

func flattenLanguageAliases() []string {
	var out []string
	seen := map[string]struct{}{}
	for _, g := range LanguageGroups {
		for _, a := range g.Aliases {
			a = strings.ToLower(a)
			if _, ok := seen[a]; ok {
				continue
			}
			seen[a] = struct{}{}
			out = append(out, a)
		}
	}
	return out
}

var (
	knownLanguagePattern = regexp.MustCompile(`(?i)\b(` + strings.Join(KnownLanguageTags, "|") + `)\b`)
	aliasToGroupID       = buildAliasToGroupID()
	englishAliases       = aliasSetFor("english")
)

func buildAliasToGroupID() map[string]string {
	m := map[string]string{}
	for _, g := range LanguageGroups {
		for _, a := range g.Aliases {
			m[strings.ToLower(a)] = g.ID
		}
	}
	return m
}

func aliasSetFor(id string) map[string]struct{} {
	out := map[string]struct{}{}
	for _, g := range LanguageGroups {
		if g.ID != id {
			continue
		}
		for _, a := range g.Aliases {
			out[strings.ToLower(a)] = struct{}{}
		}
		break
	}
	return out
}

// CanonicalLanguageID maps a stored or typed token to its group ID.
// Unknown tokens return "".
func CanonicalLanguageID(token string) string {
	return aliasToGroupID[strings.ToLower(strings.TrimSpace(token))]
}

// ExpandLanguagePreferences turns selected group IDs (or legacy aliases) into
// the full set of scene tokens that should match a release title.
func ExpandLanguagePreferences(preferred []string) map[string]struct{} {
	out := map[string]struct{}{}
	for _, p := range preferred {
		id := CanonicalLanguageID(p)
		if id == "" {
			continue
		}
		for _, g := range LanguageGroups {
			if g.ID != id {
				continue
			}
			for _, a := range g.Aliases {
				out[strings.ToLower(a)] = struct{}{}
			}
			break
		}
	}
	return out
}

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
//   - Any preferred language's aliases appear in the title → allow
//     (e.g. selecting "english" also matches ENG).
//   - Title has only non-preferred language tags → reject.
//
// Empty preferred keeps the historical English-assumed behaviour: allow
// unmarked and english-group tags; reject every other known language tag.
func TitleLanguageAllowed(title string, preferred []string) bool {
	tags := FindLanguageTags(title)
	if len(tags) == 0 {
		return true
	}
	pref := ExpandLanguagePreferences(preferred)
	if len(pref) == 0 {
		for _, t := range tags {
			if _, ok := englishAliases[t]; !ok {
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
