// Package purge implements SAK's Purge workflow: library-backed Scan/Apply
// pairs (ScanLibrary/ApplyLibrary and their Series/Adult siblings) that
// surface every tracked item matching an enabled pruning rule as a proposal to
// permanently delete — the same staged-for-approval shape internal/rename
// uses, just keyed off already-tracked items instead of unmapped folders.
//
// This package owns NO matching logic of its own. Every condition — age, size,
// quality tier and tags — is evaluated by internal/pruning, which this package
// imports; the Scan functions only project a library row into a
// pruning.Subject and forward it. Tag matching in particular used to live here
// (MatchesAny/MatchedEntries) and now lives in internal/pruning's matchedTags:
// the dependency runs purge -> pruning and cannot run the other way, so the
// primitive had to move rather than be called.
package purge

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/proposals"
	"github.com/labbersanon/sakms/internal/pruning"
)

// Claude 2026-08-11: the global per-mode tag allowlist is retired; every Scan*
// function below matches purely through `rules []pruning.Rule`, with tags now
// a fourth AND'd per-rule condition (plan
// .omc/plans/autopilot-impl-purge-rules-consolidation-cleanup-rename.md §3).
// Reason: rules are still threaded as a PARAMETER rather than evaluated
// through a new exported purge function — internal/scanschedule's static
// allowlist test (allowlist_test.go) permits cmd/sakms/scanadapter.go to
// reference only Scan*-prefixed purge symbols and ReplacePending, so a new
// exported purge.EvaluateTags would fail it even though it is entirely
// read-only.
// Troubleshooting: evaluation stays folded into the EXISTING per-item loop,
// not run as a second pass. A second pass would emit two proposals with the
// same TrackedID for an item matching two rules, and applying the first would
// make the second fail confusingly in ApplyLibrary.
// Review if: purge ever needs to propose more than one row per tracked item.

// ScanLibrary is Purge's Movies-library scan — used only for Movies mode now
// that Radarr no longer sits between SAK and this mode's item/tag data (see
// internal/library's package doc). Stages any item satisfying one of rules; an
// Item carries Size/QualityTier/CreatedAt directly, and its tags come from
// libStore's own local tag rows, so no aggregation is needed here (see
// ScanLibrarySeries).
func ScanLibrary(ctx context.Context, libStore *library.Store, rules []pruning.Rule) ([]proposals.Proposal, error) {
	items, err := libStore.List(ctx, mode.Movies)
	if err != nil {
		return nil, fmt.Errorf("loading library items: %w", err)
	}

	// One instant for the whole scan, so every item in one pass is aged
	// against the same `now` rather than drifting across a long loop.
	now := time.Now().UTC()

	var out []proposals.Proposal
	for _, item := range items {
		tags, err := libStore.Tags(ctx, item.ID)
		if err != nil {
			return nil, fmt.Errorf("loading tags for %q: %w", item.Title, err)
		}
		ruleReasons := pruning.MatchAny(rules, pruning.Subject{
			CreatedAt: item.CreatedAt, SizeBytes: item.Size, QualityTier: item.QualityTier, Tags: tags,
		}, now)
		if len(ruleReasons) == 0 {
			continue
		}
		out = append(out, proposals.Proposal{
			Mode: mode.Movies, Workflow: proposals.Purge, Status: proposals.Pending,
			SourceName: item.Title, SourcePath: item.FilePath, RootFolderPath: item.RootFolderPath,
			Title: item.Title, TMDBID: item.TMDBID, TrackedID: int(item.ID),
			Reason: strings.Join(ruleReasons, "; "),
		})
	}
	return out, nil
}

// ApplyLibrary removes the library item's file directly (no *arr app to ask)
// and deletes its record from libStore. p must be Pending and carry a
// TrackedID from ScanLibrary (the library item's own id).
//
// changes is a named return so a post-delete failure (libStore.Delete)
// still reports the committed removal to the caller for
// Session.NotifyPlayers. item.FilePath can legitimately be "" (a
// library-tracked row with no file yet) — the Deleted PathChange is only
// ever appended inside the non-empty guard, never for an empty path.
func ApplyLibrary(ctx context.Context, libStore *library.Store, p proposals.Proposal) (changes []mode.PathChange, err error) {
	if p.Status != proposals.Pending {
		return nil, fmt.Errorf("proposal %d is %q, not pending — nothing to apply", p.ID, p.Status)
	}
	if p.TrackedID == 0 {
		return nil, fmt.Errorf("proposal %d has no library item id to delete", p.ID)
	}

	item, err := libStore.Get(ctx, int64(p.TrackedID))
	if err != nil {
		return nil, fmt.Errorf("loading library item %d: %w", p.TrackedID, err)
	}
	if item.FilePath != "" {
		if err := os.Remove(item.FilePath); err != nil && !os.IsNotExist(err) {
			return nil, fmt.Errorf("deleting %q: %w", item.FilePath, err)
		}
		changes = append(changes, mode.PathChange{Path: item.FilePath, Kind: mode.Deleted})
	}
	if err := libStore.Delete(ctx, int64(p.TrackedID)); err != nil {
		return changes, err
	}
	return changes, nil
}

// ScanLibrarySeries is Purge's Series-library counterpart to ScanLibrary —
// used once Series stops requiring Sonarr (see the plan this was built
// from, Stage 2). Tags live at the series level, not per-episode (matching
// Sonarr's own tag granularity, and the only sane unit for Purge to act on
// anyway — see ApplyLibrarySeries), so one proposal per matched SERIES, not
// per episode — and a rule's tags condition is evaluated against those
// series-level tags directly, with no aggregation.
//
// Pruning-rule evaluation does have to aggregate the size/tier a Series
// row does not itself carry (migration 0055 added size/quality_tier to
// library_items/library_episodes/library_scenes, NOT library_series) — see
// seriesSubject.
func ScanLibrarySeries(ctx context.Context, libStore *library.Store, rules []pruning.Rule) ([]proposals.Proposal, error) {
	series, err := libStore.ListSeries(ctx)
	if err != nil {
		return nil, fmt.Errorf("loading series: %w", err)
	}

	now := time.Now().UTC()
	// ListEpisodes is one extra query per series, so it runs only when some
	// rule actually needs an aggregate. An age-only rule reads the series'
	// own CreatedAt, and a scan with no rules at all behaves exactly as it
	// did before pruning rules existed — no N+1 added to either (plan §2.4).
	needEpisodes := anyRuleNeedsEpisodeAggregation(rules)

	var out []proposals.Proposal
	for _, s := range series {
		tags, err := libStore.SeriesTags(ctx, s.ID)
		if err != nil {
			return nil, fmt.Errorf("loading tags for %q: %w", s.Title, err)
		}
		var ruleReasons []string
		if len(rules) > 0 {
			subj := pruning.Subject{CreatedAt: s.CreatedAt, Tags: tags}
			if needEpisodes {
				episodes, epErr := libStore.ListEpisodes(ctx, s.ID)
				if epErr != nil {
					return nil, fmt.Errorf("loading episodes for %q: %w", s.Title, epErr)
				}
				subj.SizeBytes, subj.QualityTier = seriesSubject(episodes)
			}
			ruleReasons = pruning.MatchAny(rules, subj, now)
		}
		if len(ruleReasons) == 0 {
			continue
		}
		out = append(out, proposals.Proposal{
			Mode: mode.Series, Workflow: proposals.Purge, Status: proposals.Pending,
			SourceName: s.Title, SourcePath: s.RootFolderPath, RootFolderPath: s.RootFolderPath,
			Title: s.Title, TMDBID: s.TMDBID, TrackedID: int(s.ID),
			Reason: strings.Join(ruleReasons, "; "),
		})
	}
	return out, nil
}

// anyRuleNeedsEpisodeAggregation reports whether any rule configures a size
// or tier condition — the only two a Series row cannot answer by itself.
func anyRuleNeedsEpisodeAggregation(rules []pruning.Rule) bool {
	for _, r := range rules {
		if r.SizeBytes != 0 || r.QualityTierFloor != "" {
			return true
		}
	}
	return false
}

// seriesSubject aggregates a series' episodes into the size and quality tier
// a pruning.Subject needs: size is the SUM of every episode's bytes ("this
// show occupies N bytes"), tier is the BEST tier across episodes that have a
// real one.
//
// Best, not worst: ApplyLibrarySeries deletes EVERY episode file of the whole
// show in one Apply, so worst-tier semantics would let a single low-quality
// episode stage an otherwise pristine multi-season show for deletion. Best
// means one high-quality episode protects the show — the correct failure
// direction for an irreversible bulk delete (plan §2.4).
//
// This is deliberately stricter than the display precedent in
// library.StorageAllocation, where a series spanning two tiers is counted in
// BOTH tier cells. That is right for a read-only breakdown and wrong here,
// because this path proposes deletion.
//
// Episodes whose tier is ""/"unknown" are excluded from the max (BestTier
// skips them), so a series with no real tier anywhere aggregates to "" — which
// per pruning.Match can never satisfy a tier condition. Fail-closed.
func seriesSubject(episodes []library.Episode) (int64, string) {
	var total int64
	tiers := make([]string, 0, len(episodes))
	for _, ep := range episodes {
		total += ep.Size
		tiers = append(tiers, ep.QualityTier)
	}
	return total, pruning.BestTier(tiers)
}

// ApplyLibrarySeries is Purge's Series-library counterpart to ApplyLibrary —
// removes every one of the series' episode files directly (no *arr app to
// ask), then deletes the series (and its episode/tag rows) from libStore in
// one call. p must be Pending and carry a TrackedID from ScanLibrarySeries
// (the series' own id). This has a larger blast radius per Apply than
// Movies' (a whole show's files, not one movie's) — the same blast radius
// Sonarr's own DeleteTracked(deleteFiles=true) already had, just executed
// locally now; still exactly one already-approved proposal per call.
//
// changes accumulates one Deleted PathChange per removed episode file (N
// deletes in one batch is expected, not an edge case) — a named return so a
// post-delete failure (libStore.DeleteSeries) still reports every file that
// was actually removed to the caller for Session.NotifyPlayers. An episode
// with no file (ep.FilePath == "") is skipped entirely, same as before.
//
// A logical-episode-split file (library.ParseEpisodeNumbers) can back TWO
// episode rows with an identical FilePath — removedPaths dedupes so that
// case reports exactly one Deleted PathChange per physical file, not one
// per row that happened to reference it (the second os.Remove is already a
// safe IsNotExist no-op; only the PathChange bookkeeping needed the fix).
// Unlike Dedup's ApplyLibrarySeries, no shared-file survival guard is
// needed here: every episode of the series is enumerated and removed in
// this same call, so split siblings always die together atomically —
// there's no window where one sibling's row could survive pointing at a
// file the other sibling just had deleted out from under it.
func ApplyLibrarySeries(ctx context.Context, libStore *library.Store, p proposals.Proposal) (changes []mode.PathChange, err error) {
	if p.Status != proposals.Pending {
		return nil, fmt.Errorf("proposal %d is %q, not pending — nothing to apply", p.ID, p.Status)
	}
	if p.TrackedID == 0 {
		return nil, fmt.Errorf("proposal %d has no series id to delete", p.ID)
	}

	episodes, err := libStore.ListEpisodes(ctx, int64(p.TrackedID))
	if err != nil {
		return nil, fmt.Errorf("loading episodes for series %d: %w", p.TrackedID, err)
	}
	removedPaths := make(map[string]bool, len(episodes))
	for _, ep := range episodes {
		if ep.FilePath == "" || removedPaths[ep.FilePath] {
			continue
		}
		if err := os.Remove(ep.FilePath); err != nil && !os.IsNotExist(err) {
			return changes, fmt.Errorf("deleting %q: %w", ep.FilePath, err)
		}
		removedPaths[ep.FilePath] = true
		changes = append(changes, mode.PathChange{Path: ep.FilePath, Kind: mode.Deleted})
	}
	if err := libStore.DeleteSeries(ctx, int64(p.TrackedID)); err != nil {
		return changes, err
	}
	return changes, nil
}
