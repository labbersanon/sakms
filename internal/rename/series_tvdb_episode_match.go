// Claude 2026-08-07: TheTVDB-sourced chronological anthology matching for
//
//	Series files a folder pin alone cannot place — the shared show-folder
//	traversal (§2.4), the synthetic negative TMDB id (§4), the pure
//	per-catalog episode-title search (§2), and the multi-file
//	self-corroboration post-pass over ScanLibrarySeries' proposal slice (§3).
//
// Reason: autopilot-impl (C1) — the anthology pass needs the show folder's
//
//	NAME as a TheTVDB query seed while grouping files on the show folder's
//	KEY, and the two MUST describe the same directory. Deriving the name
//	independently (filepath.Base(filepath.Dir(path))) silently diverges the
//	moment a file is nested one level deeper than the show folder, which real
//	library data does today.
//
// Troubleshooting: a whole show folder no-ops, or (worse) pins to an
//
//	unrelated show whose name happens to match a subdirectory — check that
//	the gate was seeded from showFolderName and not from the file's immediate
//	parent directory.
//
// Review if: library_series gains a real per-show folder-path column, at
//
//	which point neither derivation needs to walk the path at all.
//
// Related files: internal/rename/series_folder_guard.go (showFolderKey is
//
//	the other wrapper over showFolderSegment)
//
// Context: see .omc/plans/autopilot-impl.md §2.4
package rename

import (
	"context"
	"fmt"
	"hash/fnv"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/proposals"
	"github.com/labbersanon/sakms/internal/tvdb"
)

// showFolderSegment returns the RAW, UNNORMALIZED first path segment below
// whichever configured root contains path — i.e. the show folder's own
// directory name — or "" when path is not under any known root, or sits
// directly in a root with no show-folder segment at all.
//
// This is showFolderKey's traversal, lifted VERBATIM out of it: that
// function's body is now a single normalizeShowKey call over this one. It
// exists as a shared function rather than as a second copy because the
// anthology pass needs the folder NAME as a TVDB query seed while grouping
// on the folder KEY, and the two MUST describe the same directory. A
// separate filepath.Base(filepath.Dir(path)) derivation silently diverges
// the moment a file is nested one level deeper — which real library data
// does today: Series/The Red Skelton Show/Uncompressed/ (10 of 13 files)
// and Series/CANDID CAMERA GREATEST MOMENTS/Candid_Cameras_Greatest_Moments/
// (all 6). Seeding the gate with "Uncompressed" instead of the show folder
// name is a fail-OPEN bug, not a cosmetic one — a subdirectory name that
// happens to be a real show title would pass Check A against itself.
//
// Roots are matched longest-first so a nested root can never be shadowed by
// a shorter one, and a bare strings.HasPrefix is deliberately avoided: root
// "/media/Series" must not match the unrelated sibling path
// "/media/Series2/Show/ep.mkv". filepath.Rel plus rejecting any ".."-prefixed
// result is what enforces the separator boundary correctly.
func showFolderSegment(path string, roots []string) string {
	clean := filepath.Clean(path)
	sortedRoots := append([]string(nil), roots...)
	sort.Slice(sortedRoots, func(i, j int) bool {
		return len(sortedRoots[i]) > len(sortedRoots[j])
	})
	for _, root := range sortedRoots {
		if root == "" {
			continue
		}
		rel, err := filepath.Rel(filepath.Clean(root), clean)
		if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
			continue
		}
		segs := strings.SplitN(rel, string(os.PathSeparator), 2)
		if len(segs) < 2 {
			return "" // sits directly in this root — no show-folder segment
		}
		return segs[0]
	}
	return ""
}

// showFolderName is showFolderSegment under the name this feature's call
// sites use: the show folder's directory name, exactly as it appears on
// disk, for use as a TheTVDB query string and as the seriesGate titleSeed.
//
// An empty showFolderName IMPLIES an empty showFolderKey, which is what
// makes a skip-on-empty guard sound. The converse does NOT hold — a folder
// named "(2016)" has a non-empty name and an empty key — so do not read
// this as a biconditional.
func showFolderName(path string, roots []string) string { return showFolderSegment(path, roots) }

// anthologyTMDBID returns a stable NEGATIVE synthetic TMDB id for a TheTVDB
// series that has no legitimate TMDB TV entry, satisfying
// library_series.tmdb_id's NOT NULL / UNIQUE constraint without fabricating
// a wrong real id.
//
// This follows WebAuthorityTMDBID (web_authority.go:176-183) EXACTLY: same
// fnv32a hash, same `int(h.Sum32()&0x7fffffff) + 1` fold (the +1 makes 0
// unreachable, so the result is always strictly negative and can never
// collide with pinFolderID's sticky-ambiguous 0 sentinel or with a real
// positive TMDB id), same negation. web_authority.go itself is NOT modified
// -- only its pattern is followed, per the spec's constraint.
//
// It differs from WebAuthorityTMDBID in its INPUT, deliberately: identity
// here is TheTVDB's series id, which is stable, rather than a (title, year)
// pair, which is not -- TheTVDB can rename a series or correct its year
// without changing its id, and a shifting synthetic id would orphan every
// already-applied episode of that show behind a UNIQUE constraint that no
// longer matches.
//
// The "tvdb-anthology:" domain-separation prefix keeps this function's
// output space distinct from WebAuthorityTMDBID's for the same underlying
// string. Both draw from the same 2^31 negative range, so a collision is
// theoretically possible; at the handful-of-rows scale this feature operates
// at, the probability is negligible, and the consequence (two unrelated
// shows sharing one library_series row via ON CONFLICT(tmdb_id)) is recorded
// here as a known, accepted risk rather than left undocumented.
func anthologyTMDBID(tvdbID int) int {
	h := fnv.New32a()
	_, _ = h.Write([]byte("tvdb-anthology:"))
	_, _ = h.Write([]byte(strconv.Itoa(tvdbID)))
	v := int(h.Sum32()&0x7fffffff) + 1
	return -v
}

// tvdbAnthologyReasonPrefix marks a Pending/Unmatched reason as having come
// from this path, mirroring episodeTitleMatchReasonPrefix
// (series_episode_title_match.go) and webMatchReasonPrefix (web_authority.go).
const tvdbAnthologyReasonPrefix = "tvdb anthology match:"

// tvdbEpisodeMatch is one (season, episode) slot whose TVDB episode Name
// matched the recovered file title.
type tvdbEpisodeMatch struct {
	season  int
	episode int
	name    string // the tvdb.Episode.Name that matched
	airDate string
}

// tvdbEpisodeSearch is the result of scanning ONE show's whole catalog.
//
// Deliberately has NO Incomplete field, and the absence is the design, not an
// omission: this search runs over an ALREADY-FETCHED, ALREADY-COMPLETE
// []tvdb.Episode. Incompleteness is impossible here because the fetch either
// produced the whole catalog or produced an error, and the error aborts the
// WHOLE FOLDER before any file is searched (see tvdbAnthologyPass). That moves
// fail-closed handling up one level from the TMDB sibling
// (episodeTitleSearch.Incomplete) — which is strictly stronger, since a fetch
// failure there could decline one file while its neighbours proceeded on a
// possibly-different cache state.
//
//   - Match != nil               -> exactly one slot matched; accept
//   - Match == nil && Found >= 2 -> ambiguous; Unmatched with a DISTINCT reason
//   - Match == nil && Found == 0 -> no match; leave the row untouched
type tvdbEpisodeSearch struct {
	Match  *tvdbEpisodeMatch
	Found  int               // capped at 2 — stops as soon as ambiguity is proven
	First  *tvdbEpisodeMatch // 1st slot, for the ambiguity reason string
	Second *tvdbEpisodeMatch // 2nd slot, for the ambiguity reason string
}

// searchTVDBEpisodeByTitle scans catalog (one show's COMPLETE episode list)
// for slots whose Name matches basename, and enforces the uniqueness-across-
// the-whole-show rule.
//
// Signature takes a []tvdb.Episode, NOT a *tvdb.Client, for three reasons:
//  1. the corroboration pass must run N files against ONE catalog, so the
//     fetch has to be hoisted out of the per-file loop anyway;
//  2. it makes this function pure and server-free to unit-test;
//  3. it makes "the catalog is complete" a precondition the caller owns,
//     which is what tvdbEpisodeSearch's doc comment explains.
//
// Catalog ORDER is irrelevant and must stay irrelevant: do not sort catalog
// and do not short-circuit on season boundaries. The acceptance rule is
// uniqueness across the WHOLE show, so the loop must visit every element
// unless Found reaches 2. First/Second exist for the reason string only and
// carry no "preferred slot" semantics.
func searchTVDBEpisodeByTitle(catalog []tvdb.Episode, showTitle, basename string) tvdbEpisodeSearch {
	var res tvdbEpisodeSearch
	for _, ep := range catalog {
		// episodeTitleMatches is REUSED VERBATIM from
		// series_episode_title_match.go — the spec's stated constraint, and
		// empirically correct against the real Laurel & Hardy filenames. It
		// already rejects empty/placeholder names, subtracts the show's own
		// tokens, and requires the discriminating residual to be contained in
		// the file's tokens. Do NOT fork a TVDB-specific copy of it.
		if !episodeTitleMatches(basename, showTitle, ep.Name) {
			continue
		}
		m := &tvdbEpisodeMatch{season: ep.SeasonNumber, episode: ep.Number, name: ep.Name, airDate: ep.Aired}
		res.Found++
		switch res.Found {
		case 1:
			res.Match = m
			res.First = m
		case 2:
			// ENFORCE the contract, don't merely document it: Match is cleared
			// at the INSTANT a second slot is found, BEFORE returning. A caller
			// that tested Match != nil before Found >= 2 would otherwise accept
			// an ambiguous match. Same contract, same enforcement point, as
			// searchEpisodeByTitle (series_episode_title_match.go).
			res.Match = nil
			res.Second = m
			return res // early exit — uniqueness is already disproven
		}
	}
	return res
}

// anthologyPin is the single value object a corroborated folder resolves to.
//
// Computed EXACTLY ONCE per folder group and threaded to every file in that
// group. NOT recomputed per file, and this is not a micro-optimisation:
// ApplyLibrarySeries feeds Proposal.Title/Year straight into
// naming.SeriesFolderName AND into UpsertSeries' unconditional
// `title = excluded.title, year = excluded.year, root_folder_path =
// excluded.root_folder_path` (internal/library/library_series.go). If two files
// in one group carried different Title/Year, the second Apply would rename the
// show's folder out from under the first and rewrite the tracked row to match —
// the same class of split-folder corruption the TMDB sibling's pinnedShow
// exists to prevent, reached from the other direction (there the tracked row
// was the authority; here there IS no tracked row on first pin, so TheTVDB is,
// and it must speak with ONE voice per folder).
type anthologyPin struct {
	tvdbID  int
	synthID int    // negative synthetic tmdb id — see anthologyTMDBID
	title   string // the folder-name authority for this show
	year    int
	root    string // target root folder path (established rows only)
}

const (
	// minCorroboratingFiles is how many DISTINCT episode slots in one
	// candidate show's catalog must be claimed by distinct files of one folder
	// before the folder is pinned to that show FOR THE FIRST TIME.
	//
	// It is the threshold for ESTABLISHING a new pin only. Honouring an
	// ALREADY-established pin — a candidate found in seriesByTVDBID — needs 1,
	// not 3 (the rescan shortcut, applied per candidate in step 4). Note also
	// that this const is NOT a group-size gate: step 3 uses it only in a cheap
	// pre-filter that additionally requires seriesByTVDBID to be empty,
	// precisely so a one-file rescan of an established show is not skipped
	// before its real threshold can be evaluated.
	//
	// 3, not 2. Two files can coincide on a generic token pair; three distinct
	// slots in one catalog effectively cannot, and every acceptance path in
	// this package is uniformly conservative (fail closed on every hole,
	// decline on every ambiguity). The spec says "at least 2-3" and this takes
	// the strict end. If recall proves too low against the real 99-file
	// population, THIS is the loosening knob — one const, no structural change.
	minCorroboratingFiles = 3

	// maxAnthologyCandidates bounds how many TVDB search results are evaluated
	// per folder. Each costs one SeriesEpisodes fetch.
	maxAnthologyCandidates = 3
)

// anthologyCandidate is one TVDB search result that CLEARED its own
// corroboration threshold, carried whole so step 7 emits from the SINGLE
// survivor of step 5's uniqueness check.
//
// Keeping per-candidate results rather than one shared map is load-bearing:
// candidates are evaluated in search order, so a later non-qualifying (or
// second qualifying) candidate would otherwise overwrite the winner's
// per-file results and step 7 would emit one candidate's slots under another
// candidate's pin.
type anthologyCandidate struct {
	pin     anthologyPin
	matches map[int]tvdbEpisodeSearch // index into out -> that file's result

	// catalog is the SeriesEpisodes fetch this candidate was evaluated
	// against, carried out of the pass so a later consumer can match against
	// an already-fetched catalog rather than re-fetching it. Zero new network
	// calls — it is already in memory and was previously discarded.
	catalog []tvdb.Episode

	// candName is this candidate's OWN TheTVDB name (cand.Name), which is the
	// show-title argument searchTVDBEpisodeByTitle is called with below.
	// It CANNOT be recovered from pin.title later: the established-pin
	// shortcut overwrites pin.title with the TRACKED ROW's title, and
	// episodeTitleMatches subtracts the CATALOG's own show name from each
	// episode name. Capture it at the append or it is gone.
	candName string
}

// anthologyResolution is what tvdbAnthologyPass learned about ONE show folder,
// published for a later pass so it can match against an already-fetched
// catalog rather than re-fetching it (autopilot-impl.md §2.3).
//
// A folder gets an entry ONLY when it resolved to exactly one qualifying
// candidate. Folders declined as ambiguous (>= 2 qualifying candidates) or
// uncorroborated (0) get NO entry at all — publishing one for a folder this
// pass deliberately REFUSED to resolve would let a downstream pass match
// against a contested show's catalog, inverting the deterministic-first
// precedence order.
type anthologyResolution struct {
	pin     anthologyPin
	catalog []tvdb.Episode

	// candName is the winning candidate's own TheTVDB name — NEVER pin.title.
	// See anthologyCandidate.candName for why the two can differ.
	candName string
}

// tvdbAnthologyPass resolves unpinned show folders whose parse-failed files
// self-corroborate against a single TheTVDB show's episode catalog, and
// rewrites those files' proposals in place.
//
// candidates holds INDICES into out, supplied structurally by the caller —
// never derived by matching on out[i].Reason.
//
// out is a slice: this mutates elements in place through the shared backing
// array, so the two return values carry NO proposal data — they are the
// structural record of what this pass learned and what it spoke about
// (autopilot-impl.md §2.3). It must NEVER append to out.
//
//   - resolved maps a show-folder KEY to the one candidate that won that
//     folder. Written at exactly ONE point (the winner-selection point below),
//     never inside the per-candidate loop.
//   - handled marks every index in out this pass WROTE to — all three write
//     branches, including both declines. A downstream pass must skip these
//     structurally, BY INDEX, never by parsing out[i].Reason: overwriting an
//     ambiguity or already-tracked reason with a vaguer one is a silent
//     regression in operator diagnosability that no "the row is Unmatched"
//     assertion would catch.
//
// Both maps are always non-nil, including on every early return, so a caller
// may write to handled without a nil check.
func tvdbAnthologyPass(
	ctx context.Context, sess *mode.Session,
	tracked map[episodeKey]bool,
	seriesByTVDBID map[int]library.Series,
	folderIDs map[string]int,
	roots []string, generalRoot string,
	candidates []int,
	out []proposals.Proposal,
) (resolved map[string]anthologyResolution, handled map[int]bool) {
	// '=' not ':=' — ':=' would shadow the named returns and every caller
	// would receive nil maps, on which handled[i] = true panics.
	resolved = make(map[string]anthologyResolution)
	handled = make(map[int]bool)

	// Step 0 — preconditions. TVDB is an OPTIONAL connection (mode.Build only
	// constructs it when one is configured), so a nil client is the normal
	// case on most installs, not an error.
	if sess == nil || sess.TVDB == nil || len(candidates) == 0 {
		return
	}

	// Step 1 — group by show-folder KEY, recording the show folder's raw
	// directory NAME per key.
	//
	// The name comes from showFolderName, NEVER from
	// filepath.Base(filepath.Dir(...)): that name is the TVDB query seed and
	// the gate's titleSeed, and it must describe the SAME directory the group
	// is keyed on. For the real path
	// Series/The Red Skelton Show/Uncompressed/RED SKELTON e1524.mp4 the
	// immediate-parent derivation yields "Uncompressed" while the key is
	// "theredskeltonshow" — see showFolderSegment's doc comment for why the
	// fail-OPEN half of that divergence is the dangerous one.
	//
	// Because showFolderName and showFolderKey share one traversal, an empty
	// name and an empty key are the same condition, so the key == "" skip is
	// sufficient and no second emptiness check is needed.
	groups := map[string][]int{}
	folderNames := map[string]string{}
	var order []string
	for _, i := range candidates {
		if i < 0 || i >= len(out) {
			continue
		}
		key := showFolderKey(out[i].SourcePath, roots)
		if key == "" {
			continue // sits directly in a root — no show-folder segment
		}
		if _, ok := groups[key]; !ok {
			order = append(order, key)
			folderNames[key] = showFolderName(out[i].SourcePath, roots)
		}
		groups[key] = append(groups[key], i)
	}

	// order, not range-over-map: folder groups are independent, but a
	// deterministic walk keeps a Scan's network call ordering reproducible
	// when something has to be diagnosed from a log.
	for _, key := range order {
		group := groups[key]

		// Step 2 — skip any folder whose key is PRESENT in folderIDs at all.
		//
		// A presence test, NOT folderIDs[key] <= 0, even though the two
		// coincide in value today: the intents differ. Present-with-positive
		// means a real tracked TMDB show owns this folder (pinning it to a
		// synthetic anthology id would be a serious mis-identification, and
		// tryEpisodeTitleMatchSeries already had its shot at these files);
		// present-with-0 is pinFolderID's sticky-ambiguous sentinel, meaning
		// two DIFFERENT real shows contest this folder name — the strongest
		// possible "do not guess" signal in this package.
		if _, present := folderIDs[key]; present {
			continue
		}

		// Step 3 — a cheap PRE-filter only. The corroboration threshold is NOT
		// checked here; it is resolved per candidate in step 4, where the
		// candidate's TVDB id exists to key seriesByTVDBID on.
		//
		// Skip outright ONLY when the group is too small to establish a NEW pin
		// AND there is no established show at all that this could be a rescan
		// shortcut hit for. Both conditions are required: a one-file rescan of
		// an already-applied anthology show has len(group) == 1 but a non-empty
		// seriesByTVDBID, and must survive to step 4 where its real threshold
		// of 1 is applied.
		if len(group) < minCorroboratingFiles && len(seriesByTVDBID) == 0 {
			continue
		}

		// Step 4 — resolve candidate shows, once per folder.
		//
		// The gate is built ONCE per show folder and seeded on the show-folder
		// DIRECTORY NAME — which is literally the string used to query
		// TheTVDB, so Check A compares a query seed against what that query
		// returned, exactly the comparison allowsTitle was designed for. This
		// is a genuine strengthening over the TMDB sibling, not a workaround:
		// there Check A had to be short-circuited past because it was ACTIVELY
		// WRONG in context; here it is the real precision gate, and (since
		// allowsID fails open on a non-positive pin) the ONLY one. Measured
		// during planning:
		//
		//	allowsTitle("Laurel & Hardy") with seed "Laurel and Hardy" -> true
		//	allowsTitle("Breaking Bad")   with seed "Laurel and Hardy" -> false
		//	allowsTitle("Laurel & Hardy") with seed "Duck Soup.mkv"    -> false
		//
		// NEVER seed this gate with a filename — the third line is what that
		// would do to every file whose name does not repeat the show name.
		folderName := folderNames[key]
		gate := &seriesGate{titleSeed: folderName, rejected: new(int)}
		results, err := sess.TVDB.SearchSeries(ctx, folderName)
		if err != nil || len(results) == 0 {
			continue
		}

		var qualified []anthologyCandidate
		for ci, cand := range results {
			if ci >= maxAnthologyCandidates {
				break
			}
			// Check A, applied explicitly. gate.reject() is called separately
			// because allowsTitle alone does not increment the shared counter
			// (the identical shipped code-review finding in rename.go). The
			// counter is not read on this path — nothing here builds
			// gateRejectedReason — but leaving it uncounted diverges from the
			// one call-site convention in this package for no reason.
			if !gate.allowsTitle(cand.Name) {
				gate.reject()
				continue
			}
			// Fail-CLOSED at FOLDER granularity: any catalog error skips this
			// candidate entirely rather than searching a partial list, which
			// is what lets tvdbEpisodeSearch have no Incomplete field.
			catalog, err := sess.TVDB.SeriesEpisodes(ctx, cand.TVDBID, tvdb.SeasonTypeOfficial)
			if err != nil || len(catalog) == 0 {
				continue
			}

			matches := make(map[int]tvdbEpisodeSearch, len(group))
			// Distinct SLOTS, not matching files: three files that all resolve
			// to S01E05 are ONE piece of corroborating evidence, not three.
			slots := map[[2]int]struct{}{}
			for _, i := range group {
				// cand.Name, not pin.title: the show-title argument here is
				// the catalog's own show, which is what episodeTitleMatches
				// subtracts from each episode name.
				r := searchTVDBEpisodeByTitle(catalog, cand.Name, out[i].SourceName)
				matches[i] = r
				if r.Found == 1 && r.Match != nil {
					slots[[2]int{r.Match.season, r.Match.episode}] = struct{}{}
				}
			}

			// established is the tracked library.Series row for this candidate
			// TVDB show, if it has been pinned and applied before. Keyed on the
			// TVDB SERIES ID, never on a folder key: after the first Apply the
			// files live under a folder named from TheTVDB ("Laurel & Hardy" ->
			// key "laurelhardy") while any un-applied stragglers are still under
			// the original on-disk folder ("Laurel and Hardy" ->
			// "laurelandhardy"). Those keys DIFFER, so a folder-keyed lookup
			// misses exactly when it is needed most.
			//
			// DOCUMENTED DEVIATION from the plan's §3.5, added deliberately —
			// the plan keys the shortcut on the TVDB id ALONE, which is sound
			// only while this path is the sole writer of
			// library_series.tvdb_id. It is today (verified 2026-08-07: nothing
			// in the repo persists that column at all, §0.6), but the Apply-path
			// story that makes seriesByTVDBID non-empty in the first place could
			// just as easily persist TVDBID for EVERY series row. A real,
			// TMDB-tracked show landing here would drop threshold to 1 and then
			// mint a pin whose synthID is negative while Title/Year/root come
			// from the real row — Apply would create a SECOND library_series row
			// rendering the SAME folder name, which is exactly the split-row
			// corruption the sibling path's §0.3 exists to prevent, reached from
			// a direction the plan did not trace. The extra clause costs nothing
			// and cannot reject a genuine anthology row: step 6 below is what
			// wrote that row's TMDBID, and it always writes
			// anthologyTMDBID(tvdbID).
			established, isEstablished := seriesByTVDBID[cand.TVDBID]
			if isEstablished && established.TMDBID != anthologyTMDBID(cand.TVDBID) {
				isEstablished = false // a real TMDB show, not a prior anthology pin
			}
			threshold := minCorroboratingFiles // establishing a NEW pin
			if isEstablished {
				threshold = 1 // honouring an ALREADY-established pin
			}
			if len(slots) < threshold {
				continue // this candidate does not qualify
			}

			// Step 6 — build the pin (per qualifying candidate; step 5 below
			// guarantees at most one survives, so this is built once per folder
			// that actually resolves).
			pin := anthologyPin{
				tvdbID:  cand.TVDBID,
				synthID: anthologyTMDBID(cand.TVDBID),
				title:   cand.Name,
				year:    cand.Year,
			}
			if isEstablished {
				// Convergence by construction: writing back the row's OWN
				// values makes UpsertSeries' unconditional overwrite idempotent
				// and keeps naming.SeriesFolderName producing the folder that
				// already exists on disk. Sourcing Title/Year from TheTVDB here
				// instead would rename a committed folder if TheTVDB's series
				// name ever changed.
				pin.title, pin.year, pin.root = established.Title, established.Year, established.RootFolderPath
			}
			// catalog and cand.Name are captured HERE, at the append, and
			// deliberately not published anywhere yet: a candidate that
			// qualifies is not yet a winner — step 5 below can still decline
			// the whole folder as ambiguous.
			qualified = append(qualified, anthologyCandidate{
				pin: pin, matches: matches, catalog: catalog, candName: cand.Name,
			})
		}

		// Step 5 — SHOW-LEVEL uniqueness. Two or more qualifying candidates
		// DECLINE the whole folder; never "first qualifying candidate wins",
		// which reintroduces exactly the ordering-dependent title-collision
		// class of bug series_folder_guard.go was written to fix.
		//
		// Mixed thresholds still decline, deliberately: one folder can hold a
		// candidate that qualified via the established-pin shortcut (1 slot)
		// alongside one that qualified the hard way (3 slots). That is still
		// two qualifying candidates. The shortcut narrows how much evidence ONE
		// candidate needs, never how many candidates may win.
		if len(qualified) != 1 {
			continue // 0 = nothing corroborated; >1 = ambiguous, leave untouched
		}
		winner := qualified[0]
		pin := winner.pin

		// The ONE resolved[key] write point, and it is HERE deliberately —
		// AFTER step 5's uniqueness check, never inside the candidate loop
		// above. The candidate loop can qualify TWO candidates for one folder,
		// which step 5 then declines; writing there would publish a resolution
		// for a folder this pass refused to resolve, and a downstream pass
		// would match a file against a CONTESTED show's catalog. Folders that
		// `continue` at the check above get no entry at all.
		resolved[key] = anthologyResolution{
			pin:      winner.pin,
			catalog:  winner.catalog,
			candName: winner.candName,
		}

		// Step 7 — emit, per file in the group, from the ONE shared pin.
		for _, i := range group {
			res := winner.matches[i]
			switch {
			case res.Found == 0:
				// Leave untouched — the existing parse-failure Unmatched and
				// its verbatim reason string survive (that string is what the
				// .omc/artifacts/series-*.psv diagnostics were grepped on).
				continue
			case res.Found >= 2:
				// Mirrors the sibling's precedent: an operator must be able to
				// distinguish "SAK found two plausible slots and refused to
				// guess" from "SAK found nothing," without a re-run.
				q := out[i]
				q.Status = proposals.Unmatched
				q.Reason = fmt.Sprintf(
					"%q matches more than one episode of %q on TheTVDB (S%02dE%02d %q and S%02dE%02d %q) — ambiguous, leaving in place for manual review",
					out[i].SourceName, pin.title,
					res.First.season, res.First.episode, res.First.name,
					res.Second.season, res.Second.episode, res.Second.name,
				)
				out[i] = q
				handled[i] = true // WRITE BRANCH 1 of 3 — ambiguity decline
				continue
			case res.Match == nil:
				// Cannot happen given searchTVDBEpisodeByTitle's construction
				// (Found == 1 always sets Match), but this is an ACCEPTANCE
				// path — refuse rather than assume.
				continue
			}
			m := res.Match

			// The tracked guard, reimplemented rather than reused: acceptSeries
			// closes over a season/episode that do not exist at this call site.
			// Omitting it is the single most likely defect in this work — a
			// match onto an already-tracked slot produces a Pending whose Apply
			// overwrites that slot's library_episodes row (UNIQUE(series_id,
			// season_number, episode_number)) and silently orphans the existing
			// file. `tracked` is keyed on series.TMDBID with no sign filter, so
			// negative synthetic ids populate it correctly on rescan.
			if tracked[episodeKey{tmdbID: pin.synthID, season: m.season, episode: m.episode}] {
				q := out[i]
				q.Status = proposals.Unmatched
				q.Reason = fmt.Sprintf("appears to already be in the library as %q S%02dE%02d — leaving in place for manual review", pin.title, m.season, m.episode)
				out[i] = q
				// WRITE BRANCH 2 of 3 — tracked-slot-already-claimed decline.
				// The subtlest of the three and the one an earlier draft
				// MISSED: this row is Unmatched with TMDBID == 0, so it looks
				// untouched to any status/id-shaped eligibility test. Only
				// this bit stops a downstream pass re-deriving the same
				// refusal by a longer route and overwriting this precise
				// reason with a vaguer one.
				handled[i] = true
				continue
			}

			// Spec-mandated per-file gate composition. It evaluates TRUE by
			// construction, and the exact reason matters — BE PRECISE HERE,
			// because the obvious wording is wrong. Traced against
			// series_folder_guard.go's allows():
			//
			//  1. the pinned-ID short-circuit needs g.pinnedTMDBID > 0. This
			//     gate's pinnedTMDBID is 0 (never set), so it is SKIPPED.
			//  2. allowsID returns true at its FIRST guard, g.pinnedTMDBID <= 0
			//     — the PIN guard, not the candidate-id guard. The candidate-id
			//     guard ("synthetic negative ids must never be compared against
			//     a real pin") would ALSO fail open here, but is never reached.
			//     Neither branch of Check B contributes anything.
			//  3. allowsTitle then runs with the FOLDER-NAME seed, true.
			//
			// So Check A with the folder-name seed is the ENTIRE gate on this
			// path. allowsTitle was already proven true for this seed at
			// show-resolution time by this same instance — with one caveat:
			// when the established-pin shortcut fires, pin.title is the TRACKED
			// ROW's title, not cand.Name, so this call tests a pair that was
			// not literally the one proven above. Benign today, since the
			// tracked title is what produced the folder in the first place, but
			// do not read the earlier check as a proof of this one.
			//
			// It is kept anyway because it couples this path to the gate's
			// DEFINITION rather than to a snapshot of it: a future Check C
			// added to allows() is inherited automatically instead of this
			// becoming the one Series acceptance path that never consulted the
			// gate. Do NOT "make it meaningful" by re-seeding the gate with the
			// filename — that is the plausible-looking edit that breaks the
			// whole feature.
			if !gate.allows(pin.title, pin.synthID) {
				continue
			}

			// Kids routing without a classify.WithAI call, deliberately: an AI
			// round trip per FILE for a FOLDER-level question, which could also
			// disagree with the folder the show already lives in.
			//
			// foundRoot is read from out[i] BEFORE the row is rewritten below —
			// ScanLibrarySeries seeds Proposal.RootFolderPath with the root the
			// file was found under, and reading it after the assignment would
			// make the first branch compare against itself.
			//
			// The non-empty sess.KidsRootPath guard on the second branch is
			// REQUIRED (a shipped code-review MEDIUM finding on the sibling
			// path): without it an unconfigured Kids root "" compared against a
			// degenerate pin.root == "" both match, targetRoot becomes "", and
			// RelocateEpisodeRange builds a relative destination instead of
			// refusing.
			foundRoot := out[i].RootFolderPath
			targetRoot := generalRoot
			switch {
			case foundRoot == sess.KidsRootPath:
				targetRoot = sess.KidsRootPath // where the file was found wins (matches acceptSeries)
			case sess.KidsRootPath != "" && pin.root == sess.KidsRootPath:
				targetRoot = sess.KidsRootPath
			}

			q := out[i] // preserve Mode/Workflow/SourceName/SourcePath
			q.Status = proposals.Pending
			q.Title = pin.title
			q.TMDBID = pin.synthID // negative synthetic
			q.TVDBID = pin.tvdbID  // the REAL TheTVDB series id
			q.Year = pin.year
			q.SeasonNumber = m.season
			q.EpisodeNumber = m.episode
			// ExtraEpisodeNumbers stays nil: a title match resolves exactly one
			// slot.
			q.RootFolderPath = targetRoot
			// No Genres/Cast enrichment, and this is deliberate rather than an
			// oversight: the TMDB sibling calls TVDetails/TVAggregateCredits,
			// but there is no TMDB id here to call them with, and adding a TMDB
			// lookup would reintroduce the show-guessing this design avoids.
			// library.Series.Genres/Cast stay empty for anthology shows.
			q.Reason = fmt.Sprintf("%s %q -> S%02dE%02d (tvdb %d)",
				tvdbAnthologyReasonPrefix, m.name, m.season, m.episode, pin.tvdbID)
			out[i] = q
			handled[i] = true // WRITE BRANCH 3 of 3 — the Pending emit
		}
	}

	// The three no-write `continue`s above (res.Found == 0, the defensive
	// res.Match == nil, and the !gate.allows refusal) deliberately do NOT set
	// handled: those rows are genuinely still unspoken-for.
	return resolved, handled
}
