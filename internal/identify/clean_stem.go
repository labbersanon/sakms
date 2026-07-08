package identify

import (
	"regexp"
	"strings"
)

var (
	bracketedRe   = regexp.MustCompile(`\[.*?\]|\(.*?\)`)
	mediaTagsRe   = regexp.MustCompile(`(?i)\b(1080p|2160p|720p|4k|hd|sd|xxx|hevc|x265|x264|aac|h264|mp4|mkv|wmv)\b`)
	pathSepCharRe = regexp.MustCompile(`[-_.]`)
	multiSpaceRe2 = regexp.MustCompile(`\s+`)
)

// CleanStemForSearch strips brackets/parentheses and common media tags from a
// filename stem, for use as a fallback web-search query when the structured
// (studio+title) query returns nothing.
func CleanStemForSearch(stem string) string {
	s := bracketedRe.ReplaceAllString(stem, "")
	s = mediaTagsRe.ReplaceAllString(s, "")
	s = pathSepCharRe.ReplaceAllString(s, " ")
	s = multiSpaceRe2.ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
}
