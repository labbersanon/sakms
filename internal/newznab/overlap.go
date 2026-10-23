package newznab

import (
	"regexp"
	"strconv"
	"strings"
)

var titleYear = regexp.MustCompile(`(?:^|[.\s_\-])((?:19|20)\d{2})(?:[.\s_\-]|$)`)

// Claude 2026-10-23: overlap joins NZB release names, not IMDB titles.
// Reason: Dock overlap is "same NZB on both indexers." IMDB collapsed every
//
//	quality of one movie into one key. imdb_id stays on the row for Lighthouse.
//
// Troubleshooting: both≈movie count → overlap_key still imdb:/tvdb:/tmdb:.
// Review if: Lighthouse filters TMDB by imdb_id (do not reuse this key).
func OverlapKey(h CatalogHit) string {
	title := sanitizeGrabQuery(h.Title)
	if title == "" {
		return ""
	}
	return "nzb:" + title
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
