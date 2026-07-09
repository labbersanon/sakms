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
	"os"
	"path/filepath"
	"strings"

	"github.com/curtiswtaylorjr/tidyarr/internal/classify"
	"github.com/curtiswtaylorjr/tidyarr/internal/config"
	"github.com/curtiswtaylorjr/tidyarr/internal/identify"
	"github.com/curtiswtaylorjr/tidyarr/internal/mode"
	"github.com/curtiswtaylorjr/tidyarr/internal/place"
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

	// Adult identification runs through sess.Identify, which mode.Build leaves
	// nil when the AI backbone isn't configured. Fail fast with an actionable
	// message rather than nil-panicking mid-walk or burying the real "you
	// haven't configured identification" signal under N Unmatched rows.
	if sess.Mode == mode.Adult && sess.Identify == nil {
		return nil, fmt.Errorf("adult identification isn't configured — add a connection for your chosen AI provider and set the AI model in Settings, plus at least one of StashDB/FansDB/TPDB")
	}

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

	// Kids classification only ever reroutes content INTO sess.KidsRootPath —
	// it's only meaningful if that's actually one of this *arr app's own
	// currently-reported root folders (never a stale or mistyped setting).
	validRootPaths := map[string]bool{}
	for _, root := range folders {
		validRootPaths[root.Path] = true
	}
	kidsRootPath := sess.KidsRootPath
	if kidsRootPath != "" && !validRootPaths[kidsRootPath] {
		kidsRootPath = ""
	}

	var out []proposals.Proposal
	for _, root := range folders {
		for _, uf := range root.UnmappedFolders {
			if config.SidecarExts[strings.ToLower(filepath.Ext(uf.Name))] {
				continue
			}
			if sess.Mode == mode.Adult {
				out = append(out, proposeOneAdult(ctx, sess.Identify, sess.Mode, root, uf, tracked, profiles))
			} else {
				out = append(out, proposeOne(ctx, client, sess.Mode, sess.MainstreamAI, kidsRootPath, root, uf, tracked, profiles))
			}
		}
	}
	return out, nil
}

func proposeOne(
	ctx context.Context, client *servarr.Client, m mode.Mode, mainstreamAI identify.AIClient, kidsRootPath string,
	root servarr.RootFolder, uf servarr.UnmappedFolder,
	tracked []servarr.TrackedItem, profiles []servarr.QualityProfile,
) proposals.Proposal {
	p := proposals.Proposal{
		Mode: m, Workflow: proposals.Rename,
		SourceName: uf.Name, SourcePath: uf.Path, RootFolderPath: root.Path,
	}

	term := searchterm.FromName(uf.Name)
	lr, ok, reason := lookupFirst(ctx, client, term)
	if !ok && mainstreamAI != nil {
		lr, ok, reason = lookupWithAIFallback(ctx, client, mainstreamAI, uf.Name, reason)
	}
	if !ok {
		p.Status = proposals.Unmatched
		p.Reason = reason
		return p
	}

	if dup := findTrackedDuplicate(tracked, client.AppType(), lr); dup != nil {
		p.Status = proposals.Unmatched
		p.Reason = fmt.Sprintf("appears to already be tracked as %q (in %s) — leaving in place for manual review", dup.Title, dup.RootFolderPath)
		return p
	}

	targetPath := root.Path
	// Only worth classifying if a Kids path is actually configured for this
	// mode and this item wasn't already found sitting in it — an item
	// already under the Kids root is already correctly placed by whoever put
	// it there.
	if kidsRootPath != "" && kidsRootPath != root.Path {
		if classifyKids(ctx, mainstreamAI, lr).IsKids {
			targetPath = kidsRootPath
		}
	}

	p.Status = proposals.Pending
	p.Title = lr.Title
	p.TVDBID = lr.TVDBID
	p.TMDBID = lr.TMDBID
	p.RootFolderPath = targetPath
	p.QualityProfileID = servarr.DefaultQualityProfileID(tracked, targetPath, profiles)
	return p
}

// classifyKids runs the structured-signal-first, AI-fallback-second
// classification chain (see internal/classify): deterministic
// certification/genre first, falling back to mainstreamAI only when that
// signal is too weak to trust AND an AI client is actually configured. On an
// AI failure, or with no AI configured, the not-confident metadata-only
// result stands — its IsKids is already false in that case, matching the
// original CLI's "default to general" behavior when nothing resolves it.
func classifyKids(ctx context.Context, mainstreamAI identify.AIClient, lr servarr.LookupResult) classify.Result {
	result := classify.FromMetadata(classify.Signal{Certification: lr.Certification, Genres: lr.Genres})
	if result.Confident || mainstreamAI == nil {
		return result
	}
	if aiResult, err := classify.WithAI(ctx, mainstreamAI, lr.Title, lr.Overview); err == nil {
		return aiResult
	}
	return result
}

// lookupFirst runs client.Lookup for term and reports its first result.
// ok=false covers both a lookup error and an empty result set — both route to
// the same "try the AI fallback next" branch in proposeOne.
func lookupFirst(ctx context.Context, client *servarr.Client, term string) (lr servarr.LookupResult, ok bool, reason string) {
	results, err := client.Lookup(ctx, term)
	if err != nil {
		return servarr.LookupResult{}, false, fmt.Sprintf("lookup failed for search term %q: %v", term, err)
	}
	if len(results) == 0 {
		return servarr.LookupResult{}, false, fmt.Sprintf("no match for search term %q", term)
	}
	return results[0], true, ""
}

// lookupWithAIFallback asks the configured AI provider to guess the real
// title from name, then retries Lookup with that guess — Rename's fallback
// for names the *arr app's own search term couldn't resolve. firstReason
// (from the failed lookupFirst attempt) is folded into the result so a final
// Unmatched proposal explains both attempts, not just the last one.
func lookupWithAIFallback(ctx context.Context, client *servarr.Client, ai identify.AIClient, name, firstReason string) (lr servarr.LookupResult, ok bool, reason string) {
	guessed, err := identify.GuessTitle(ctx, ai, name)
	if err != nil {
		return servarr.LookupResult{}, false, fmt.Sprintf("%s, and AI title guess failed: %v", firstReason, err)
	}
	results, err := client.Lookup(ctx, guessed)
	if err != nil {
		return servarr.LookupResult{}, false, fmt.Sprintf("%s, and lookup failed for AI-guessed title %q: %v", firstReason, guessed, err)
	}
	if len(results) == 0 {
		return servarr.LookupResult{}, false, fmt.Sprintf("%s, and no match even for AI-guessed title %q", firstReason, guessed)
	}
	return results[0], true, ""
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

// proposeOneAdult resolves one unmapped folder via the AI identification
// pipeline (sess.Identify) instead of the *arr app's own TVDB/TMDB Lookup.
// Duplicate detection is intentionally skipped: TrackedItem carries no
// ForeignID/StashId to key an Adult scene against (see spec §7) — an
// already-tracked duplicate surfaces safely as Whisparr's own foreignId
// uniqueness rejection at Apply, not silent corruption.
func proposeOneAdult(
	ctx context.Context, ident *identify.Identifier, m mode.Mode,
	root servarr.RootFolder, uf servarr.UnmappedFolder,
	tracked []servarr.TrackedItem, profiles []servarr.QualityProfile,
) proposals.Proposal {
	p := proposals.Proposal{
		Mode: m, Workflow: proposals.Rename,
		SourceName: uf.Name, SourcePath: uf.Path, RootFolderPath: root.Path,
	}
	res, err := ident.Identify(ctx, uf.Name, filepath.Base(root.Path))
	p.Status, p.Reason, p.Title, p.ForeignID, p.ItemType = classifyAdultMatch(res, err)
	if res != nil {
		// Captured regardless of match outcome: an Unmatched (web-identified-only)
		// proposal still needs Studio/Date for SubmitDraft to give the scene back
		// to the community databases.
		p.Studio, p.Date = res.Studio, res.Date
	}
	if p.Status == proposals.Pending {
		p.QualityProfileID = servarr.DefaultQualityProfileID(tracked, root.Path, profiles)
	}
	return p
}

// classifyAdultMatch maps a completed Identify result to a proposal's
// identification-derived fields, or to an Unmatched reason. A match without a
// valid stash-box scene identifier (web_search-only, SceneID=="" || Box=="")
// is a correctness requirement to reject: it has no valid Whisparr ForeignID.
func classifyAdultMatch(res *identify.MatchResult, err error) (status proposals.Status, reason, title, foreignID, itemType string) {
	switch {
	case err != nil:
		return proposals.Unmatched, fmt.Sprintf("identification failed: %v", err), "", "", ""
	case res == nil:
		return proposals.Unmatched, "no confident identification", "", "", ""
	}
	foreignID, hasID := res.WhisparrForeignID()
	if !hasID {
		return proposals.Unmatched, "web-identified only (no scene ID) — needs manual review", "", "", ""
	}
	return proposals.Pending, "", res.Title, foreignID, res.Type
}

// Apply registers p's identified item with sess's Servarr app, then triggers
// a broad downloaded-scan so the app picks up the file already sitting on
// disk under p.RootFolderPath. p must be Pending — Apply refuses anything
// else (already applied, dismissed, or unmatched with nothing to register).
//
// If p was classified into a different root than it was originally found
// under (see classifyKids in Scan), the file is physically relocated into
// that root FIRST — Sonarr/Radarr can only import a file that's already
// sitting under the root folder it's being registered against. This is the
// one place Rename ever touches the filesystem directly (mirroring Dedup's
// existing os.Remove precedent for the same reason: Tidyarr runs with direct
// local access to the same paths the *arr apps report).
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

	// Structural safety guard at the mutation boundary: a Whisparr scene needs
	// BOTH a ForeignID and an ItemType, or Whisparr silently files it as a
	// mis-typed movie (its ItemType enum's zero value is "movie"). Refuse here
	// rather than trusting Scan-convention — even a hand-crafted or future-buggy
	// Adult proposal can never be registered without a real scene identifier.
	if sess.Servarr.AppType() == servarr.Whisparr && (p.ForeignID == "" || p.ItemType == "") {
		return 0, fmt.Errorf("proposal %d has no scene identifier — refusing to register it as a mis-typed movie", p.ID)
	}

	if p.SourcePath != "" && filepath.Dir(p.SourcePath) != p.RootFolderPath {
		if _, err := relocate(p.SourcePath, p.RootFolderPath); err != nil {
			return 0, fmt.Errorf("relocating %q into %q: %w", p.SourcePath, p.RootFolderPath, err)
		}
	}

	id, err := sess.Servarr.Add(ctx, servarr.AddRequest{
		Title: p.Title, TVDBID: p.TVDBID, TMDBID: p.TMDBID,
		ForeignID: p.ForeignID, ItemType: p.ItemType,
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

// SubmitDraft gives an Adult proposal's identification back to the community
// databases (TPDB preferred, StashDB as fallback — see identify.GiveBack) when
// AI+web-search confidently identified a file (Title/Studio present) but it
// matched no existing scene anywhere. This is a distinct, human-triggered
// action from Apply — unlike the original CLI, which submitted automatically
// during its scan, Tidyarr never fires an outbound mutation without an
// explicit human decision (see the design spec's staged-for-approval
// principle). p must be Unmatched and not already have a DraftID — submitting
// a draft twice for the same proposal is refused rather than silently
// duplicating it on the remote database.
func SubmitDraft(ctx context.Context, sess *mode.Session, p proposals.Proposal) (string, error) {
	if p.Workflow != proposals.Rename {
		return "", fmt.Errorf("proposal %d is a %q proposal, not rename — cannot submit a draft", p.ID, p.Workflow)
	}
	if p.Status != proposals.Unmatched {
		return "", fmt.Errorf("proposal %d is %q, not unmatched — nothing to give back", p.ID, p.Status)
	}
	if p.DraftID != "" {
		return "", fmt.Errorf("proposal %d already has a draft (%s) — refusing to submit a duplicate", p.ID, p.DraftID)
	}
	if p.Title == "" {
		return "", fmt.Errorf("proposal %d has no identified title — nothing to give back", p.ID)
	}
	if sess.Identify == nil || sess.Identify.GiveBack == nil {
		return "", fmt.Errorf("give-back isn't configured — add a TPDB or StashDB connection in Settings")
	}
	return sess.Identify.GiveBack.SubmitDraft(ctx, p.Title, p.Studio, p.Date)
}

// relocate physically moves sourcePath into destRoot, preserving its current
// basename, and returns the new path. filepath.Base already strips any
// directory components from sourcePath, so the destination Join is safe
// against a traversal-shaped source path by construction. Collision-checked
// via place.UniquePath — a Kids and general root can easily already contain
// something with the same name.
func relocate(sourcePath, destRoot string) (string, error) {
	dest := filepath.Join(destRoot, filepath.Base(sourcePath))
	unique, err := place.UniquePath(dest, func(p string) bool {
		_, err := os.Stat(p)
		return err == nil
	})
	if err != nil {
		return "", err
	}
	if err := os.Rename(sourcePath, unique); err != nil {
		return "", fmt.Errorf("moving %q to %q: %w", sourcePath, unique, err)
	}
	return unique, nil
}
