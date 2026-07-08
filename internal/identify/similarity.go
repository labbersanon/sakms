package identify

import (
	"regexp"
	"strings"
)

var camelBoundaryRe = regexp.MustCompile(`([a-z])([A-Z])`)

// wordRe: Go's regexp \w is ASCII-only ([0-9A-Za-z_]). Using \p{L}\p{N}_
// instead keeps accented and non-Latin characters intact as single tokens.
var wordRe = regexp.MustCompile(`[\p{L}\p{N}_]+`)

func tokenize(s string) map[string]struct{} {
	s = camelBoundaryRe.ReplaceAllString(s, "$1 $2")
	s = strings.ToLower(s)
	words := wordRe.FindAllString(s, -1)
	set := make(map[string]struct{}, len(words))
	for _, w := range words {
		set[w] = struct{}{}
	}
	return set
}

func intersectionSize(a, b map[string]struct{}) int {
	small, big := a, b
	if len(b) < len(a) {
		small, big = b, a
	}
	n := 0
	for k := range small {
		if _, ok := big[k]; ok {
			n++
		}
	}
	return n
}

// TitleSimilarity returns max(Jaccard, containment-of-a-in-b) when a is
// substantially contained in b (containment >= 0.7 with >= 2 overlapping
// tokens, to avoid a false-positive match on a single generic word), else
// plain Jaccard similarity. Splits camelCase/PascalCase boundaries first
// (e.g. "MaxineX" -> "Maxine X") so contiguous filenames tokenize sensibly.
func TitleSimilarity(a, b string) float64 {
	ta, tb := tokenize(a), tokenize(b)
	if len(ta) == 0 || len(tb) == 0 {
		return 0.0
	}
	inter := intersectionSize(ta, tb)
	union := len(ta) + len(tb) - inter
	jaccard := float64(inter) / float64(union)
	containment := float64(inter) / float64(len(ta))
	if containment >= 0.7 && inter >= 2 {
		if containment > jaccard {
			return containment
		}
		return jaccard
	}
	return jaccard
}
