package newznab

import (
	"regexp"
	"sort"
	"strings"
	"unicode"
)

// Claude 2026-10-22: NZBmegasearch smartsearch, in Go, for grab t=search.
// Reason: indexer RSS is noisy; they kept hits whose sanitized title tokens
//
//	cover the query (prefix-relax for 1080 vs 1080p), hid the rest, collapsed
//	same title within 5MB, and ranked an exact PreDB title first. We do not
//	scrape predb.me: exact means sanitized title == sanitized query. Do not
//	fetch NZBs to count PAR2.
//
// Troubleshooting: grab empty after a title that should match → sanitize or
//
//	prefix-relax dropped it; this falls back to the unfiltered list (still
//	deduped). Review if: Prowlarr already applies the same token gate.
const grabDupSizeSlack int64 = 5_000_000

var grabLogicTerm = regexp.MustCompile(`(?:^|\s)([-+])(\w+)`)

// FilterGrabReleases keeps query-matching grab hits, prefers an exact
// sanitized title, and drops same-title duplicates within 5MB.
func FilterGrabReleases(query string, rels []Release) []Release {
	if len(rels) == 0 {
		return rels
	}
	inc, exc, core := parseGrabLogic(query)
	qTok := tokenSet(sanitizeGrabQuery(core))
	items := make([]scoredGrab, 0, len(rels))
	kept := 0
	for _, r := range rels {
		title := sanitizeGrabQuery(r.Title)
		tTok := tokenSet(title)
		if !grabLogicOK(tTok, inc, exc) {
			items = append(items, scoredGrab{rel: r})
			continue
		}
		if len(qTok) > 0 && !titleMatchesQuery(qTok, tTok) {
			items = append(items, scoredGrab{rel: r})
			continue
		}
		exact := title != "" && title == sanitizeGrabQuery(core)
		items = append(items, scoredGrab{rel: r, exact: exact, keep: true})
		kept++
	}
	use := items
	if kept > 0 {
		filtered := make([]scoredGrab, 0, kept)
		for _, it := range items {
			if it.keep {
				filtered = append(filtered, it)
			}
		}
		use = filtered
	}
	sort.SliceStable(use, func(i, j int) bool {
		if use[i].exact != use[j].exact {
			return use[i].exact
		}
		return false
	})
	return dedupGrab(use)
}

func parseGrabLogic(query string) (include, exclude map[string]bool, core string) {
	include = map[string]bool{}
	exclude = map[string]bool{}
	for _, m := range grabLogicTerm.FindAllStringSubmatch(query, -1) {
		tok := sanitizeGrabQuery(m[2])
		if tok == "" {
			continue
		}
		if m[1] == "-" {
			exclude[tok] = true
		} else {
			include[tok] = true
		}
	}
	core = grabLogicTerm.ReplaceAllString(query, " ")
	return include, exclude, core
}

func grabLogicOK(title map[string]bool, include, exclude map[string]bool) bool {
	for tok := range exclude {
		if titleHasTerm(title, tok) {
			return false
		}
	}
	if len(include) == 0 {
		return true
	}
	for tok := range include {
		if !titleHasTerm(title, tok) {
			return false
		}
	}
	return true
}

func titleHasTerm(title map[string]bool, tok string) bool {
	if title[tok] {
		return true
	}
	for t := range title {
		if strings.HasPrefix(t, tok) || strings.HasPrefix(tok, t) {
			return true
		}
	}
	return false
}

func titleMatchesQuery(qTok, tTok map[string]bool) bool {
	if len(qTok) == 0 {
		return true
	}
	unmatched := 0
	for tok := range qTok {
		if tTok[tok] {
			continue
		}
		hit := false
		for t := range tTok {
			if strings.HasPrefix(t, tok) {
				hit = true
				break
			}
		}
		if !hit {
			unmatched++
		}
	}
	return unmatched == 0
}

func sanitizeGrabQuery(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		if unicode.Is(unicode.Mn, r) {
			continue
		}
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			continue
		}
		b.WriteByte(' ')
	}
	return strings.Join(strings.Fields(b.String()), ".")
}

func tokenSet(s string) map[string]bool {
	out := map[string]bool{}
	if s == "" {
		return out
	}
	for _, t := range strings.Split(s, ".") {
		if t != "" {
			out[t] = true
		}
	}
	return out
}

func dedupGrab(items []scoredGrab) []Release {
	out := make([]Release, 0, len(items))
	for _, it := range items {
		dup := false
		st := sanitizeGrabQuery(it.rel.Title)
		for _, have := range out {
			if sanitizeGrabQuery(have.Title) != st {
				continue
			}
			d := have.Size - it.rel.Size
			if d < 0 {
				d = -d
			}
			if d < grabDupSizeSlack {
				dup = true
				break
			}
		}
		if !dup {
			out = append(out, it.rel)
		}
	}
	return out
}

type scoredGrab struct {
	rel   Release
	exact bool
	keep  bool
}
