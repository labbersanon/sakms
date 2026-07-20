// Package purge implements SAK's Purge workflow: an editable allowlist
// of tag names, and library-backed Scan/Apply pairs (ScanLibrary/ApplyLibrary
// and their Series/Adult siblings) that surface every tracked item whose tags
// match it as a proposal to permanently delete — the same staged-for-approval
// shape internal/rename uses, just keyed off tags on already-tracked items
// instead of unmapped folders.
//
// MatchesAny/MatchedEntries are ported unchanged from stash-whisparr-sort's
// internal/purge: EXACT (case-insensitive) matching against tag names,
// deliberately not substring or word-boundary matching — see their doc
// comments for why a tag like "Transgender" and one like "Transformation"
// need to be distinguishable with zero false positives.
package purge

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/proposals"
)

// MatchesAny reports whether tagName exactly matches (case-insensitive) any
// entry in allowlist.
func MatchesAny(tagName string, allowlist []string) bool {
	for _, a := range allowlist {
		if strings.EqualFold(tagName, a) {
			return true
		}
	}
	return false
}

// MatchedEntries returns which allowlist entries exactly matched tagName (for
// recording which rule fired) — in practice at most one, since tag names are
// unique, but returns a slice in case that ever changes.
func MatchedEntries(tagName string, allowlist []string) []string {
	var out []string
	for _, a := range allowlist {
		if strings.EqualFold(tagName, a) {
			out = append(out, a)
		}
	}
	return out
}

// ScanLibrary is Purge's Movies-library scan — used only for Movies mode now
// that Radarr no longer sits between SAK and this mode's item/tag data (see
// internal/library's package doc). Matches libStore's own local tags against
// allowlist using the exact same MatchesAny rule.
func ScanLibrary(ctx context.Context, libStore *library.Store, allowlist []string) ([]proposals.Proposal, error) {
	items, err := libStore.List(ctx, mode.Movies)
	if err != nil {
		return nil, fmt.Errorf("loading library items: %w", err)
	}

	var out []proposals.Proposal
	for _, item := range items {
		tags, err := libStore.Tags(ctx, item.ID)
		if err != nil {
			return nil, fmt.Errorf("loading tags for %q: %w", item.Title, err)
		}
		var matched []string
		for _, tag := range tags {
			matched = append(matched, MatchedEntries(tag, allowlist)...)
		}
		if len(matched) == 0 {
			continue
		}
		out = append(out, proposals.Proposal{
			Mode: mode.Movies, Workflow: proposals.Purge, Status: proposals.Pending,
			SourceName: item.Title, SourcePath: item.FilePath, RootFolderPath: item.RootFolderPath,
			Title: item.Title, TMDBID: item.TMDBID, TrackedID: int(item.ID),
			Reason: fmt.Sprintf("matched allowlist tag(s): %s", strings.Join(matched, ", ")),
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
// per episode.
func ScanLibrarySeries(ctx context.Context, libStore *library.Store, allowlist []string) ([]proposals.Proposal, error) {
	series, err := libStore.ListSeries(ctx)
	if err != nil {
		return nil, fmt.Errorf("loading series: %w", err)
	}

	var out []proposals.Proposal
	for _, s := range series {
		tags, err := libStore.SeriesTags(ctx, s.ID)
		if err != nil {
			return nil, fmt.Errorf("loading tags for %q: %w", s.Title, err)
		}
		var matched []string
		for _, tag := range tags {
			matched = append(matched, MatchedEntries(tag, allowlist)...)
		}
		if len(matched) == 0 {
			continue
		}
		out = append(out, proposals.Proposal{
			Mode: mode.Series, Workflow: proposals.Purge, Status: proposals.Pending,
			SourceName: s.Title, SourcePath: s.RootFolderPath, RootFolderPath: s.RootFolderPath,
			Title: s.Title, TMDBID: s.TMDBID, TrackedID: int(s.ID),
			Reason: fmt.Sprintf("matched allowlist tag(s): %s", strings.Join(matched, ", ")),
		})
	}
	return out, nil
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
