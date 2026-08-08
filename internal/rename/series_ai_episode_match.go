// Claude 2026-08-07: AI-assisted Series episode recovery — the POST-PASS half
//
//	of C2 (autopilot-impl.md §2.5). Asks the already-wired AI client to recover
//	an EPISODE TITLE from an opaque, foreign-language or typo'd Series
//	filename, then feeds that corrected title into the EXISTING acceptance
//	machinery — tryEpisodeTitleMatchSeries (mode 1) or searchTVDBEpisodeByTitle
//	against a catalog tvdbAnthologyPass already fetched (mode 2).
//
// Reason: an INLINE attempt inside proposeOneEpisodeLibrary was rejected
//
//	(§2.1): a file the inline site resolved returns parseFailed == false and
//	drops out of anthologyIdx, which can pull a folder below
//	minCorroboratingFiles and pre-empt STRONGER multi-file deterministic
//	evidence for every other file in it. Running strictly AFTER anthology and
//	consuming its results makes that inversion structurally impossible:
//	deterministic first, model last.
//
// Troubleshooting: an eligible row is never spoken for — check, in order:
//
//	sess.MainstreamAI non-nil; the index present in candidates; handled[i]
//	false; showFolderKey non-empty; the key in folderIDs (mode 1) or in
//	resolved (mode 2) — a key in neither is MODE 3, which is DEFERRED scope and
//	costs zero AI calls by construction.
//
// Review if: MODE 3 (§2.6) is ever approved — it is the only mode that can
//
//	name a WRONG SHOW, so it needs its own dual-uniqueness acceptance bar
//	rather than an extra case in the switch below.
//
// Related files: internal/identify/series_prompts.go (the prompt),
//
//	internal/rename/series_tvdb_episode_match.go (mode 2's catalog + guards),
//	internal/rename/series_episode_title_match.go (mode 1, reused UNMODIFIED)
//
// Context: see .omc/plans/autopilot-impl.md §2.5
package rename

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/labbersanon/sakms/internal/identify"
	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/proposals"
	"github.com/labbersanon/sakms/internal/searchterm"
)

// aiEpisodeMatchReasonPrefix marks a reason as having come from this path,
// mirroring episodeTitleMatchReasonPrefix (series_episode_title_match.go) and
// tvdbAnthologyReasonPrefix (series_tvdb_episode_match.go).
//
// Every reason this pass writes carries it — Pending emits and both declines —
// so an operator can tell an AI-derived row from a deterministic one by
// grepping, without re-running a scan.
const aiEpisodeMatchReasonPrefix = "AI-assisted match:"

// dvdAuthoringStemRe matches a raw DVD-authoring filename stem — VIDEO_TS and
// VTS_NN_N, the structural container names a ripped DVD leaves behind. These
// carry ZERO title information and are exactly the shape a 7B model
// confabulates a confident wrong answer for.
//
// DELIBERATELY LOCAL to this file. Do NOT fold these into junkStemRe
// (web_authority.go) — Movies and Adult both consume that shared predicate,
// and widening it changes their behaviour for a Series-only problem.
//
// The separator class is [_ -] rather than _ because isDVDAuthoringName tests
// TWO different renderings of the same name and they use different separators.
var dvdAuthoringStemRe = regexp.MustCompile(`(?i)^\s*(vts[_ -]\d+[_ -]\d+|video[_ -]ts)\s*$`)

// isDVDAuthoringName reports whether name is a DVD-authoring container name
// rather than a title.
//
// Empirically verified 2026-08-07: IsJunkRenameFilename returns FALSE (i.e.
// non-junk) for BOTH real filenames — searchterm.FromName("VTS_01_0.VOB")
// yields "VTS 01 0", whose last token "0" junkStemRe does not match. Without
// this guard all 6 Candid Camera DVD-authoring files reach the model.
//
// BOTH forms are tested because neither alone covers both real filenames:
//
//	VTS_01_0.VOB -> raw stem "VTS_01_0",  searchterm.FromName "VTS 01 0"
//	VIDEO_TS.VOB -> raw stem "VIDEO_TS",  searchterm.FromName "VIDEO TS"
func isDVDAuthoringName(name string) bool {
	stem := strings.TrimSuffix(filepath.Base(name), filepath.Ext(name))
	if dvdAuthoringStemRe.MatchString(stem) {
		return true
	}
	return dvdAuthoringStemRe.MatchString(searchterm.FromName(name))
}

// resolvedHas is a two-value map lookup, deliberately NOT a zero-value test.
//
// tvdbAnthologyPass writes resolved[key] ONLY for a folder that resolved to
// exactly one qualifying candidate; a folder it declined as ambiguous gets no
// entry at all. Presence is therefore the exact question mode 2 must ask, and
// a value-shaped test (e.g. `resolved[key].catalog != nil`) answers a
// different one that only coincides by accident of today's writes.
func resolvedHas(resolved map[string]anthologyResolution, key string) bool {
	_, ok := resolved[key]
	return ok
}

// aiEpisodeRecoveryPass is a POST-PASS, mirroring tvdbAnthologyPass
// argument-for-argument. It runs after that pass (so deterministic multi-file
// evidence always wins, and so its results are consumable here) and before the
// `seen` collision loop (so every row it writes is covered by the within-batch
// episodeKey guard for free) — plan §0.8, §2.2.
//
// candidates holds INDICES into out, supplied structurally by the caller —
// never derived by matching on out[i].Reason.
//
// Writes directly into out. Returns nothing: unlike proposeOneEpisodeLibrary
// there is no "fall through to the next path" caller to signal. It must NEVER
// append to out.
//
// Cost: at most ONE identify.ParseSeriesEpisode call per eligible row, and
// ZERO new catalog calls in mode 2 (it reuses resolved[key].catalog).
func aiEpisodeRecoveryPass(
	ctx context.Context, sess *mode.Session,
	tracked map[episodeKey]bool,
	seriesByID map[int]library.Series,
	folderIDs map[string]int,
	resolved map[string]anthologyResolution,
	handled map[int]bool,
	roots []string, generalRoot string,
	candidates []int,
	out []proposals.Proposal,
) {
	// Pass-level preconditions. The MainstreamAI nil check IS the
	// ai_fallback_enabled gate: buildAIClient returns a nil client when the
	// setting is unset or not "true" (§0.1), so a disabled install returns here
	// before any prompt is built.
	if sess == nil || sess.MainstreamAI == nil {
		return
	}
	if sess.TMDB == nil {
		return
	}
	if len(candidates) == 0 {
		return
	}

	for _, i := range candidates {
		// Per-row preconditions, IN THIS ORDER, all of them BEFORE any AI call.
		if i < 0 || i >= len(out) {
			continue
		}
		// tvdbAnthologyPass already spoke about this row (§2.3). Skipping is
		// what protects its PRECISE reason — an ambiguity decline, or an
		// already-in-the-library decline — from being overwritten with this
		// path's vaguer one. Both of those rows are Unmatched with TMDBID == 0,
		// so the eligibility test below cannot catch them; this bit is the only
		// thing that can. STRUCTURAL, by index — never by parsing out[i].Reason.
		if handled[i] {
			continue
		}
		if out[i].Status != proposals.Unmatched || out[i].TMDBID != 0 {
			continue
		}
		name := out[i].SourceName
		if strings.TrimSpace(name) == "" {
			continue
		}
		if IsJunkRenameFilename(name) { // reused UNMODIFIED from web_authority.go
			continue
		}
		if isDVDAuthoringName(name) {
			continue
		}

		// Mode selection runs BEFORE the AI call, deliberately. It is a pure
		// map lookup with no I/O, while the call costs a model round trip — so
		// a MODE 3 row, which this round declines to handle (§2.6), never pays
		// for a result that would be discarded. Reordering these two makes the
		// deferral invisible in the cost profile.
		key := showFolderKey(out[i].SourcePath, roots)
		aiMode := 0
		switch {
		case key == "":
			continue // no show folder — nothing to resolve against
		case folderIDs[key] > 0:
			aiMode = 1 // pinned to a tracked TMDB show (§2.5.5)
		case resolvedHas(resolved, key):
			aiMode = 2 // resolved by tvdbAnthologyPass THIS scan (§2.5.6)
		default:
			// MODE 3 — DEFERRED, confirmed by Wade 2026-08-07 (§2.6). This
			// `continue` is the entire implementation, and it sits BEFORE the
			// AI call so such a row costs nothing at all. Do NOT add a show
			// search here without a separate explicit go-ahead: modes 1 and 2
			// can only ever be wrong about WHICH EPISODE of an
			// already-established show; mode 3 is the only one that can name a
			// wrong SHOW.
			continue
		}

		// showFolderName, NEVER filepath.Base(filepath.Dir(...)): for
		// .../The Red Skelton Show/Uncompressed/RED SKELTON e1524.mp4 the
		// second form yields "Uncompressed", a nested subdirectory carrying no
		// title information that actively misleads the model. showFolderName
		// shares showFolderKey's traversal, which is what guarantees the folder
		// NAME handed to the model and the folder KEY used for mode selection
		// can never describe different directories.
		folder := showFolderName(out[i].SourcePath, roots)

		// The error is deliberately discarded: ParseSeriesEpisode soft-fails to
		// a ZERO struct on every non-success path (§2.4), and returns a zero
		// struct alongside any real error too — so the empty-EpisodeTitle check
		// below is the single decline point for both.
		//
		// guess.SeasonNumber / guess.EpisodeNumber are NOT READ here or
		// anywhere below. They are §2.4's numeric sink: they exist so a number
		// the model believes it sees does not contaminate a title field. Every
		// slot this pass emits comes from a CATALOG MATCH.
		guess, _ := identify.ParseSeriesEpisode(ctx, sess.MainstreamAI, name, folder)
		if strings.TrimSpace(guess.EpisodeTitle) == "" {
			continue
		}

		switch aiMode {
		case 1:
			aiEpisodeMode1(ctx, sess, tracked, seriesByID, folderIDs, key,
				generalRoot, name, guess.EpisodeTitle, i, out)
		case 2:
			aiEpisodeMode2(sess, tracked, resolved[key], folder,
				generalRoot, name, guess.EpisodeTitle, i, out)
		}
	}
}

// aiEpisodeMode1 places a file whose show folder is pinned to a TRACKED TMDB
// show, by handing the AI's recovered episode title to
// tryEpisodeTitleMatchSeries — reused COMPLETELY UNMODIFIED, in the argument
// position that normally receives the filename-derived title.
//
// That reuse is the single most valuable decision in this design. It inherits,
// already shipped and already tested: episodeTitleMatches' uniqueness-across-
// catalog rule, fail-closed handling of an Incomplete catalog read, Season 0 /
// Specials support, the tracked-slot collision guard, the Kids-routing guard
// including its sess.KidsRootPath != "" check, gate composition, and Title/Year
// sourced from the PINNED ROW ONLY (no split provenance).
//
// NO changes to series_episode_title_match.go are made or needed: the pin here
// is a REAL tracked row (folderIDs[key] > 0), so the pin.tmdbID <= 0 guard that
// an earlier draft wanted to work around never fires.
func aiEpisodeMode1(
	ctx context.Context, sess *mode.Session,
	tracked map[episodeKey]bool,
	seriesByID map[int]library.Series,
	folderIDs map[string]int,
	key, generalRoot, name, episodeTitle string,
	i int, out []proposals.Proposal,
) {
	// Mirrors ScanLibrarySeries' own pin construction (rename.go): Title/Year/
	// RootFolderPath come from the tracked row or not at all.
	pin := pinnedShow{tmdbID: folderIDs[key]}
	if s, ok := seriesByID[pin.tmdbID]; ok {
		pin.title, pin.year, pin.root = s.Title, s.Year, s.RootFolderPath
	}

	// foundRoot is the root the file was FOUND under, read BEFORE any rewrite —
	// the same pattern tvdbAnthologyPass uses. Passing generalRoot in this
	// argument position instead silently breaks Kids routing: the first branch
	// of tryEpisodeTitleMatchSeries' switch would then compare the general root
	// against the Kids root rather than comparing where the file actually is.
	foundRoot := out[i].RootFolderPath

	// out[i] is the base proposal, carrying Mode/Workflow/SourceName/
	// SourcePath. A fresh proposals.Proposal{} would compile and produce a
	// plausible-looking Pending with a blank SourceName and SourcePath.
	got := tryEpisodeTitleMatchSeries(ctx, sess, tracked, pin, generalRoot, foundRoot, episodeTitle, out[i])
	if got == nil {
		return
	}

	// PREFIX ONLY — never parse, reconstruct or rewrite the underlying reason.
	// That preserves the existing diagnostic grep-ability of every reason string
	// that path already emits, and it applies to EVERY non-nil return
	// (the ambiguity and tracked-slot declines included), not just the Pending.
	got.Reason = fmt.Sprintf("%s recovered episode title %q from %q; %s",
		aiEpisodeMatchReasonPrefix, episodeTitle, name, got.Reason)
	out[i] = *got
}

// aiEpisodeMode2 places a file whose show folder tvdbAnthologyPass resolved in
// THIS SAME scan, by matching the AI's recovered episode title against that
// pass's ALREADY-FETCHED catalog. Zero new catalog calls.
//
// This is the mode that actually closes the target population — the foreign-
// language and typo'd Laurel & Hardy titles (Politiquerias, Ladrones,
// "Thier First Mistake"). Stated plainly because mode 1 looks like the "main"
// mode and is not the one that matters here: Laurel & Hardy is TheTVDB-only,
// with no TMDB TV entry at all, so any design routing this population through
// TMDB is a design that cannot work.
func aiEpisodeMode2(
	sess *mode.Session,
	tracked map[episodeKey]bool,
	r anthologyResolution,
	folder, generalRoot, name, episodeTitle string,
	i int, out []proposals.Proposal,
) {
	// r.candName, NEVER r.pin.title — a correctness requirement, not a style
	// preference. searchTVDBEpisodeByTitle passes this argument straight into
	// episodeTitleMatches, which uses it to compute a SUBTRACTION RESIDUAL: it
	// strips the show's own tokens out of each episode name before testing
	// containment. Feed it a different string than the catalog was scanned with
	// and the same catalog yields a DIFFERENT match set, silently. pin.title is
	// overwritten by the TRACKED ROW's title whenever the established-pin
	// shortcut fires (series_tvdb_episode_match.go), so on any rescan of an
	// already-applied anthology show the two are genuinely different strings.
	res := searchTVDBEpisodeByTitle(r.catalog, r.candName, episodeTitle)

	// The three-outcome contract, in the documented order.
	var m *tvdbEpisodeMatch
	switch {
	case res.Found >= 2:
		// Declines SILENTLY, and deliberately so: unlike tvdbAnthologyPass's
		// own ambiguity branch, the ambiguous string here is a MODEL's guess,
		// not the file's own name. Rewriting the row's reason around it would
		// present a confabulation as evidence. The verbatim parse-failure
		// Unmatched survives instead.
		return
	case res.Found == 1 && res.Match != nil:
		m = res.Match
	default:
		// Found == 0, or the defensive Match == nil that
		// searchTVDBEpisodeByTitle's construction makes unreachable. This is an
		// ACCEPTANCE path — refuse rather than assume.
		return
	}

	// Claude 2026-08-07: SITE 7 of 7 — tracked slot is now an alternate, not a decline (plan §5.2.6)
	// Reason: deep-interview-sakms-series-parsing-accuracy-improvements §5.2 —
	//   REPLACES (per the stale-comment rule, not edits) the block that stood
	//   here, whose premise was that a Pending on an already-tracked slot
	//   "overwrites that slot's library_episodes row ... and silently orphans
	//   the existing file." That premise is now FALSE: ApplyLibrarySeries folds
	//   a second file into library_episode_files as an alternate, resolving the
	//   collision against LIVE DB state (existing.FilePath != "") rather than a
	//   Scan-time snapshot. Same shape as Site 6, operating on out[i]: record
	//   the duplicate here, let the gate/Kids-routing/emit below run unchanged,
	//   and overwrite only Status+Reason at the end.
	//   NEEDS NO handled[i] WRITE, and that asymmetry with Site 6 is correct —
	//   do not "fix" it. This pass never writes handled (the map appears in
	//   this file only as a read at :157 and in the file doc), because nothing
	//   runs after it that consults the map.
	//   DELIBERATE, DOCUMENTED LOSS: acceptDuplicatePendingEpisode writes Reason
	//   WHOLESALE, so the aiEpisodeMatchReasonPrefix the retired branch built
	//   inline is GONE from a softened row. That is a real, if minor, loss of
	//   grep-ability, accepted rather than worked around: re-prefixing would
	//   have to happen at this site only, and changing the helper is not an
	//   option — seven sites share it. Contrast aiEpisodeMode1 (:282-283),
	//   which WRAPS rather than replaces and therefore keeps its prefix, so the
	//   two AI-recovery modes now produce different reason shapes on purpose.
	// Troubleshooting: an AI-recovered duplicate Applied and OVERWROTE the
	//   slot's library_episodes row instead of folding in — the Apply-side
	//   fold-in gate (existing.FilePath != "") did not fire; check §0.3.
	// Review if: operators report unwanted alternates accumulating and want
	//   same-slot duplicates declined again.
	duplicateSlot := tracked[episodeKey{tmdbID: r.pin.synthID, season: m.season, episode: m.episode}]

	// Gate composition, seeded with the SHOW FOLDER NAME exactly as
	// tvdbAnthologyPass seeds it. NEVER re-seed with the filename: the filename
	// is the thing that was unusable, and Check A would then compare an opaque
	// or foreign-language basename against the show's title and veto every
	// legitimate match. Check A with the folder-name seed is the entire gate on
	// this path (allowsID fails open at its pinnedTMDBID <= 0 guard) — it is
	// kept so a future Check C added to allows() is inherited automatically.
	gate := &seriesGate{titleSeed: folder, rejected: new(int)}
	if !gate.allows(r.pin.title, r.pin.synthID) {
		return // allows() increments the shared counter itself — do not reject() again
	}

	// Kids routing, mirroring tvdbAnthologyPass's own block exactly. foundRoot
	// is read BEFORE the row is rewritten below — reading it after would make
	// the first branch compare against itself.
	//
	// The non-empty sess.KidsRootPath guard on the SECOND branch is REQUIRED (a
	// shipped code-review MEDIUM finding on both sibling paths): without it an
	// unconfigured Kids root "" compared against a degenerate pin.root == ""
	// both match, targetRoot becomes "", and RelocateEpisodeRange builds a
	// relative destination instead of refusing. The first branch deliberately
	// carries NO such guard — it is byte-identical to both siblings, and adding
	// one there would diverge this path from every other Kids branch in the
	// package.
	foundRoot := out[i].RootFolderPath
	targetRoot := generalRoot
	switch {
	case foundRoot == sess.KidsRootPath:
		targetRoot = sess.KidsRootPath // where the file was found wins (matches acceptSeries)
	case sess.KidsRootPath != "" && r.pin.root == sess.KidsRootPath:
		targetRoot = sess.KidsRootPath
	}

	q := out[i] // preserve Mode/Workflow/SourceName/SourcePath
	q.Status = proposals.Pending
	q.Title = r.pin.title
	q.TMDBID = r.pin.synthID // negative synthetic
	q.TVDBID = r.pin.tvdbID  // the REAL TheTVDB series id
	q.Year = r.pin.year
	q.SeasonNumber = m.season
	q.EpisodeNumber = m.episode
	// ExtraEpisodeNumbers stays nil: a title match resolves exactly one slot.
	q.RootFolderPath = targetRoot
	// No Genres/Cast enrichment, mirroring anthology's own emit: there is no
	// TMDB id here to call TVDetails/TVAggregateCredits with, and inventing one
	// would be the split-provenance defect again.
	q.Reason = fmt.Sprintf("%s recovered episode title %q from %q -> %q S%02dE%02d (tvdb %d)",
		aiEpisodeMatchReasonPrefix, episodeTitle, name, m.name, m.season, m.episode, r.pin.tvdbID)
	// Site 7's softened outcome — Status+Reason only; the emit's
	// Title/TMDBID/TVDBID/Year/Root above stay as an ordinary match's would be.
	// This is where aiEpisodeMatchReasonPrefix is lost, deliberately (see the
	// SITE 7 block above).
	if duplicateSlot {
		acceptDuplicatePendingEpisode(&q, r.pin.title, m.season, m.episode)
	}
	out[i] = q
}
