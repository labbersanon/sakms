// Package purge implements SAK's Purge workflow: an editable allowlist
// of tag names, and a Scan/Apply pair that surfaces every tracked item whose
// tags match it as a proposal to permanently delete — the same staged-for-
// approval shape internal/rename uses, just keyed off tags on already-
// tracked items instead of unmapped folders.
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
	"strings"

	"github.com/curtiswtaylorjr/sak/internal/mode"
	"github.com/curtiswtaylorjr/sak/internal/proposals"
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

// Scan fetches every tracked item and every native tag for sess's app,
// resolves each item's tag IDs to labels, and produces a Pending proposal
// for each item that matches allowlist — one row per matched item, never a
// bulk "delete everything matched" action. Scan makes no mutating calls.
func Scan(ctx context.Context, sess *mode.Session, allowlist []string) ([]proposals.Proposal, error) {
	client := sess.Servarr

	tracked, err := client.AllTracked(ctx)
	if err != nil {
		return nil, fmt.Errorf("loading tracked items: %w", err)
	}
	tags, err := client.Tags(ctx)
	if err != nil {
		return nil, fmt.Errorf("loading tags: %w", err)
	}
	labelByID := make(map[int]string, len(tags))
	for _, t := range tags {
		labelByID[t.ID] = t.Label
	}

	var out []proposals.Proposal
	for _, item := range tracked {
		matched := matchedLabels(item.TagIDs, labelByID, allowlist)
		if len(matched) == 0 {
			continue
		}
		out = append(out, proposals.Proposal{
			Mode: sess.Mode, Workflow: proposals.Purge, Status: proposals.Pending,
			SourceName: item.Title, SourcePath: item.Path, RootFolderPath: item.RootFolderPath,
			Title: item.Title, TrackedID: item.ID,
			Reason: fmt.Sprintf("matched allowlist tag(s): %s", strings.Join(matched, ", ")),
		})
	}
	return out, nil
}

func matchedLabels(tagIDs []int, labelByID map[int]string, allowlist []string) []string {
	var matched []string
	for _, id := range tagIDs {
		label, ok := labelByID[id]
		if !ok {
			continue
		}
		matched = append(matched, MatchedEntries(label, allowlist)...)
	}
	return matched
}

// Apply permanently deletes p's tracked item and its underlying file(s). p
// must be Pending, and must carry a TrackedID from Scan — there is no
// "delete everything matched" path, by design: exactly one already-approved
// proposal per call.
func Apply(ctx context.Context, sess *mode.Session, p proposals.Proposal) error {
	if p.Status != proposals.Pending {
		return fmt.Errorf("proposal %d is %q, not pending — nothing to apply", p.ID, p.Status)
	}
	if p.TrackedID == 0 {
		return fmt.Errorf("proposal %d has no tracked item id to delete", p.ID)
	}
	if err := sess.Servarr.DeleteTracked(ctx, p.TrackedID); err != nil {
		return fmt.Errorf("deleting %q (tracked id %d): %w", p.Title, p.TrackedID, err)
	}
	return nil
}
