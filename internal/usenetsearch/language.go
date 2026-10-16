package usenetsearch

import (
	"strings"

	"github.com/labbersanon/sakms/internal/release"
)

// Claude 2026-10-16: NNTP index follows Global preferred grab languages.
// Reason: OVER of dvd.german* filled the index with titles autograb would reject.
// Troubleshooting: german groups still crawled → langsFn not wired / filter off.
// Review if: indexer APIs expose structured audio language (drop title tokens).
// Related: release.TitleLanguageAllowed; Settings → Preferred grab languages.

// groupLanguageQuery turns a dotted newsgroup into title-like tokens so
// TitleLanguageAllowed's word-boundary matcher sees "german" in
// alt.binaries.dvd.german.
func groupLanguageQuery(group string) string {
	return strings.ReplaceAll(group, ".", " ")
}

func languageAllowed(title string, preferred []string, filter bool) bool {
	if !filter {
		return true
	}
	return release.TitleLanguageAllowed(title, preferred)
}

func (c *Crawler) languageOK(title string) bool {
	return languageAllowed(title, c.PreferredLanguages, c.LanguageFilter)
}

func (c *Crawler) groupLanguageOK(group string) bool {
	return languageAllowed(groupLanguageQuery(group), c.PreferredLanguages, c.LanguageFilter)
}

func (c *Crawler) dropWrongLanguage(headers []HeaderRow) []HeaderRow {
	if !c.LanguageFilter || len(headers) == 0 {
		return headers
	}
	out := make([]HeaderRow, 0, len(headers))
	for _, h := range headers {
		if h.ReleaseName == "" || h.Obfuscated || c.languageOK(h.ReleaseName) {
			out = append(out, h)
		}
	}
	return out
}
