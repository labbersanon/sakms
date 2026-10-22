package newznab

import (
	"regexp"
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
	coreSan := sanitizeGrabQuery(core)
	qTok := tokenSet(coreSan)
	var exact, rest, fallback []Release
	for _, r := range rels {
		title := sanitizeGrabQuery(r.Title)
		tTok := tokenSet(title)
		if !grabLogicOK(tTok, inc, exc) || (len(qTok) > 0 && !titleMatchesQuery(qTok, tTok)) {
			fallback = append(fallback, r)
			continue
		}
		if title != "" && title == coreSan {
			exact = append(exact, r)
		} else {
			rest = append(rest, r)
		}
	}
	if len(exact)+len(rest) == 0 {
		return dedupGrab(fallback)
	}
	return dedupGrab(append(exact, rest...))
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
			return false
		}
	}
	return true
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
	for _, t := range strings.Split(s, ".") {
		if t != "" {
			out[t] = true
		}
	}
	return out
}

func dedupGrab(rels []Release) []Release {
	out := make([]Release, 0, len(rels))
	for _, r := range rels {
		st := sanitizeGrabQuery(r.Title)
		dup := false
		for _, have := range out {
			if sanitizeGrabQuery(have.Title) != st {
				continue
			}
			d := have.Size - r.Size
			if d < 0 {
				d = -d
			}
			if d < grabDupSizeSlack {
				dup = true
				break
			}
		}
		if !dup {
			out = append(out, r)
		}
	}
	return out
}
