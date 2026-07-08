// Package rename implements Tidyarr's Rename workflow: propose registering
// orphaned (unmapped) files with their mode's Sonarr/Radarr instance, then —
// once a human approves a specific proposal — actually register it.
//
// Scan never mutates anything; it only reads and produces proposals.Proposal
// values. Apply is the only function in this package that calls a *arr app's
// write endpoints, and it only ever acts on one already-approved proposal at
// a time — there is no "apply everything" path, by design (see the design
// spec's staged-for-approval principle).
package rename

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/curtiswtaylorjr/tidyarr/internal/config"
	"github.com/curtiswtaylorjr/tidyarr/internal/mode"
	"github.com/curtiswtaylorjr/tidyarr/internal/proposals"
	"github.com/curtiswtaylorjr/tidyarr/internal/searchterm"
	"github.com/curtiswtaylorjr/tidyarr/internal/servarr"
)

// Scan walks every root folder sess's Servarr app currently reports and
// produces one proposal per orphaned item: a resolved match ready to
// register (Pending), or a record of why it couldn't be resolved on its own
// (Unmatched) — surfaced either way, never silently dropped.
func Scan(ctx context.Context, sess *mode.Session) ([]proposals.Proposal, error) {
	client := sess.Servarr

	folders, err := client.RootFolders(ctx)
	if err != nil {
		return nil, fmt.Errorf("loading root folders: %w", err)
	}
	tracked, err := client.AllTracked(ctx)
	if err != nil {
		return nil, fmt.Errorf("loading tracked items: %w", err)
	}
	profiles, err := client.QualityProfiles(ctx)
	if err != nil {
		return nil, fmt.Errorf("loading quality profiles: %w", err)
	}

	var out []proposals.Proposal
	for _, root := range folders {
		for _, uf := range root.UnmappedFolders {
			if config.SidecarExts[strings.ToLower(filepath.Ext(uf.Name))] {
				continue
			}
			out = append(out, proposeOne(ctx, client, sess.Mode, root, uf, tracked, profiles))
		}
	}
	return out, nil
}

func proposeOne(
	ctx context.Context, client *servarr.Client, m mode.Mode,
	root servarr.RootFolder, uf servarr.UnmappedFolder,
	tracked []servarr.TrackedItem, profiles []servarr.QualityProfile,
) proposals.Proposal {
	p := proposals.Proposal{
		Mode: m, Workflow: proposals.Rename,
		SourceName: uf.Name, SourcePath: uf.Path, RootFolderPath: root.Path,
	}

	term := searchterm.FromName(uf.Name)
	results, err := client.Lookup(ctx, term)
	if err != nil {
		p.Status = proposals.Unmatched
		p.Reason = fmt.Sprintf("lookup failed for search term %q: %v", term, err)
		return p
	}
	if len(results) == 0 {
		p.Status = proposals.Unmatched
		p.Reason = fmt.Sprintf("no match for search term %q", term)
		return p
	}
	lr := results[0]

	if dup := findTrackedDuplicate(tracked, client.AppType(), lr); dup != nil {
		p.Status = proposals.Unmatched
		p.Reason = fmt.Sprintf("appears to already be tracked as %q (in %s) — leaving in place for manual review", dup.Title, dup.RootFolderPath)
		return p
	}

	p.Status = proposals.Pending
	p.Title = lr.Title
	p.TVDBID = lr.TVDBID
	p.TMDBID = lr.TMDBID
	p.QualityProfileID = defaultQualityProfileID(tracked, root.Path, profiles)
	return p
}

// findTrackedDuplicate reports whether lr's identified TVDB/TMDB ID already
// matches something the app tracks — i.e. this "orphaned" item is actually a
// duplicate copy of existing content, not a genuinely new addition.
func findTrackedDuplicate(tracked []servarr.TrackedItem, app servarr.App, lr servarr.LookupResult) *servarr.TrackedItem {
	for i, t := range tracked {
		if app == servarr.Sonarr && lr.TVDBID != 0 && t.TVDBID == lr.TVDBID {
			return &tracked[i]
		}
		if app == servarr.Radarr && lr.TMDBID != 0 && t.TMDBID == lr.TMDBID {
			return &tracked[i]
		}
	}
	return nil
}

// defaultQualityProfileID picks the profile most commonly already used by
// tracked items in rootPath, so a new addition fits how this particular
// library is already organized instead of guessing at a hardcoded profile.
// Falls back to the first available profile if rootPath has no tracked items
// yet to learn a convention from.
func defaultQualityProfileID(tracked []servarr.TrackedItem, rootPath string, profiles []servarr.QualityProfile) int {
	counts := make(map[int]int)
	for _, t := range tracked {
		if t.RootFolderPath == rootPath {
			counts[t.QualityProfileID]++
		}
	}

	// Map iteration order is randomized — break ties by lowest ID so the
	// result is deterministic across runs instead of depending on Go's map
	// ordering.
	bestID, bestCount := 0, 0
	for id, count := range counts {
		if count > bestCount || (count == bestCount && id < bestID) {
			bestID, bestCount = id, count
		}
	}
	if bestCount > 0 {
		return bestID
	}
	if len(profiles) > 0 {
		return profiles[0].ID
	}
	return 0
}

// Apply registers p's identified item with sess's Servarr app, then triggers
// a broad downloaded-scan so the app picks up the file already sitting on
// disk under p.RootFolderPath. p must be Pending — Apply refuses anything
// else (already applied, dismissed, or unmatched with nothing to register).
//
// If Add succeeds but the follow-up scan trigger fails, trackedID is still
// returned alongside the error: the item is genuinely registered at that
// point, so the caller should still record it as applied rather than losing
// track of it — the scan trigger can be retried independently (e.g. the
// app's own periodic scan will pick it up eventually regardless).
func Apply(ctx context.Context, sess *mode.Session, p proposals.Proposal) (trackedID int, err error) {
	if p.Status != proposals.Pending {
		return 0, fmt.Errorf("proposal %d is %q, not pending — nothing to apply", p.ID, p.Status)
	}

	id, err := sess.Servarr.Add(ctx, servarr.AddRequest{
		Title: p.Title, TVDBID: p.TVDBID, TMDBID: p.TMDBID,
		QualityProfileID: p.QualityProfileID, RootFolderPath: p.RootFolderPath, Monitored: true,
	})
	if err != nil {
		return 0, fmt.Errorf("registering %q: %w", p.Title, err)
	}

	if err := sess.Servarr.ScanForDownloaded(ctx); err != nil {
		return id, fmt.Errorf("registered as id=%d but triggering the downloaded-files scan failed: %w", id, err)
	}
	return id, nil
}
