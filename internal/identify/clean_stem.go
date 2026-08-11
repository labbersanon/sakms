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

	// sceneTechMarkerRe matches the first token of a scene-release title's
	// trailing technical block — the low-ambiguity markers that reliably
	// separate the descriptive title from the quality/codec/source/group
	// suffix: the Adult "XXX" tag, an NNNp resolution, or a distinctive
	// codec/source token. Deliberately excludes bare hd/sd/4k/uhd/web (which
	// can appear as ordinary title words) so truncation never fires mid-title.
	// Used by CleanReleaseTitleForSearch to drop everything at/after this
	// boundary — which is how it strips the trailing release group
	// (-Narcos, .PRT, -KTR) that CleanStemForSearch's in-place tag removal
	// leaves behind.
	sceneTechMarkerRe = regexp.MustCompile(`(?i)\b(xxx|\d{3,4}p|x26[45]|h\.?26[45]|hevc|xvid|av1|web[.\-_]?dl|web[.\-_]?rip|bluray|bdrip|brrip|hdtv|dvdrip|remux)\b`)
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

// CleanReleaseTitleForSearch turns a raw scene-release title into a clean
// free-text indexer query, stripping the noise that makes a verbatim release
// title fail as a Prowlarr search term. Verified live against a real Prowlarr
// instance (2026-07-31): the raw title
// "TabooHeat.26.07.18.Cory.Chase.In.Step.Mom.Has.One.Wish.BBC.Gangbang.XXX.720p.HEVC.x265.PRT"
// returned 18 OTHER scenes and never the target, while the cleaned query
// "TabooHeat Cory Chase In Step Mom Has One Wish BBC Gangbang" surfaced it.
// Three signals had to go, in this order:
//
//  1. bracketed groups — "[720p][x264][xFans]", "(26.07.18)", a bracketed
//     studio tag — via bracketedRe;
//  2. the YY.MM.DD / YYYY.MM.DD date token — via dateTokenPattern (embedded
//     release dates are pure noise to a title search);
//  3. the entire trailing technical block (resolution/codec/source, the "XXX"
//     tag, AND the release group after it) — by truncating at the first
//     sceneTechMarkerRe match. Truncation, not in-place removal, is what drops
//     the release group: a bare -Narcos/.PRT/-KTR left in the query was itself
//     enough to return zero matches in live testing.
//
// A leftover in-brackets container tag (e.g. a trailing ".mp4" outside any
// technical block) is mopped up by mediaTagsRe, then separators collapse to
// spaces. An already-clean title (no brackets, date, or technical markers)
// passes through with only separator/whitespace normalization — so well-formed
// queries are unchanged. This is the scene-release counterpart to
// CleanStemForSearch (which removes media tags in place, leaving the group) —
// kept separate because the group-stripping truncation is only safe for a full
// release title, not the partial filename stems CleanStemForSearch handles.
func CleanReleaseTitleForSearch(title string) string {
	s := bracketedRe.ReplaceAllString(title, " ")
	s = dateTokenPattern.ReplaceAllString(s, " ")
	if m := sceneTechMarkerRe.FindStringIndex(s); m != nil {
		s = s[:m[0]]
	}
	s = mediaTagsRe.ReplaceAllString(s, " ")
	s = pathSepCharRe.ReplaceAllString(s, " ")
	s = multiSpaceRe2.ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
}

// ReleaseTitleLacksSceneStructure reports whether title carries NONE of the
// scene-release structure CleanReleaseTitleForSearch knows how to recognize:
// no bracketed group, no DOTTED date token (dateTokenPattern is literal-dot
// only — a space-separated "26 02 08" is invisible to it), no technical
// marker, no media tag. It answers that question the only honest way
// available — by running the cleaner and checking it changed nothing beyond
// separator/whitespace normalization — so it can never drift out of sync with
// what the cleaner actually strips.
//
// A true answer means the stored title is UNVERIFIABLE as a well-formed
// release name, not that it is clean: the two look identical from here. That
// is exactly the shape of the 2026-08-11 bug — a title already truncated
// mid-word by whatever recorded it ("...Teasing Cheerlea"), with a
// space-separated date and no trailing tech marker, survives the cleaner
// completely unmodified and goes to Prowlarr as a query that cannot match.
// autoGrabSearch's Adult branch uses this as the gate on its 0-result
// Studio+Title fallback: a query built from a title with recognizable scene
// structure that legitimately returns nothing is believed, while a query built
// from an unverifiable one is retried more broadly.
func ReleaseTitleLacksSceneStructure(title string) bool {
	s := pathSepCharRe.ReplaceAllString(title, " ")
	s = multiSpaceRe2.ReplaceAllString(s, " ")
	return CleanReleaseTitleForSearch(title) == strings.TrimSpace(s)
}
