// Claude 2026-08-06: Series episode-title matching for files with no
//
//	season/episode marker anywhere in the basename or parent directory
//	(library.ParseEpisodeNumbersLoose's !ok branch).
//
// Reason: autopilot-impl-episode-title-matching — a file whose show is
//
//	ALREADY pinned by the folder guard (series_folder_guard.go) can still be
//	placed by matching its recovered title against that show's own TMDB
//	episode names, across every season TMDB reports. This never guesses the
//	show — pin.tmdbID > 0 is a precondition, not an outcome of this file — so
//	it only ever resolves the ONE remaining unknown (which episode), matching
//	this project's "one unknown at a time" acceptance posture.
//
// Troubleshooting: an episode-title match landed on the wrong slot — check
//
//	episodeTitleMatches' residual/containment rule below, not the parse gate
//	in rename.go. An ambiguous-ahead-of-unique outcome — check
//	searchEpisodeByTitle's early-exit-at-two-matches ordering.
//
// Review if: ParseEpisodeNumbersLoose learns the digit-code shape (e1524),
//
//	which would move the Red Skelton population (see the plan's §0.2) out of
//	this branch entirely.
//
// Context: see .omc/plans/autopilot-impl-episode-title-matching.md
package rename

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/proposals"
	"github.com/labbersanon/sakms/internal/searchterm"
	"github.com/labbersanon/sakms/internal/tmdb"
)

// episodeTitleMatchReasonPrefix marks an Unmatched/Pending reason as having
// come from this path, mirroring webMatchReasonPrefix (web_authority.go).
const episodeTitleMatchReasonPrefix = "episode-title match:"

// pinnedShow is the tracked library.Series row whose normalized title/folder
// key this file's show folder resolved to — the SAME pin seriesGate's Check B
// runs on, carried whole instead of as a bare id.
//
// Carrying Title/Year/RootFolderPath is not convenience: ApplyLibrarySeries
// feeds Proposal.Title/Year straight into naming.SeriesFolderName AND into
// UpsertSeries' unconditional `title = excluded.title, year = excluded.year`
// (internal/library/library_series.go). A proposal built from TMDB's own
// metadata instead would place the file in a SECOND folder for the same show
// and rewrite the tracked row to match — and normalizeShowKey strips both
// " (YYYY)" and " [tmdbid-N]", so the folder guard cannot see that drift.
//
// This path only ever READS Title/Year/RootFolderPath from the already-
// tracked row and writes those same values back via UpsertSeries at Apply —
// so the write is idempotent by construction, not merely "whatever happens to
// be on the folder on disk" (a slightly different, weaker claim the plan's
// own prose states — the code comment states it correctly). See the plan's
// §0.3 for the full defect this closes.
//
// The zero value means "unpinned"; tmdbID <= 0 is the single test for it,
// identical to the bare int it replaces.
type pinnedShow struct {
	tmdbID int
	title  string
	year   int
	root   string // library.Series.RootFolderPath — the LIBRARY root (§0.4)
}

// episodeTitleMatch is one (season, episode) slot whose TMDB episode Name
// matched the recovered file title.
type episodeTitleMatch struct {
	season  int
	episode int
	name    string // the TMDB SeasonEpisode.Name that matched
	airDate string
}

// episodeTitleSearch is the result of scanning EVERY season of one show.
//
// The three outcomes are deliberately distinguishable, because they must be
// handled differently and two of them are easy to conflate:
//   - Match != nil               -> exactly one slot matched; accept
//   - Match == nil && Found >= 2 -> ambiguous; Unmatched with a DISTINCT reason
//   - Match == nil && Found == 0 -> no match; fall through unchanged
//   - Incomplete                 -> uniqueness UNPROVABLE; treat as no match
type episodeTitleSearch struct {
	Match      *episodeTitleMatch
	Found      int                // capped at 2 — the search stops as soon as ambiguity is proven
	First      *episodeTitleMatch // the 1st slot, for the ambiguity reason string
	Second     *episodeTitleMatch // the 2nd slot, for the ambiguity reason string
	Incomplete bool
}

// searchEpisodeByTitle scans every season TMDB reports for show tmdbID and
// returns the slot(s) whose episode Name matches basename.
//
// Fail-CLOSED on any hole in the data: a TVDetails error, an empty Seasons
// array, or ANY SeasonDetails error aborts the whole search as Incomplete.
// This is not defensive boilerplate — the acceptance rule is unique across
// ALL seasons, and a season this couldn't read might hold the second match
// that would have made it ambiguous. Skipping the failed season instead would
// silently downgrade the guarantee to "unique across the seasons that
// happened to load" (contrast internal/api/discover_detail.go's soft-fail and
// internal/api/seriescatalog.go's continue-on-error — both fine for their own
// display/sync jobs, wrong for this acceptance path).
//
// Season 0 (Specials) is deliberately included, not filtered — see the
// plan's §1.3: Specials are over-represented in exactly the population this
// feature targets (unmarked episode-number files), and the uniqueness rule
// is what protects against a Specials/regular-season title collision.
//
// Sequential, one TVDetails + one SeasonDetails per season, with an early
// exit the instant Found reaches 2 — mirrors
// internal/api/seriescatalog.go's season loop. See the plan's §1.7 for why
// the bounded errgroup fan-out at internal/api/discover_detail.go was
// rejected for this path (fail-closed error handling gets awkward under an
// errgroup that deliberately swallows per-season errors, and it throws away
// the early-exit savings).
func searchEpisodeByTitle(ctx context.Context, client *tmdb.Client, tmdbID int, showTitle, basename string) episodeTitleSearch {
	det, err := client.TVDetails(ctx, tmdbID)
	if err != nil || len(det.Seasons) == 0 {
		return episodeTitleSearch{Incomplete: true}
	}
	seasons := make([]int, 0, len(det.Seasons))
	for _, s := range det.Seasons {
		seasons = append(seasons, s.SeasonNumber)
	}
	sort.Ints(seasons)

	var res episodeTitleSearch
	for _, season := range seasons {
		eps, err := client.SeasonDetails(ctx, tmdbID, season)
		if err != nil {
			return episodeTitleSearch{Incomplete: true} // uniqueness is now unprovable
		}
		for _, ep := range eps {
			if !episodeTitleMatches(basename, showTitle, ep.Name) {
				continue
			}
			m := &episodeTitleMatch{season: season, episode: ep.EpisodeNumber, name: ep.Name, airDate: ep.AirDate}
			res.Found++
			switch res.Found {
			case 1:
				res.Match = m
				res.First = m
			case 2:
				// ENFORCE the contract, don't merely document it: Match is
				// cleared at the INSTANT a second slot is found, before
				// returning — a caller that checked Match != nil first
				// (instead of Found >= 2 first) would otherwise accept an
				// ambiguous match. See this file's doc comment and the
				// plan's §1.1.
				res.Match = nil
				res.Second = m
				return res // early exit — uniqueness is already disproven
			}
		}
	}
	return res
}

// placeholderEpisodeNameRe matches TMDB's un-titled-episode placeholders
// ("Episode 12", "Ep. 3", "Chapter 4", "Part 2"). In-repo precedent for the
// shape: junkStemRe (web_authority.go), which rejects "Title12"/"Video3"
// basenames for the same class of reason.
var placeholderEpisodeNameRe = regexp.MustCompile(`(?i)^\s*(episode|ep|chapter|part|installment)\s*[.#]?\s*\d+\s*$`)

// isPlaceholderEpisodeName reports whether name is TMDB's stand-in for an
// un-titled episode — see the plan's §1.5(b) for why this must be rejected
// outright rather than left to the residual/containment rule: "episode" is 7
// runes (a "strong" token by HasTitleTokenOverlap's own bar) and survives the
// show-title subtraction untouched, so without this explicit reject a sparse
// season could produce a unique but meaningless false match.
func isPlaceholderEpisodeName(name string) bool {
	return strings.TrimSpace(name) == "" || placeholderEpisodeNameRe.MatchString(name)
}

// discriminating mirrors HasTitleTokenOverlap's own "strong || shared >= 2"
// bar (web_authority.go): one >=4-rune token, or two tokens of any length.
func discriminating(toks map[string]struct{}) bool {
	if len(toks) >= 2 {
		return true
	}
	for t := range toks {
		if len(t) >= 4 {
			return true
		}
	}
	return false
}

// subtractTokens returns the tokens in a that are not in b.
func subtractTokens(a, b map[string]struct{}) map[string]struct{} {
	out := make(map[string]struct{}, len(a))
	for t := range a {
		if _, ok := b[t]; !ok {
			out[t] = struct{}{}
		}
	}
	return out
}

// tokensSubsetOf reports whether every token in a is also in b.
func tokensSubsetOf(a, b map[string]struct{}) bool {
	for t := range a {
		if _, ok := b[t]; !ok {
			return false
		}
	}
	return true
}

// episodeTitleMatches is the match predicate, applied per episode. Order is
// cheapest-first; only the order's cost matters, not its correctness — see
// the plan's §1.4.
//
//  0. Reject TMDB placeholder names outright (isPlaceholderEpisodeName).
//  1. The spec-mandated reuse of the proven predicate, UNMODIFIED — this call
//     never changes today's outcome (if residual is discriminating and
//     contained in fileToks, HasTitleTokenOverlap's own bar necessarily
//     holds, since it computes fileToks identically and
//     titleTokens(episodeName) is a superset of residual). It stays because
//     (a) it is the literal reuse the spec's constraint names, and (b) it is
//     the cheap short-circuit that keeps the per-episode cost near-zero
//     across a long-running show. Do not delete it as dead logic, and do not
//     "fix" it thinking it is the real gate — steps 2-3 are.
//  2. Subtract the SHOW's own tokens from the episode's tokens before testing
//     anything else — without this, any episode whose TMDB name is a subset
//     of the show's own title (e.g. an episode also titled "The Path" on a
//     show called "The Path") would match every file in that folder, since
//     the recovered file title very often repeats the show's name (plan
//     §1.5(a)).
//  3. The residual must be "discriminating" (not just non-empty — see
//     discriminating's own doc comment) AND fully contained in the file's own
//     tokens. Containment, not equality, is the strictness rule: a file
//     "Red Skelton More Funny Faces.mp4" recovers to tokens that are a
//     SUPERSET of the episode "More Funny Faces"'s tokens, not equal to them
//     — exact-equality would reject the feature's own motivating shape.
func episodeTitleMatches(basename, showTitle, episodeName string) bool {
	if isPlaceholderEpisodeName(episodeName) {
		return false
	}
	if !HasTitleTokenOverlap(basename, episodeName) {
		return false
	}
	fileToks := titleTokens(searchterm.FromName(basename))
	showToks := titleTokens(showTitle)
	residual := subtractTokens(titleTokens(episodeName), showToks)
	if !discriminating(residual) {
		return false
	}
	return tokensSubsetOf(residual, fileToks)
}

// tryEpisodeTitleMatchSeries is the sole entry point into this file from
// rename.go — called ONLY from proposeOneEpisodeLibrary's
// library.ParseEpisodeNumbersLoose !ok branch, i.e. only once season/episode
// parsing has already failed by every means the rest of the pipeline has.
//
// Returns nil to mean "this path has nothing to say — fall through to the
// existing parse-failure Unmatched", never to mean "no proposal was built" in
// any other sense; every non-nil return is a fully-formed *proposals.Proposal
// (either Pending or a distinctly-reasoned Unmatched).
func tryEpisodeTitleMatchSeries(
	ctx context.Context, sess *mode.Session, tracked map[episodeKey]bool,
	pin pinnedShow, generalRoot, foundRoot, name string,
	base proposals.Proposal,
) *proposals.Proposal {
	// Claude 2026-08-06: precondition gate (plan §2.2)
	// Reason: this path spends the SAME "folder is already pinned to a show"
	//   confidence seriesGate's Check B runs on, to resolve a SECOND unknown
	//   (which episode) — it must never run for an unpinned folder (that
	//   would be a show+episode guess, the exact combined-guessing failure
	//   mode the spec forbids) or for a junk filename (IsJunkRenameFilename,
	//   reused unmodified from web_authority.go).
	// Troubleshooting: this path never fires — confirm pin.tmdbID > 0 for the
	//   folder in question (folderIDs[showFolderKey(...)] in rename.go).
	// Review if: a second "show already known" source (e.g. an NFO sidecar
	//   id) is ever added — an id that DISAGREES with the folder pin must
	//   reject, not pick a winner (plan §7).
	//
	// Claude 2026-08-07: pin.title == "" also declines (code-review HIGH finding)
	// Reason: library_series.title has no non-empty CHECK constraint and
	//   UpsertSeries writes it unconditionally, so a row with title=='' is
	//   reachable and can still acquire a path-feed pin (only the TITLE feed
	//   refuses an empty key — pinFolderID's path feed does not). A
	//   TVDetails-sourced Title fallback for that case would source Title
	//   from TMDB while Year still came from the row — the exact
	//   split-provenance defect §0.3 exists to prevent, just moved from Year
	//   to Title. Declining is the only choice consistent with "never source
	//   Title/Year from anything but the pinned row."
	// Troubleshooting: a title-matched Pending has a TMDB-sourced Title that
	//   doesn't match the show's tracked row — confirm this guard wasn't
	//   loosened back into a det.Title fallback.
	if pin.tmdbID <= 0 || pin.title == "" || sess.TMDB == nil || IsJunkRenameFilename(name) {
		return nil
	}

	// Claude 2026-08-06: gate.allows composition (plan §3)
	// Reason: the spec requires this match to still pass the existing gate
	//   checks, not bypass them. gate.allows evaluates true BY CONSTRUCTION
	//   here (pin.tmdbID > 0 and the candidate id IS pin.tmdbID, so the
	//   pinned-ID short-circuit in allows() fires every time) — so this call
	//   cannot reject anything today. It is kept anyway to couple this path
	//   to the gate's DEFINITION rather than a snapshot of it: a future
	//   Check C added to allows() is inherited automatically.
	// Troubleshooting: NEVER call gate.allowsTitle(...) directly here instead
	//   — Check A compares the query seed against the CANDIDATE's title, but
	//   on this path the seed (the recovered file/episode title) and the
	//   candidate (the show itself) are deliberately about different
	//   objects, so Check A alone would reject every legitimate match (see
	//   the plan's §3.2 worked example: seed "More Funny Faces" vs. show
	//   "The Red Skelton Show" shares zero tokens).
	// Review if: seriesGate.allows' short-circuit condition changes.
	gate := &seriesGate{titleSeed: name, pinnedTMDBID: pin.tmdbID, rejected: new(int)}
	if !gate.allows(pin.title, pin.tmdbID) {
		return nil // unreachable today by construction — see above
	}

	res := searchEpisodeByTitle(ctx, sess.TMDB, pin.tmdbID, pin.title, name)

	// Claude 2026-08-06: ENFORCE the three-outcome contract, don't merely
	//   document it (plan §1.1) — test Incomplete, then Found >= 2, then
	//   Match != nil, in that exact order, with an explicit default instead
	//   of relying on "Match is non-nil here by construction."
	// Troubleshooting: an ambiguous file lands Pending instead of Unmatched —
	//   confirm this switch's case order was not reordered or collapsed.
	var match *episodeTitleMatch
	switch {
	case res.Incomplete, res.Found == 0:
		return nil
	case res.Found >= 2:
		q := base
		q.Status = proposals.Unmatched
		q.Reason = fmt.Sprintf(
			"%q matches more than one episode of %q (S%02dE%02d %q and S%02dE%02d %q) — ambiguous, leaving in place for manual review",
			name, pin.title,
			res.First.season, res.First.episode, res.First.name,
			res.Second.season, res.Second.episode, res.Second.name,
		)
		return &q
	case res.Match != nil:
		match = res.Match
	default:
		// Cannot happen given searchEpisodeByTitle's construction (Found==1
		// always sets Match), but this is an ACCEPTANCE path — refuse rather
		// than assume.
		return nil
	}

	// Claude 2026-08-07: SITE 5 of 7 — tracked slot is now an alternate, not a decline (plan §5.2.4)
	// Reason: deep-interview-sakms-series-parsing-accuracy-improvements §5.2 —
	//   REPLACES (per the stale-comment rule, not edits) the 2026-08-06 block
	//   that stood here, whose premise was that a Pending on an already-tracked
	//   slot "overwrites that slot's library.Episode row ... silently orphaning
	//   the existing file." That premise is now FALSE: ApplyLibrarySeries folds
	//   a second file into library_episode_files as an alternate, resolving the
	//   collision against LIVE DB state (existing.FilePath != "") rather than a
	//   Scan-time snapshot, promoting or demoting by probed tier. So the guard
	//   still fires — it just records "this is an alternate" instead of
	//   declining. This is the Red Skelton population's path (the compact-code
	//   parser now delivers those files here rather than to the parse-failure
	//   branch), so hard-declining here would leave that whole population
	//   Unmatched.
	//   The duplicate flag is recorded HERE and applied at the END so the
	//   pin-sourced Title/Year population below runs untouched —
	//   acceptDuplicatePendingEpisode writes only Status+Reason, which is
	//   precisely what keeps §0.3's blocking Title/Year-from-pin rule safe.
	// Troubleshooting: an Apply from this path clobbered an existing tracked
	//   episode instead of folding in as an alternate — the Apply-side fold-in
	//   gate (existing.FilePath != "") did not fire; check §0.3.
	// Review if: operators report unwanted alternates accumulating and want
	//   same-slot duplicates declined again.
	duplicateSlot := tracked[episodeKey{tmdbID: pin.tmdbID, season: match.season, episode: match.episode}]

	// Claude 2026-08-06: Kids routing without a second classify.WithAI call (plan §2.4)
	// Reason: the show is already tracked, which means it was already
	//   Kids-classified once, at the Apply that created its row —
	//   library.Series.RootFolderPath (the LIBRARY root, §0.4) records that
	//   answer. Re-asking an AI here could disagree with the folder the show
	//   already lives in, reproducing exactly the split-folder outcome §0.3
	//   exists to prevent, for the cost of an AI round trip per file.
	// Troubleshooting: a title-matched file lands under the wrong root —
	//   confirm pin.root actually reflects the tracked series' RootFolderPath
	//   (rename.go's seriesByID lookup), not a stale/zero value.
	// Claude 2026-08-07: non-empty guard on the pin.root branch (code-review
	//   MEDIUM finding)
	// Reason: every other Kids-routing branch in this codebase
	//   (rename.go:256, 292, 835, 925, 1215, 1366, 1406) guards on
	//   sess.KidsRootPath != "" before comparing. Without it, an unconfigured
	//   Kids root ("") compared against a degenerate/legacy pin.root == ""
	//   would both be empty, the branch would fire, and targetRoot would
	//   become "" — RelocateEpisodeRange would then build a relative
	//   destination path instead of refusing.
	// Troubleshooting: a title-matched file lands with an empty
	//   RootFolderPath — confirm this guard wasn't dropped again.
	targetRoot := generalRoot
	switch {
	case foundRoot == sess.KidsRootPath:
		targetRoot = sess.KidsRootPath // where the file was found wins (matches acceptSeries)
	case sess.KidsRootPath != "" && pin.root == sess.KidsRootPath:
		targetRoot = sess.KidsRootPath
	}

	// Claude 2026-08-06: Title/Year sourced from the pinned row, NEVER from
	//   TVDetails (plan §0.3 — the blocking data-corruption fix this whole
	//   feature was gated on).
	// Reason: TVDetails carries no year (Year would be 0), and a Year==0
	//   proposal builds a DIFFERENT folder name at Apply (naming.SeriesFolderName
	//   omits the year when it's 0) — splitting the show's existing folder in
	//   two and, via UpsertSeries' unconditional overwrite, blanking the
	//   tracked row's year for every future Apply too. normalizeShowKey
	//   strips both the year and the tmdbid tag, so the folder guard cannot
	//   see this drift on its own.
	// Troubleshooting: a second folder appears for an already-tracked show —
	//   confirm p.Title/p.Year below still read from pin, not from det.
	// Review if: never — see the plan's "explicitly rejected" note on adding
	//   FirstAirDate to TVDetails instead.
	//
	// Claude 2026-08-07: no det.Title fallback — Title comes ONLY from pin (code-review HIGH finding)
	// Reason: pin.title == "" is now refused outright by the precondition
	//   gate above, so there is no remaining caller-visible case where a
	//   TVDetails-sourced Title would ever run. Title/Year must come from
	//   the pinned row with NO exceptions — a fallback here, even a
	//   "defensive only" one, is the same split-provenance hole §0.3 exists
	//   to prevent, just moved from Year to Title. Do not reintroduce one.
	det, detErr := sess.TMDB.TVDetails(ctx, pin.tmdbID)

	p := base
	p.Status = proposals.Pending
	p.Title = pin.title
	p.TMDBID = pin.tmdbID
	p.Year = pin.year // never fall back to TVDetails — see above
	p.SeasonNumber = match.season
	p.EpisodeNumber = match.episode
	// ExtraEpisodeNumbers stays nil: a title match resolves exactly one slot.
	p.RootFolderPath = targetRoot
	p.Reason = fmt.Sprintf("%s %q -> S%02dE%02d", episodeTitleMatchReasonPrefix, match.name, match.season, match.episode)
	if detErr == nil {
		p.Genres = det.Genres
	}
	if names, err := sess.TMDB.TVAggregateCredits(ctx, pin.tmdbID); err == nil {
		p.Cast = names
	}
	// Site 5's softened outcome — Status+Reason only, so the pin-sourced
	// Title/Year above survive verbatim. Note this REPLACES the
	// episodeTitleMatchReasonPrefix reason set a few lines up; when this
	// function is reached via aiEpisodeMode1 that caller then wraps the result,
	// so those rows read "<aiPrefix> ...; alternate: ..." rather than starting
	// with "alternate:".
	if duplicateSlot {
		acceptDuplicatePendingEpisode(&p, pin.title, match.season, match.episode)
	}
	return &p
}
