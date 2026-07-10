// Package rename implements SAK's Rename workflow: propose registering
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
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/curtiswtaylorjr/sakms/internal/classify"
	"github.com/curtiswtaylorjr/sakms/internal/config"
	"github.com/curtiswtaylorjr/sakms/internal/identify"
	"github.com/curtiswtaylorjr/sakms/internal/library"
	"github.com/curtiswtaylorjr/sakms/internal/mode"
	"github.com/curtiswtaylorjr/sakms/internal/naming"
	"github.com/curtiswtaylorjr/sakms/internal/place"
	"github.com/curtiswtaylorjr/sakms/internal/proposals"
	"github.com/curtiswtaylorjr/sakms/internal/searchterm"
	"github.com/curtiswtaylorjr/sakms/internal/servarr"
)

// yearFromReleaseDate parses the release year out of TMDB's normalized
// "YYYY-MM-DD" ReleaseDate string, returning 0 if it's empty or malformed —
// kept local to this package rather than shared with internal/dedup, same
// precedent as internal/library's own private videoExts duplication.
func yearFromReleaseDate(releaseDate string) int {
	if len(releaseDate) < 4 {
		return 0
	}
	year, err := strconv.Atoi(releaseDate[:4])
	if err != nil {
		return 0
	}
	return year
}

// Scan walks every root folder sess's Servarr app currently reports and
// produces one proposal per orphaned item: a resolved match ready to
// register (Pending), or a record of why it couldn't be resolved on its own
// (Unmatched) — surfaced either way, never silently dropped.
//
// For Adult, hasher/prober back phash-first identification: hasher computes
// each candidate's StashDB-compatible phash locally (internal/videophash) and
// prober supplies its duration for give-back (internal/mediainfo). identifyEnabled
// is Adult's per-mode toggle (resolved by the caller from settings) — when
// true, Adult resolves via the phash cascade first; when false, it goes
// straight to the legacy AI/text pipeline. The toggle is the SOLE dispatch
// gate (it replaced the old sess.Stash != nil check entirely). Movies/Series
// ignore all three (they run through ScanLibrary*, not Scan).
func Scan(ctx context.Context, sess *mode.Session, hasher PHasher, prober Prober, identifyEnabled bool) ([]proposals.Proposal, error) {
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
	var adultCandidates []adultCandidate
	for _, root := range folders {
		for _, uf := range root.UnmappedFolders {
			if config.SidecarExts[strings.ToLower(filepath.Ext(uf.Name))] {
				continue
			}
			switch {
			case sess.Mode == mode.Adult && identifyEnabled:
				// Batched through the phash-first pipeline below rather than
				// resolved here one at a time — it computes every candidate's
				// phash (bounded worker pool) then does one batched cascade lookup.
				adultCandidates = append(adultCandidates, adultCandidate{root: root, uf: uf})
			case sess.Mode == mode.Adult:
				// Toggle off -> straight to the legacy AI/text pipeline.
				out = append(out, proposeOneAdult(ctx, sess.Identify, sess.Mode, root, uf, tracked, profiles))
			default:
				out = append(out, proposeOne(ctx, client, sess.Mode, sess.MainstreamAI, kidsRootPath, root, uf, tracked, profiles))
			}
		}
	}
	if len(adultCandidates) > 0 {
		out = append(out, scanAdultPhashFirst(ctx, sess, hasher, prober, adultCandidates, tracked, profiles)...)
	}
	if sess.Mode != mode.Adult {
		out = append(out, reconcileTracked(ctx, sess.Mode, sess.MainstreamAI, kidsRootPath, folders, tracked)...)
	}
	return out, nil
}

// reconcileTracked audits every ALREADY-TRACKED item under the general or
// Kids root for kids/general misplacement — the counterpart to proposeOne's
// classification, which only ever runs on newly-found orphans. Without this,
// a tracked item's classification could drift (a re-rated title, a fixed-up
// genre list) and Rename would never surface it. Only produces proposals
// when kidsRootPath is actually configured; a no-op otherwise.
//
// general→kids is unambiguous (the destination is always kidsRootPath), but
// kids→general needs to know WHICH other root folder is "general" — only
// attempted when exactly one non-Kids root folder exists, since guessing
// among several would be exactly the kind of silent misrouting this project
// avoids. A multi-general-root setup simply won't get kids→general
// reconciliation; it still gets general→kids.
func reconcileTracked(ctx context.Context, m mode.Mode, mainstreamAI identify.AIClient, kidsRootPath string, folders []servarr.RootFolder, tracked []servarr.TrackedItem) []proposals.Proposal {
	if kidsRootPath == "" {
		return nil
	}

	var generalRoot string
	generalCount := 0
	for _, f := range folders {
		if f.Path != kidsRootPath {
			generalRoot = f.Path
			generalCount++
		}
	}
	unambiguousGeneral := generalCount == 1

	var out []proposals.Proposal
	for _, t := range tracked {
		cls := classifyKids(ctx, mainstreamAI, servarr.LookupResult{
			Title: t.Title, Certification: t.Certification, Genres: t.Genres, Overview: t.Overview,
		})

		var wantPath string
		switch {
		case cls.IsKids && t.RootFolderPath != kidsRootPath:
			wantPath = kidsRootPath
		case !cls.IsKids && t.RootFolderPath == kidsRootPath && unambiguousGeneral:
			wantPath = generalRoot
		default:
			continue // already correctly placed
		}

		out = append(out, proposals.Proposal{
			Mode: m, Workflow: proposals.Rename, Status: proposals.Pending,
			SourceName: t.Title, SourcePath: t.Path, RootFolderPath: wantPath,
			Title: t.Title, TVDBID: t.TVDBID, TMDBID: t.TMDBID, TrackedID: t.ID,
			Reason: fmt.Sprintf("currently in %s, classified kids=%v (%s) — should move to %s", t.RootFolderPath, cls.IsKids, cls.Reason, wantPath),
		})
	}
	return out
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
	res, err := ident.Identify(ctx, uf.Name, filepath.Base(root.Path))
	return buildAdultProposal(m, root, uf, res, err, tracked, profiles)
}

// buildAdultProposal assembles a Proposal from an already-resolved (or
// failed) identification result. Factored out of proposeOneAdult so
// scanAdultPhashFirst's fingerprint-cascade hits can build a Proposal the
// same way without paying for a second ident.Identify call — both callers
// are the same Adult/Whisparr pipeline, so this is ordinary same-package
// logic extraction, not the "different backend, needs its own sibling
// function" case CLAUDE.md warns against.
func buildAdultProposal(
	m mode.Mode, root servarr.RootFolder, uf servarr.UnmappedFolder,
	res *identify.MatchResult, err error,
	tracked []servarr.TrackedItem, profiles []servarr.QualityProfile,
) proposals.Proposal {
	p := proposals.Proposal{
		Mode: m, Workflow: proposals.Rename,
		SourceName: uf.Name, SourcePath: uf.Path, RootFolderPath: root.Path,
	}
	p.Status, p.Reason, p.Title, p.ForeignID, p.ItemType = classifyAdultMatch(res, err)
	if res != nil {
		// Captured regardless of match outcome: an Unmatched (web-identified-only)
		// proposal still needs Studio/Date for SubmitDraft to give the scene back
		// to the community databases.
		p.Studio, p.Date = res.Studio, res.Date
		// GiveBackBox/GiveBackSceneID are captured separately from ForeignID:
		// WhisparrForeignID() returns the SAME raw UUID string for both a
		// stashdb and a fansdb match, so ForeignID alone can't tell give-back
		// which community box to submit a fingerprint to later.
		p.GiveBackBox, p.GiveBackSceneID = res.Box, res.SceneID
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
// A nonzero p.TrackedID means p came from reconcileTracked, not proposeOne —
// the item is already tracked and just needs to move root folders, which
// Radarr/Sonarr's own UpdateRootFolder (moveFiles=true) handles entirely on
// its own side; SAK never touches that file directly.
//
// Otherwise (a new orphan), if p was classified into a different root than
// it was originally found under (see classifyKids in Scan), the file is
// physically relocated into that root FIRST — Sonarr/Radarr can only import
// a file that's already sitting under the root folder it's being registered
// against. This is the one place Rename ever touches the filesystem
// directly (mirroring Dedup's existing os.Remove precedent for the same
// reason: SAK runs with direct local access to the same paths the *arr
// apps report).
//
// If Add succeeds but the follow-up scan trigger fails, trackedID is still
// returned alongside the error: the item is genuinely registered at that
// point, so the caller should still record it as applied rather than losing
// track of it — the scan trigger can be retried independently (e.g. the
// app's own periodic scan will pick it up eventually regardless).
//
// fingerprintSubmitted reports whether an Adult proposal's phash was
// successfully given back to its origin community box (see
// submitFingerprintGiveBack) — give-back only ever runs here, after a human
// has approved Apply, never during Scan (see the design spec's
// staged-for-approval principle; the original CLI gave back during its scan
// pass, which this project deliberately does not reproduce). It's
// best-effort and never turns an otherwise-successful Apply into an error —
// the caller uses it only to decide whether to record
// p.FingerprintSubmittedAt.
func Apply(ctx context.Context, sess *mode.Session, p proposals.Proposal) (trackedID int, fingerprintSubmitted bool, err error) {
	if p.Status != proposals.Pending {
		return 0, false, fmt.Errorf("proposal %d is %q, not pending — nothing to apply", p.ID, p.Status)
	}

	if p.TrackedID != 0 {
		if err := sess.Servarr.UpdateRootFolder(ctx, p.TrackedID, p.RootFolderPath); err != nil {
			return 0, false, fmt.Errorf("reclassifying %q: %w", p.Title, err)
		}
		return p.TrackedID, false, nil
	}

	// Structural safety guard at the mutation boundary: a Whisparr scene needs
	// BOTH a ForeignID and an ItemType, or Whisparr silently files it as a
	// mis-typed movie (its ItemType enum's zero value is "movie"). Refuse here
	// rather than trusting Scan-convention — even a hand-crafted or future-buggy
	// Adult proposal can never be registered without a real scene identifier.
	if sess.Servarr.AppType() == servarr.Whisparr && (p.ForeignID == "" || p.ItemType == "") {
		return 0, false, fmt.Errorf("proposal %d has no scene identifier — refusing to register it as a mis-typed movie", p.ID)
	}

	if p.SourcePath != "" && filepath.Dir(p.SourcePath) != p.RootFolderPath {
		if _, err := Relocate(p.SourcePath, p.RootFolderPath); err != nil {
			return 0, false, fmt.Errorf("relocating %q into %q: %w", p.SourcePath, p.RootFolderPath, err)
		}
	}

	id, err := sess.Servarr.Add(ctx, servarr.AddRequest{
		Title: p.Title, TVDBID: p.TVDBID, TMDBID: p.TMDBID,
		ForeignID: p.ForeignID, ItemType: p.ItemType,
		QualityProfileID: p.QualityProfileID, RootFolderPath: p.RootFolderPath, Monitored: true,
	})
	if err != nil {
		return 0, false, fmt.Errorf("registering %q: %w", p.Title, err)
	}

	if err := sess.Servarr.ScanForDownloaded(ctx); err != nil {
		return id, false, fmt.Errorf("registered as id=%d but triggering the downloaded-files scan failed: %w", id, err)
	}
	return id, submitFingerprintGiveBack(ctx, sess, p), nil
}

// submitFingerprintGiveBack submits p's phash back to whichever community box
// it was first fingerprint-matched against (see buildAdultProposal's
// GiveBackBox/GiveBackSceneID capture). A no-op, not an error, whenever p
// wasn't a phash-cascade hit (GiveBackBox/GiveBackSceneID/PHash unset), give-back
// isn't configured, or the submission itself fails — the item is already
// genuinely registered by the time this runs, so a give-back failure must
// never surface as an Apply error. ok reports whether it actually succeeded.
func submitFingerprintGiveBack(ctx context.Context, sess *mode.Session, p proposals.Proposal) (ok bool) {
	if p.GiveBackBox == "" || p.GiveBackSceneID == "" || p.PHash == "" || p.DurationSeconds <= 0 {
		return false
	}
	if sess.Identify == nil || sess.Identify.GiveBack == nil {
		return false
	}
	err := sess.Identify.GiveBack.SubmitFingerprint(ctx, p.GiveBackBox, p.GiveBackSceneID, p.PHash, p.DurationSeconds)
	return err == nil
}

// SubmitDraft gives an Adult proposal's identification back to the community
// databases (TPDB preferred, StashDB as fallback — see identify.GiveBack) when
// AI+web-search confidently identified a file (Title/Studio present) but it
// matched no existing scene anywhere. This is a distinct, human-triggered
// action from Apply — unlike the original CLI, which submitted automatically
// during its scan, SAK never fires an outbound mutation without an
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

// Relocate physically moves sourcePath into destRoot, preserving its current
// basename, and returns the new path. filepath.Base already strips any
// directory components from sourcePath, so the destination Join is safe
// against a traversal-shaped source path by construction. Collision-checked
// via place.UniquePath — a Kids and general root can easily already contain
// something with the same name. Exported so the native search-and-grab
// import step (internal/api's check-import handler) can reuse the exact same
// move logic once a download completes, instead of duplicating it.
func Relocate(sourcePath, destRoot string) (string, error) {
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

// ScanLibrary is Rename's Movies-library counterpart to Scan — used only
// for Movies mode, now that Radarr no longer sits between SAK and the
// filesystem/TMDB (see internal/library's package doc). It walks
// rootFolderPath (and sess.KidsRootPath, if configured and different) for
// files libStore doesn't already know about, resolves each via TMDB search
// instead of Servarr's Lookup, and skips anything whose TMDB id is already
// in the library.
//
// Unlike Scan, this does NOT audit already-tracked items for kids/general
// drift (reconcileTracked's counterpart): TMDB's search response carries no
// certification/genre data the way *arr's own Lookup did, and fetching it
// per already-tracked item on every Scan would be an expensive N+1 against
// TMDB. This is a deliberate v1 simplification — new orphans are still
// classified (via AI, using title+overview only, since certification/genre
// metadata isn't available here without an extra per-item call), just not
// already-tracked items.
func ScanLibrary(ctx context.Context, sess *mode.Session, libStore *library.Store, rootFolderPath string, preset naming.Preset) ([]proposals.Proposal, error) {
	if sess.TMDB == nil {
		return nil, fmt.Errorf("tmdb isn't configured yet — add it in Settings first")
	}
	if rootFolderPath == "" {
		return nil, fmt.Errorf("no Movies library root folder configured yet — add one in Settings first")
	}

	existing, err := libStore.List(ctx, mode.Movies)
	if err != nil {
		return nil, fmt.Errorf("loading library items: %w", err)
	}
	known := make(map[string]bool, len(existing))
	byTMDB := make(map[int]bool, len(existing))
	for _, item := range existing {
		// Marking just the file path is enough — ScanRootFolder's recursive
		// walk decides atomicity dynamically from known at whatever depth it
		// encounters a directory, so it doesn't need the wrapping folder
		// pre-marked too.
		known[item.FilePath] = true
		byTMDB[item.TMDBID] = true
	}

	roots := []string{rootFolderPath}
	if sess.KidsRootPath != "" && sess.KidsRootPath != rootFolderPath {
		roots = append(roots, sess.KidsRootPath)
	}

	var out []proposals.Proposal
	for _, root := range roots {
		entries, err := library.ScanRootFolder(root, known)
		if err != nil {
			return nil, fmt.Errorf("scanning %s: %w", root, err)
		}
		for _, entry := range entries {
			if config.SidecarExts[strings.ToLower(filepath.Ext(entry.Name))] {
				continue
			}
			if naming.MatchesMovieSchema(entry.Path, preset) {
				continue // already organized under the active preset — nothing to propose
			}
			out = append(out, proposeOneLibrary(ctx, sess, byTMDB, rootFolderPath, root, entry))
		}
	}
	return out, nil
}

func proposeOneLibrary(
	ctx context.Context, sess *mode.Session, byTMDB map[int]bool,
	generalRoot, foundRoot string, entry library.UnmappedEntry,
) proposals.Proposal {
	p := proposals.Proposal{
		Mode: mode.Movies, Workflow: proposals.Rename,
		SourceName: entry.Name, SourcePath: entry.Path, RootFolderPath: foundRoot,
	}

	term := searchterm.FromName(entry.Name)
	items, err := sess.TMDB.SearchMovies(ctx, term)
	if err != nil {
		p.Status = proposals.Unmatched
		p.Reason = fmt.Sprintf("TMDB search failed for %q: %v", term, err)
		return p
	}
	if len(items) == 0 {
		p.Status = proposals.Unmatched
		p.Reason = fmt.Sprintf("no TMDB match for %q", term)
		return p
	}
	match := items[0]

	if byTMDB[match.ID] {
		p.Status = proposals.Unmatched
		p.Reason = fmt.Sprintf("appears to already be in the library as %q — leaving in place for manual review", match.Title)
		return p
	}

	targetRoot := generalRoot
	switch {
	case foundRoot == sess.KidsRootPath:
		// Already sitting under the Kids root — already correctly placed by
		// whoever put it there, same as proposeOne's rule.
		targetRoot = sess.KidsRootPath
	case sess.KidsRootPath != "" && sess.MainstreamAI != nil:
		if result, err := classify.WithAI(ctx, sess.MainstreamAI, match.Title, match.Overview); err == nil && result.IsKids {
			targetRoot = sess.KidsRootPath
		}
	}

	p.Status = proposals.Pending
	p.Title = match.Title
	p.TMDBID = match.ID
	p.Year = yearFromReleaseDate(match.ReleaseDate)
	p.RootFolderPath = targetRoot
	return p
}

// RelocateMovie moves sourcePath into a preset-formatted wrapping folder
// under destRoot — Movies' counterpart to RelocateEpisode, giving Movies
// real renaming behavior for the first time (Relocate alone only ever moves
// a file, preserving whatever name it already had). If the computed
// destination already equals sourcePath (the file is already correctly
// placed and named), this is a no-op — comparing paths up front, rather
// than always calling os.Rename, avoids place.UniquePath mistaking a file
// for colliding with itself and needlessly appending a ".2" suffix.
func RelocateMovie(sourcePath, destRoot, title string, year, tmdbID int, preset naming.Preset) (string, error) {
	folder := filepath.Join(destRoot, naming.MovieFolderName(preset, title, year, tmdbID))
	dest := filepath.Join(folder, naming.MovieFileName(preset, title, year, tmdbID, filepath.Ext(sourcePath)))
	if dest == sourcePath {
		return dest, nil
	}
	if err := os.MkdirAll(folder, 0o755); err != nil {
		return "", fmt.Errorf("creating %q: %w", folder, err)
	}
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

// ApplyLibrary is Rename's Movies-library counterpart to Apply. p must be
// Pending. Unlike Apply, there's no reconcile-drift case (ScanLibrary never
// produces one — see its doc comment), so this only ever handles a new
// orphan: resolve the actual video file (p.SourcePath may be a directory
// wrapping it, or the file itself), relocate just that file into a
// preset-formatted folder via RelocateMovie, then record it directly in
// libStore — no registration/rescan round trip needed, since
// libStore.Upsert itself IS the "now tracked" state, immediately.
func ApplyLibrary(ctx context.Context, libStore *library.Store, p proposals.Proposal, preset naming.Preset) (itemID int64, err error) {
	if p.Status != proposals.Pending {
		return 0, fmt.Errorf("proposal %d is %q, not pending — nothing to apply", p.ID, p.Status)
	}

	videoPath, err := library.ResolveVideoFile(p.SourcePath)
	if err != nil {
		return 0, fmt.Errorf("resolving the video file under %q: %w", p.SourcePath, err)
	}
	destPath, err := RelocateMovie(videoPath, p.RootFolderPath, p.Title, p.Year, p.TMDBID, preset)
	if err != nil {
		return 0, fmt.Errorf("relocating %q into %q: %w", videoPath, p.RootFolderPath, err)
	}

	item, err := libStore.Upsert(ctx, library.Item{
		Mode: mode.Movies, TMDBID: p.TMDBID, Title: p.Title, Year: p.Year,
		FilePath: destPath, RootFolderPath: p.RootFolderPath,
	})
	if err != nil {
		return 0, fmt.Errorf("recording %q in the library: %w", p.Title, err)
	}
	return item.ID, nil
}

// episodeKey identifies one already-tracked-with-a-file episode, for
// ScanLibrarySeries' duplicate-skip check.
type episodeKey struct {
	tmdbID, season, episode int
}

// ScanLibrarySeries is Rename's Series-library counterpart to Scan — used
// only once Series stops requiring Sonarr (see the plan this was built
// from, Stage 2). It walks rootFolderPath (and sess.KidsRootPath, if
// configured and different) for orphaned files or season-pack folders,
// resolves each file's season/episode from its own name, resolves the show
// via TMDB search, and skips anything already tracked WITH a file — an
// episode TMDB previously reported as missing (file_path == "") is NOT
// skipped, since finding its file is exactly what fills that gap in.
//
// One proposal per resolved episode file, never one per season-pack folder
// — same "surface everything individually" posture ScanLibrary (Movies)
// and every other workflow already follows.
func ScanLibrarySeries(ctx context.Context, sess *mode.Session, libStore *library.Store, rootFolderPath string, preset naming.Preset) ([]proposals.Proposal, error) {
	if sess.TMDB == nil {
		return nil, fmt.Errorf("tmdb isn't configured yet — add it in Settings first")
	}
	if rootFolderPath == "" {
		return nil, fmt.Errorf("no Series library root folder configured yet — add one in Settings first")
	}

	allSeries, err := libStore.ListSeries(ctx)
	if err != nil {
		return nil, fmt.Errorf("loading series: %w", err)
	}

	known := map[string]bool{}
	tracked := map[episodeKey]bool{}
	for _, series := range allSeries {
		episodes, err := libStore.ListEpisodes(ctx, series.ID)
		if err != nil {
			return nil, fmt.Errorf("loading episodes for %q: %w", series.Title, err)
		}
		for _, ep := range episodes {
			if ep.FilePath == "" {
				continue // known from TMDB but not on disk yet — not a duplicate
			}
			// Marking just the file path is enough — ScanRootFolder's
			// recursive walk decides atomicity dynamically from known at
			// whatever depth it encounters a directory, so a new season
			// added later (or a new file dropped next to this one) is still
			// discovered rather than masked by pre-marking ancestor dirs.
			known[ep.FilePath] = true
			tracked[episodeKey{tmdbID: series.TMDBID, season: ep.SeasonNumber, episode: ep.EpisodeNumber}] = true
		}
	}

	roots := []string{rootFolderPath}
	if sess.KidsRootPath != "" && sess.KidsRootPath != rootFolderPath {
		roots = append(roots, sess.KidsRootPath)
	}

	var out []proposals.Proposal
	for _, root := range roots {
		entries, err := library.ScanRootFolder(root, known)
		if err != nil {
			return nil, fmt.Errorf("scanning %s: %w", root, err)
		}
		for _, entry := range entries {
			if config.SidecarExts[strings.ToLower(filepath.Ext(entry.Name))] {
				continue
			}
			videoFiles, err := library.ResolveEpisodeVideoFiles(entry.Path)
			if err != nil {
				out = append(out, proposals.Proposal{
					Mode: mode.Series, Workflow: proposals.Rename, Status: proposals.Unmatched,
					SourceName: entry.Name, SourcePath: entry.Path, RootFolderPath: root,
					Reason: fmt.Sprintf("no video file found under %q: %v", entry.Path, err),
				})
				continue
			}
			for _, videoPath := range videoFiles {
				if naming.MatchesSeriesSchema(videoPath, preset) {
					continue // already organized under the active preset — nothing to propose
				}
				out = append(out, proposeOneEpisodeLibrary(ctx, sess, tracked, rootFolderPath, root, videoPath))
			}
		}
	}
	return out, nil
}

func proposeOneEpisodeLibrary(
	ctx context.Context, sess *mode.Session, tracked map[episodeKey]bool,
	generalRoot, foundRoot, videoPath string,
) proposals.Proposal {
	name := filepath.Base(videoPath)
	p := proposals.Proposal{
		Mode: mode.Series, Workflow: proposals.Rename,
		SourceName: name, SourcePath: videoPath, RootFolderPath: foundRoot,
	}

	season, episode, ok := library.ParseEpisodeFilename(name)
	if !ok {
		p.Status = proposals.Unmatched
		p.Reason = fmt.Sprintf("could not determine season/episode from %q", name)
		return p
	}

	term := searchterm.FromName(library.StripEpisodeMarker(name))
	items, err := sess.TMDB.SearchTV(ctx, term)
	if err != nil {
		p.Status = proposals.Unmatched
		p.Reason = fmt.Sprintf("TMDB search failed for %q: %v", term, err)
		return p
	}
	if len(items) == 0 {
		p.Status = proposals.Unmatched
		p.Reason = fmt.Sprintf("no TMDB match for %q", term)
		return p
	}
	match := items[0]

	if tracked[episodeKey{tmdbID: match.ID, season: season, episode: episode}] {
		p.Status = proposals.Unmatched
		p.Reason = fmt.Sprintf("appears to already be in the library as %q S%02dE%02d — leaving in place for manual review", match.Title, season, episode)
		return p
	}

	// Confirming the season actually exists in TMDB before accepting the
	// filename-parsed season/episode — a cheap sanity check against a
	// bogus parse landing a file under the wrong show/season. Whether the
	// exact episode number is IN that season's list isn't required (the
	// filename parse is trusted over TMDB's completeness); TMDB reporting
	// the season at all is confirmation enough.
	if _, err := sess.TMDB.SeasonDetails(ctx, match.ID, season); err != nil {
		p.Status = proposals.Unmatched
		p.Reason = fmt.Sprintf("could not confirm season %d of %q via TMDB: %v", season, match.Title, err)
		return p
	}

	targetRoot := generalRoot
	switch {
	case foundRoot == sess.KidsRootPath:
		targetRoot = sess.KidsRootPath
	case sess.KidsRootPath != "" && sess.MainstreamAI != nil:
		if result, err := classify.WithAI(ctx, sess.MainstreamAI, match.Title, match.Overview); err == nil && result.IsKids {
			targetRoot = sess.KidsRootPath
		}
	}

	p.Status = proposals.Pending
	p.Title = match.Title
	p.TMDBID = match.ID
	p.Year = yearFromReleaseDate(match.ReleaseDate)
	p.SeasonNumber = season
	p.EpisodeNumber = episode
	p.RootFolderPath = targetRoot
	return p
}

// RelocateEpisode moves sourcePath into a preset-formatted
// destRoot/Series Folder/Season NN/, naming the destination file via
// naming.EpisodeFileName rather than preserving sourcePath's original
// basename (unlike Relocate) — an episode's original release name carries
// no useful organization on its own the way a movie's own wrapping folder
// often already does, so Series needs Rename to actually impose the
// season-folder structure. No episode title is threaded through here
// (proposals.Proposal carries none — see ScanLibrarySeries) — a deliberate
// v1 simplification; EpisodeFileName handles an empty title by simply
// omitting that segment. If the computed destination already equals
// sourcePath, this is a no-op — same self-collision guard RelocateMovie
// uses.
func RelocateEpisode(sourcePath, destRoot, seriesTitle string, seriesYear, tmdbID, seasonNumber, episodeNumber int, preset naming.Preset) (string, error) {
	seriesFolder := naming.SeriesFolderName(preset, seriesTitle, seriesYear, tmdbID)
	seasonDir := filepath.Join(destRoot, seriesFolder, naming.SeasonDirName(seasonNumber))
	dest := filepath.Join(seasonDir, naming.EpisodeFileName(preset, seriesTitle, seasonNumber, episodeNumber, "", filepath.Ext(sourcePath)))
	if dest == sourcePath {
		return dest, nil
	}

	if err := os.MkdirAll(seasonDir, 0o755); err != nil {
		return "", fmt.Errorf("creating %q: %w", seasonDir, err)
	}
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

// ApplyLibrarySeries is Rename's Series-library counterpart to ApplyLibrary.
// p must be Pending. Relocates the file via RelocateEpisode, then
// get-or-creates the series (by TMDB id) and upserts the episode row.
// Existing title/air-date metadata for this exact episode (e.g. from a
// prior Sonarr import or Scan reporting it as missing) is preserved rather
// than blanked out, since this Apply call only ever supplies a file path,
// never episode metadata of its own.
func ApplyLibrarySeries(ctx context.Context, libStore *library.Store, p proposals.Proposal, preset naming.Preset) (episodeID int64, err error) {
	if p.Status != proposals.Pending {
		return 0, fmt.Errorf("proposal %d is %q, not pending — nothing to apply", p.ID, p.Status)
	}

	moved, err := RelocateEpisode(p.SourcePath, p.RootFolderPath, p.Title, p.Year, p.TMDBID, p.SeasonNumber, p.EpisodeNumber, preset)
	if err != nil {
		return 0, fmt.Errorf("relocating %q: %w", p.SourcePath, err)
	}

	series, err := libStore.UpsertSeries(ctx, library.Series{
		TMDBID: p.TMDBID, Title: p.Title, Year: p.Year, RootFolderPath: p.RootFolderPath,
	})
	if err != nil {
		return 0, fmt.Errorf("recording series %q: %w", p.Title, err)
	}

	title, airDate := "", ""
	if existing, err := libStore.GetEpisode(ctx, series.ID, p.SeasonNumber, p.EpisodeNumber); err == nil {
		title, airDate = existing.Title, existing.AirDate
	} else if !errors.Is(err, library.ErrNotFound) {
		return 0, fmt.Errorf("checking existing episode metadata: %w", err)
	}

	ep, err := libStore.UpsertEpisode(ctx, library.Episode{
		SeriesID: series.ID, SeasonNumber: p.SeasonNumber, EpisodeNumber: p.EpisodeNumber,
		Title: title, AirDate: airDate, FilePath: moved,
	})
	if err != nil {
		return 0, fmt.Errorf("recording episode: %w", err)
	}
	return ep.ID, nil
}
