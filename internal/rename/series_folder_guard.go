// Claude 2026-08-06: Series title-collision precision gate (Check A + Check B)
// Reason: autopilot-impl-title-collision-fix — live TMDB search for "The Path"
//
//	also returns wrong shows ("The Oath", "The Secret Path", a duplicate "The
//	Path" catalog entry) ahead of or alongside the correct one; the acceptance
//	path had no title-overlap check and no folder-identity check, so 6 real
//	files landed under the wrong show.
//
// Troubleshooting: a Series file lands Pending under an unrelated show's TMDB
//
//	id — check whether this file's candidate walk ever reaches seriesGate.allows.
//
// Review if: library_series gains a real per-show folder-path column, at which
//
//	point the title feed (showFolderTitleKey) becomes unnecessary.
//
// Context: see .omc/plans/autopilot-impl-title-collision-fix.md
package rename

import (
	"regexp"
	"strings"
	"unicode"

	"github.com/labbersanon/sakms/internal/searchterm"
)

// seriesGate is the Series-only precision gate applied to every TMDB/TVDB
// candidate before it is accepted or stashed as the weak fallback. Neither
// check touches SignalsCorroborate and neither is reachable from Movies or
// Adult (see the plan's §6 Movies-safety argument).
type seriesGate struct {
	// titleSeed is the title the TMDB/TVDB query was actually derived from —
	// NOT the raw filename (see the plan's §1.1: an opaque hash basename
	// tokenizes to one 32-char token and would reject every real title
	// deterministically). A no-tokens seed disables Check A (§1.2a).
	titleSeed string
	// pinnedTMDBID is the show folder's pinned TMDB id (Check B). <= 0
	// disables Check B (§3.4 rule 1) — this covers "no pin", an ambiguous
	// folder key (stored as 0), and a reachable negative synthetic id.
	pinnedTMDBID int
	// rejected is a SHARED counter across every seriesGate derived from the
	// same file via withSeed — a POINTER, not a value, so a rejection inside
	// trySeriesQueries/bravePhase2Series/tvdbFallbackSeries (which each work
	// from a copy) is visible on the original gate too (§4.3's distinct
	// Unmatched reason depends on this).
	rejected *int
}

// withSeed returns a copy of g with a different title seed, sharing the same
// rejected counter by pointer — see the rejected field's doc comment for why
// a value copy here would silently break §4.3.
func (g *seriesGate) withSeed(seed string) *seriesGate {
	return &seriesGate{titleSeed: seed, pinnedTMDBID: g.pinnedTMDBID, rejected: g.rejected}
}

func (g *seriesGate) reject() {
	if g.rejected != nil {
		*g.rejected++
	}
}

// allowsTitle is Check A: a meaningful token overlap between the query seed
// and the candidate's title, reusing HasTitleTokenOverlap unmodified. Fails
// open (allows) when either side tokenizes to the EMPTY SET, not merely an
// empty string — titleTokens drops every token shorter than 2 runes and every
// short roman-numeral-only token, so a legitimate title like "9-1-1" or "V"
// can flatten to no tokens on a non-empty string (§1.2a). Mirrors
// HasTitleTokenOverlap's own asymmetry: searchterm.FromName runs on the seed
// side only, matching argument 1 of HasTitleTokenOverlap.
func (g *seriesGate) allowsTitle(candidateTitle string) bool {
	if len(titleTokens(searchterm.FromName(g.titleSeed))) == 0 ||
		len(titleTokens(candidateTitle)) == 0 {
		return true // no discriminating tokens — no basis to reject
	}
	return HasTitleTokenOverlap(g.titleSeed, candidateTitle)
}

// allowsID is Check B: the candidate's TMDB id must match the folder's
// pinned show id. Fails open (allows) on any of §3.4's four independent
// conditions: no pin (or a non-positive/ambiguous one), or a non-positive
// candidate id (covers tryWebAuthoritySeries' synthetic negative ids, which
// must never be compared against a real pin — see §2.6).
func (g *seriesGate) allowsID(candidateID int) bool {
	if g.pinnedTMDBID <= 0 {
		return true
	}
	if candidateID <= 0 {
		return true
	}
	return candidateID == g.pinnedTMDBID
}

// allows runs both checks in the order §3.4b requires: a folder-confirmed id
// SHORT-CIRCUITS Check A. This is not an optimisation — without it, an odd
// query seed (e.g. an opaque hash) could veto a candidate whose id already
// equals the folder's pinned show, which is the correct show by the guard's
// own definition. Increments the shared rejected counter on any reject.
func (g *seriesGate) allows(candidateTitle string, candidateID int) bool {
	if g.pinnedTMDBID > 0 && candidateID == g.pinnedTMDBID {
		return true // folder-confirmed — the title check is moot, never let it veto
	}
	if !g.allowsID(candidateID) {
		g.reject()
		return false
	}
	if !g.allowsTitle(candidateTitle) {
		g.reject()
		return false
	}
	return true
}

var (
	// showFolderTMDBIDSuffixRe strips a trailing " [tmdbid-N]" tag, mirroring
	// naming.MovieFolderName's construction (internal/naming/naming.go).
	showFolderTMDBIDSuffixRe = regexp.MustCompile(`(?i)\s*\[tmdbid-\d+\]\s*$`)
	// showFolderYearSuffixRe strips a trailing " (YYYY)" year tag.
	showFolderYearSuffixRe = regexp.MustCompile(`\s*\((\d{4})\)\s*$`)
)

// normalizeShowKey is the single shared core both showFolderKey and
// showFolderTitleKey route through: strip a trailing " [tmdbid-N]", then a
// trailing " (YYYY)", then lowercase and drop every non-alphanumeric rune.
// Convergence between the path feed and the title feed IS the whole
// mechanism (§3.1) — e.g. "The Path" and "The Path (2016) [tmdbid-65227]"
// both normalize to "thepath". Two independent implementations would
// silently diverge the first time either is touched.
func normalizeShowKey(raw string) string {
	s := showFolderTMDBIDSuffixRe.ReplaceAllString(raw, "")
	s = showFolderYearSuffixRe.ReplaceAllString(s, "")
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// showFolderTitleKey normalizes a tracked series' own Title (the title feed —
// fires even with zero tracked episode files on disk, see §3.1).
func showFolderTitleKey(title string) string {
	return normalizeShowKey(title)
}

// showFolderKey returns the normalized name of the show folder path sits
// under — the first path segment below whichever configured root contains
// it — or "" when path is not under a known root, or sits directly in a root
// with no show-folder segment at all (the path feed — fires when a tracked
// episode's file physically lives under a folder with this name, §3.1).
//
// The traversal itself lives in showFolderSegment
// (series_tvdb_episode_match.go), which documents the longest-first root
// sort and the filepath.Rel boundary check. Sharing one implementation is
// the whole point: the anthology pass needs that same segment RAW as a
// query seed, and a second copy would drift.
func showFolderKey(path string, roots []string) string {
	return normalizeShowKey(showFolderSegment(path, roots))
}

// pinFolderID sets folderIDs[key] on first sight of id, and sets it to the
// sticky-ambiguous sentinel 0 the moment a second DIFFERENT id claims the
// same key — a third write (of either id) does not resurrect it.
//
// Both an empty key and a non-positive id are refused outright and never
// enter the map at all — these are independent, both-required guards
// (§3.3 step 2): without the id guard, a tmdb_id<=0 series row (a real, live
// shape — see allowsID's doc comment) would pin a real folder key, and the
// very next legitimate series claiming that key would flip it to the
// sticky-ambiguous 0 sentinel, silently disabling the guard for that folder.
// Without the empty-key guard, a folderIDs[""] entry would be inherited by
// every unkeyable file in the scan.
func pinFolderID(m map[string]int, key string, id int) {
	if key == "" || id <= 0 {
		return
	}
	existing, ok := m[key]
	if !ok {
		m[key] = id
		return
	}
	if existing != id {
		m[key] = 0
	}
}
