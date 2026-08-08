package rename

import (
	"context"
	"fmt"
	"hash/fnv"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/labbersanon/sakms/internal/identify"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/proposals"
	"github.com/labbersanon/sakms/internal/searchterm"
)

// Claude 2026-08-05: web-authority naming beyond TMDB/TVDB
//   (deep-interview-rename-web-authority).
// Reason: Brave Phase 2 only re-queries TMDB; HBO specials / non-catalog titles
//   need web title+year as Pending when catalogs miss.
// Troubleshooting: TitleN still Pending → junk gate failed; Funny Faces unmatched
//   → token overlap or missing year from grounding.
// Review if: library schema allows nullable tmdb_id without synthetic negatives.
//
// Claude 2026-08-05: webAuthorityQueries drops trailing sequel markers (III, 2, …)
// Reason: Bing/SearXNG for "Red Skelton Funny Faces III" returns Red (2010), not
//   Skelton; query without III surfaces IMDb/YouTube Funny Faces III (1984).
// Troubleshooting: unmatched despite IMDb listing → poisoned sequel query.
// Review if: search engines stop mangling roman-numeral sequel queries.

const webMatchReasonPrefix = "web match:"

// junkStemRe matches empty/generic stems: Title, Title12, Video3, Movie, Track1, Clip9.
var junkStemRe = regexp.MustCompile(`(?i)^(title|video|movie|track|clip)\d*$`)

// IsJunkRenameFilename reports filenames too empty for web-authority Pending
// (e.g. RED_SKELTON_Title1.mp4). Real special titles like
// "Red Skelton More Funny Faces" are not junk.
func IsJunkRenameFilename(name string) bool {
	stem := searchterm.FromName(name)
	if stem == "" {
		return true
	}
	parts := strings.FieldsFunc(stem, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	if len(parts) == 0 {
		return true
	}
	last := parts[len(parts)-1]
	if junkStemRe.MatchString(last) {
		return true
	}
	if len(parts) == 1 && junkStemRe.MatchString(parts[0]) {
		return true
	}
	return false
}

var tokenStop = map[string]struct{}{
	"a": {}, "an": {}, "the": {}, "and": {}, "or": {}, "of": {}, "for": {},
	"to": {}, "in": {}, "on": {}, "at": {}, "with": {}, "from": {}, "by": {},
	"vs": {}, "feat": {}, "ft": {}, "av1": {}, "x264": {}, "x265": {}, "hevc": {},
	"h264": {}, "bluray": {}, "webrip": {}, "webdl": {}, "hdtv": {}, "remux": {},
	"proper": {}, "repack": {}, "extended": {}, "unrated": {}, "internal": {},
}

func titleTokens(s string) map[string]struct{} {
	out := map[string]struct{}{}
	fields := strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	for _, f := range fields {
		if len(f) < 2 {
			continue
		}
		if _, stop := tokenStop[f]; stop {
			continue
		}
		if len(f) <= 3 && onlyLetters(f) && isMostlyRoman(f) {
			continue
		}
		out[f] = struct{}{}
	}
	return out
}

// Claude 2026-08-07: hasWordToken — "is this a title or just a code" predicate (plan §1.4)
// Reason: the compact-eNNNN seed cascade in proposeOneEpisodeLibrary needs to
//   detect a seed that carries no title at all ("e614_720x480.mp4"). Three
//   simpler predicates all silently fail on it: `looseTitle == ""` (the string
//   is non-empty), `len(titleTokens(...)) == 0` (titleTokens keeps both "e614"
//   and "720x480" — each is >= 2 runes, not a stop-word, not a roman numeral),
//   and "does any token contain a letter" ("e614" has an 'e', "720x480" an 'x').
// Troubleshooting: an eNNNN file parses its season/episode but stays Unmatched
//   with a gate-rejection reason — confirm the seed cascade's step 3 fired.
// Review if: titleTokens starts dropping digit-bearing tokens itself.

// wordTokenRe matches two or more consecutive ASCII letters — the "is this a
// word rather than a code" test.
var wordTokenRe = regexp.MustCompile(`[A-Za-z]{2,}`)

// hasWordToken reports whether s tokenizes to at least one token containing two
// consecutive letters. Do not weaken it to a plain "contains a letter" check —
// that is the third rejected alternative in the Reason block above, not an
// untried simplification.
func hasWordToken(s string) bool {
	for tok := range titleTokens(searchterm.FromName(s)) {
		if wordTokenRe.MatchString(tok) {
			return true
		}
	}
	return false
}

func onlyLetters(s string) bool {
	for _, r := range s {
		if !unicode.IsLetter(r) {
			return false
		}
	}
	return true
}

func isMostlyRoman(s string) bool {
	for _, r := range strings.ToLower(s) {
		switch r {
		case 'i', 'v', 'x', 'l', 'c', 'm':
		default:
			return false
		}
	}
	return true
}

// HasTitleTokenOverlap requires a meaningful token match between the filename
// stem and the grounded web title (blocks inventing titles for TitleN, and
// blocks weak single-token hits like filename "Red …" vs grounded "Red").
func HasTitleTokenOverlap(filename, groundedTitle string) bool {
	fileToks := titleTokens(searchterm.FromName(filename))
	groundToks := titleTokens(groundedTitle)
	if len(fileToks) == 0 || len(groundToks) == 0 {
		return false
	}
	shared := 0
	strong := false
	for t := range fileToks {
		if _, ok := groundToks[t]; !ok {
			continue
		}
		shared++
		if len(t) >= 4 {
			strong = true
		}
	}
	return strong || shared >= 2
}

// trailingSequelRe matches end-of-query sequel markers that poison Bing/etc.
// (e.g. "… Funny Faces III" → Red (2010) instead of Skelton).
var trailingSequelRe = regexp.MustCompile(`(?i)\s+\b(II|III|IV|V|VI|VII|VIII|IX|X|[2-9])\b\s*$`)

func dropTrailingSequelMarker(q string) string {
	s := strings.TrimSpace(trailingSequelRe.ReplaceAllString(strings.TrimSpace(q), ""))
	return s
}

// webAuthorityQueries returns distinct web-search strings: guessed + filename
// SearchQueries, each also without a trailing sequel marker.
func webAuthorityQueries(entryName, guessed string) []string {
	var out []string
	add := func(s string) {
		s = strings.TrimSpace(s)
		if s == "" {
			return
		}
		for _, e := range out {
			if strings.EqualFold(e, s) {
				return
			}
		}
		out = append(out, s)
	}
	if g := strings.TrimSpace(guessed); g != "" {
		// Sequel-stripped first: engines often poison "… III" (Bing → Red 2010).
		if d := dropTrailingSequelMarker(g); !strings.EqualFold(d, g) {
			add(d)
		}
		add(g)
	}
	for _, q := range searchterm.SearchQueries(entryName) {
		if d := dropTrailingSequelMarker(q); !strings.EqualFold(d, q) {
			add(d)
		}
		add(q)
	}
	return out
}

// WebAuthorityTMDBID returns a stable negative id for library uniqueness when
// there is no real TMDB id. Real TMDB ids are always positive; naming omits
// [tmdbid-…] for id <= 0.
func WebAuthorityTMDBID(title string, year int) int {
	h := fnv.New32a()
	_, _ = h.Write([]byte(strings.ToLower(strings.TrimSpace(title))))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(strconv.Itoa(year)))
	v := int(h.Sum32()&0x7fffffff) + 1
	return -v
}

// tryWebAuthorityMovie runs after Brave→TMDB miss: ground via web, require
// title+year, junk/token gates, then Pending with synthetic TMDB id.
func tryWebAuthorityMovie(
	ctx context.Context, sess *mode.Session,
	byTMDB map[int]bool, generalRoot, foundRoot string,
	entryName, guessed string, base proposals.Proposal,
) *proposals.Proposal {
	if sess.WebSearch == nil || sess.MainstreamAI == nil {
		return nil
	}
	if IsJunkRenameFilename(entryName) {
		return nil
	}
	var grounded identify.TitleGrounding
	for _, query := range webAuthorityQueries(entryName, guessed) {
		g, err := identify.GroundTitleViaSearch(ctx, sess.WebSearch, sess.MainstreamAI, query)
		if err != nil || g.Title == "" || g.Year <= 0 {
			continue
		}
		if !HasTitleTokenOverlap(entryName, g.Title) {
			continue
		}
		grounded = g
		break
	}
	if grounded.Title == "" || grounded.Year <= 0 {
		return nil
	}
	synth := WebAuthorityTMDBID(grounded.Title, grounded.Year)
	p := base
	targetRoot := generalRoot
	if foundRoot == sess.KidsRootPath {
		targetRoot = sess.KidsRootPath
	}
	if byTMDB[synth] {
		acceptDuplicatePending(&p, grounded.Title, synth, grounded.Year, targetRoot)
		p.Reason = fmt.Sprintf("%s %s (%d)", webMatchReasonPrefix, grounded.Title, grounded.Year)
		return &p
	}
	p.Status = proposals.Pending
	p.Title = grounded.Title
	p.TMDBID = synth
	p.Year = grounded.Year
	p.RootFolderPath = targetRoot
	p.Reason = fmt.Sprintf("%s %s (%d)", webMatchReasonPrefix, grounded.Title, grounded.Year)
	return &p
}

func tryWebAuthoritySeries(
	ctx context.Context, sess *mode.Session,
	tracked map[episodeKey]bool, generalRoot, foundRoot string,
	season, episode int, extraEpisodes []int,
	entryName, guessed string, base proposals.Proposal,
) *proposals.Proposal {
	if sess.WebSearch == nil || sess.MainstreamAI == nil {
		return nil
	}
	if IsJunkRenameFilename(entryName) {
		return nil
	}
	var grounded identify.TitleGrounding
	for _, query := range webAuthorityQueries(entryName, guessed) {
		g, err := identify.GroundTitleViaSearch(ctx, sess.WebSearch, sess.MainstreamAI, query)
		if err != nil || g.Title == "" || g.Year <= 0 {
			continue
		}
		if !HasTitleTokenOverlap(entryName, g.Title) {
			continue
		}
		grounded = g
		break
	}
	if grounded.Title == "" || grounded.Year <= 0 {
		return nil
	}
	synth := WebAuthorityTMDBID(grounded.Title, grounded.Year)
	key := episodeKey{tmdbID: synth, season: season, episode: episode}
	// Claude 2026-08-07: SITE 8 of 8 — DELIBERATELY NOT SOFTENED (plan §5.2a)
	// Reason: deep-interview-sakms-series-parsing-accuracy-improvements softened
	//   the other SEVEN tracked-guard sites to accept-as-alternate; this one is
	//   deferred, so it is now the ONLY tracked[] read that still declines.
	//   Recorded here because a mechanical `grep tracked\[` sweep will find it
	//   and must know it was considered, not missed. Three reasons, in order of
	//   weight:
	//   1. PRIMARY — returning nil hands control to gateRejectedReason() and the
	//      terminal-reason ladder below it, which rename.go:1295-1298 documents
	//      as load-bearing in one direction ("gateRejectedReason() must keep
	//      winning when it fires"). Emitting a Pending here bypasses that ladder,
	//      and re-deriving which of the competing terminal reasons an operator
	//      should see in the duplicate case is a real design question this spec
	//      never asked. Movies has no such ladder, so no Movies precedent
	//      touches this reason.
	//   2. No spec-named population routes here (Beverly Hillbillies → Site 1,
	//      Red Skelton → Site 5, Laurel & Hardy → Site 6), so softening buys
	//      nothing the spec asked for while carrying reason 1's cost.
	//   3. SECONDARY, and explicitly counter-weighted: this key is a SYNTHETIC
	//      negative id, so a hit means "two files independently grounded to the
	//      same title+year", not "TMDB/TVDB says same show, same slot". This is
	//      NOT a safety argument — Movies already ships exactly this softening on
	//      the same WebAuthorityTMDBID key (web_authority.go:248's
	//      acceptDuplicatePending, with rename.go:557-558 documenting the
	//      Apply-side counterpart as intentional). What remains is only that an
	//      episode-slot collision has more degrees of freedom than a title one.
	//   Consequence to state honestly: a duplicate whose show was resolved ONLY
	//   by the web-naming-authority path still lands Unmatched. Known, accepted
	//   residual — not a bug, and not something tests should assert away. Note
	//   this branch writes NO proposal and therefore NO reason string, so it
	//   contributes zero hits to the retired-reason-string audits.
	// Review if: a follow-up spec asks for Site 8 — the work is then to answer
	//   reason 1 (which terminal reason wins in the duplicate case), NOT to
	//   overcome reason 3, which Movies has already refuted in production.
	if tracked[key] {
		return nil
	}
	targetRoot := generalRoot
	if foundRoot == sess.KidsRootPath {
		targetRoot = sess.KidsRootPath
	}
	p := base
	p.Status = proposals.Pending
	p.Title = grounded.Title
	p.TMDBID = synth
	p.Year = grounded.Year
	p.SeasonNumber = season
	p.EpisodeNumber = episode
	p.ExtraEpisodeNumbers = extraEpisodes
	p.RootFolderPath = targetRoot
	p.Reason = fmt.Sprintf("%s %s (%d)", webMatchReasonPrefix, grounded.Title, grounded.Year)
	return &p
}

// unmatchedAfterWebAuthority sets Unmatched after web-authority also declines.
func unmatchedAfterWebAuthority(p *proposals.Proposal, entryName string, lastLimit int, lastTerm string) {
	if IsJunkRenameFilename(entryName) {
		p.Status = proposals.Unmatched
		p.Reason = fmt.Sprintf("filename too generic for web naming authority (%q) — needs manual review", entryName)
		return
	}
	if lastLimit == 0 {
		p.Status = proposals.Unmatched
		p.Reason = fmt.Sprintf("no catalog or web title+year for queries derived from %q", entryName)
		return
	}
	p.Status = proposals.Unmatched
	p.Reason = fmt.Sprintf("no catalog/web title+year after top %d candidates (last query %q)", lastLimit, lastTerm)
}
