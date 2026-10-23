package newznab

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var titleYear = regexp.MustCompile(`(?:^|[.\s_\-])((?:19|20)\d{2})(?:[.\s_\-]|$)`)

// OverlapKey is the join used to tell whether two indexers list the same
// title. Prefer IMDB, then TVDB, then TMDB, then sanitized title+year.
// Empty means the hit cannot participate in overlap.
func OverlapKey(h CatalogHit) string {
	if id := NormalizeIMDB(h.ImdbID); id != "" {
		return "imdb:" + id
	}
	if id := DigitsOnly(h.TvdbID); id != "" {
		return "tvdb:" + id
	}
	if id := DigitsOnly(h.TmdbID); id != "" {
		return "tmdb:" + id
	}
	title := sanitizeGrabQuery(h.Title)
	if title == "" {
		return ""
	}
	year := h.Year
	if year == 0 {
		year = yearFromTitle(h.Title)
	}
	if year > 0 {
		return fmt.Sprintf("title:%s:%d", title, year)
	}
	return "title:" + title
}

// NormalizeIMDB turns "tt0111161", "111161", or "IMDB tt0111161" into tt + digits.
func NormalizeIMDB(raw string) string {
	s := strings.ToLower(strings.TrimSpace(raw))
	if s == "" {
		return ""
	}
	s = strings.TrimPrefix(s, "imdb:")
	s = strings.TrimPrefix(s, "imdb")
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "tt")
	s = DigitsOnly(s)
	if s == "" {
		return ""
	}
	return "tt" + s
}

// DigitsOnly keeps decimal digits from s.
func DigitsOnly(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func yearFromTitle(title string) int {
	m := titleYear.FindStringSubmatch(title)
	if len(m) < 2 {
		return 0
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		return 0
	}
	return n
}
