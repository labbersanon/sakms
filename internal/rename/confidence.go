package rename

import (
	"math"
	"regexp"
	"strconv"
	"strings"
)

// DefaultConfidenceThreshold is the minimum matchConfidence score (0-100)
// a TMDB search's best (items[0]) result must
// clear to be accepted automatically — chosen as a permissive floor, not a
// strict one: searchterm.FromName's cleaned title is an explicitly
// best-effort heuristic (see its own package doc), so a real match often
// won't be a perfect string match against TMDB's canonical title. This
// threshold needs to tolerate that gap while still catching results that
// share essentially nothing with the search term (the case that today's
// unconditional items[0] silently accepts).
const DefaultConfidenceThreshold = 40

var (
	yearParenRe = regexp.MustCompile(`\((19\d{2}|20\d{2})\)`)
	yearBareRe  = regexp.MustCompile(`\b(19\d{2}|20\d{2})\b`)
	nonAlnumRe  = regexp.MustCompile(`[^a-z0-9]+`)
)

// matchConfidence scores how well matchTitle (TMDB's best search result)
// corresponds to searchTerm (the cleaned name SAK searched TMDB with),
// returning a percentage in [0, 100] — higher is more confident. Combines
// two independent signals:
//   - title similarity: a Dice coefficient over normalized word-token sets
//     (see titleSimilarity) — tolerant of word reordering and partial
//     overlap, since searchTerm is a best-effort heuristic, not guaranteed
//     to equal the canonical title verbatim.
//   - year corroboration: if searchTerm contains an unambiguous year AND
//     matchReleaseDate parses to one, a mismatch of more than a year halves
//     the score rather than being ignored — two different titles can easily
//     share word tokens, but rarely share both title tokens AND a release
//     year by coincidence. A known, accepted limitation: a numeric movie
//     title (e.g. "2012") can make extractYear return a false year that
//     happens to not match the real release year, incorrectly halving an
//     otherwise-correct match's score — accepted because the halving alone
//     rarely drops a genuinely correct, high-token-overlap match below
//     DefaultConfidenceThreshold (see the package's test for a worked
//     example), and a more precise fix would need a real filename parser
//     this package doesn't have.
func matchConfidence(searchTerm, matchTitle, matchReleaseDate string) int {
	score := titleSimilarity(searchTerm, matchTitle)

	if searchYear := extractYear(searchTerm); searchYear != 0 {
		if matchYear := yearFromReleaseDate(matchReleaseDate); matchYear != 0 {
			diff := searchYear - matchYear
			if diff > 1 || diff < -1 {
				score *= 0.5
			}
		}
	}

	return int(math.Round(score * 100))
}

// titleSimilarity is a Dice coefficient (2*|A∩B| / (|A|+|B|)) over
// normalized (lowercased, punctuation-stripped) word-token sets — simple,
// dependency-free, and tolerant of word reordering and partial overlap.
// Not tolerant of misspellings (no character-level edit distance) — a
// deliberate simplicity tradeoff, not an oversight; see the package doc.
// Returns 0 if either side has no tokens at all.
func titleSimilarity(a, b string) float64 {
	ta, tb := tokenSet(a), tokenSet(b)
	if len(ta) == 0 || len(tb) == 0 {
		return 0
	}
	shared := 0
	for t := range ta {
		if tb[t] {
			shared++
		}
	}
	return 2 * float64(shared) / float64(len(ta)+len(tb))
}

func tokenSet(s string) map[string]bool {
	s = nonAlnumRe.ReplaceAllString(strings.ToLower(s), " ")
	set := make(map[string]bool)
	for _, tok := range strings.Fields(s) {
		set[tok] = true
	}
	return set
}

// extractYear pulls a plausible (19xx/20xx) year out of s, e.g. a
// searchterm.FromName result that preserved a trailing "(2020)" or a bare
// "1999" (both real, common forms — see searchterm's own tests). A
// parenthesized year is preferred when present, since it's an unambiguous,
// deliberate year marker; otherwise falls back to a bare year ONLY when
// exactly one 19xx/20xx-shaped number appears — two or more is treated as
// "no reliable signal" rather than guessing which one is the real year.
func extractYear(s string) int {
	if m := yearParenRe.FindStringSubmatch(s); m != nil {
		y, err := strconv.Atoi(m[1])
		if err == nil {
			return y
		}
	}
	matches := yearBareRe.FindAllString(s, -1)
	if len(matches) != 1 {
		return 0
	}
	y, err := strconv.Atoi(matches[0])
	if err != nil {
		return 0
	}
	return y
}
