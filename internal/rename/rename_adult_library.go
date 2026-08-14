package rename

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/labbersanon/sakms/internal/config"
	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/naming"
	"github.com/labbersanon/sakms/internal/place"
	"github.com/labbersanon/sakms/internal/proposals"
)

// ScanLibraryAdult is Rename's Adult-library scan — the library-backed path
// used once Adult stopped requiring Whisparr (see the plan this was built
// from, Stage 2). It walks rootFolderPath for files libStore doesn't already
// know about, resolves each to its video file, runs it through the same
// phash-first identification cascade (identifyAdultFiles: local phash ->
// LookupFingerprints -> Identify fallback), and builds one proposal per
// resolved scene.
//
// hasher/prober are threaded in exactly as Scan takes them — the cascade
// computes each candidate's phash+duration locally, and neither client lives
// on mode.Session, so they can't be sourced any other way.
//
// The real improvement the library-owned (box, scene_id) key unlocks over the
// Whisparr path: a scene whose identification already resolves to a tracked
// (box, scene_id) is skipped up front via GetScene, rather than punting
// duplicate detection to "Whisparr's own foreignId uniqueness rejection at
// Apply time" the way the Servarr-backed Adult path had to.
func ScanLibraryAdult(ctx context.Context, sess *mode.Session, libStore *library.Store, hasher PHasher, prober Prober, rootFolderPath, aspect string) ([]proposals.Proposal, error) {
	if sess.Identify == nil {
		return nil, fmt.Errorf("adult identification isn't configured — add a connection for your chosen AI provider and set the AI model in Settings, plus at least one of StashDB/FansDB/TPDB")
	}
	if rootFolderPath == "" {
		return nil, fmt.Errorf("no Adult library root folder configured yet — add one in Settings first")
	}

	// Claude 2026-08-12: use AllScenePaths instead of ListScenes for the known map.
	// Reason: deep-interview-adult-rename-review-alts B1 — library_scene_files
	//   holds non-primary (alternate) paths that ListScenes never returns, so a
	//   folded alternate was re-discovered as an orphan on the next Scan. Mirrors
	//   ScanLibrary's AllFilePaths feed (rename.go:175-186) and ScanLibrarySeries'
	//   AllEpisodeFilePaths feed (rename.go:701-712), including the soft-fail-on-error
	//   style: if AllScenePaths fails, fall back to ListScenes so Scan still runs.
	// Troubleshooting: after an alternate fold, the demoted file re-appeared as a
	//   Rename proposal on the next Scan.
	// Review if: AllScenePaths stops covering library_scene_files.
	known := map[string]bool{}
	if paths, err := libStore.AllScenePaths(ctx); err == nil {
		for _, p := range paths {
			known[p] = true
		}
	} else {
		scenes, listErr := libStore.ListScenes(ctx)
		if listErr != nil {
			return nil, fmt.Errorf("loading library scenes: %w", listErr)
		}
		for _, sc := range scenes {
			known[sc.FilePath] = true
		}
	}

	entries, err := library.ScanRootFolder(rootFolderPath, known)
	if err != nil {
		return nil, fmt.Errorf("scanning %s: %w", rootFolderPath, err)
	}

	// First pass: resolve each entry to an allowlisted video file, dropping
	// sidecars and silently omitting non-video / empty-of-video paths. Only
	// survivors go through the (batched) identification cascade.
	type candidate struct {
		entry     library.UnmappedEntry
		videoPath string
	}
	var candidates []candidate
	var out []proposals.Proposal
	for _, entry := range entries {
		if config.SidecarExts[strings.ToLower(filepath.Ext(entry.Name))] {
			continue
		}
		videoPath, err := library.ResolveVideoFile(entry.Path)
		if err != nil {
			continue // silent omit — non-video / empty of video
		}
		if naming.MatchesAdultSchema(videoPath) {
			continue // already organized to SAK's Adult scheme — nothing to propose
		}
		candidates = append(candidates, candidate{entry: entry, videoPath: videoPath})
	}

	files := make([]adultFileID, len(candidates))
	for i, c := range candidates {
		// parentName is the video file's immediate parent folder (a studio-
		// named folder is useful identification context), which collapses to
		// the root folder's own base name for a flat loose scene — a deliberate
		// refinement of the Servarr path's filepath.Base(root.Path), not an
		// oversight of "exact same approach."
		files[i] = adultFileID{
			path:       c.videoPath,
			stem:       filepath.Base(c.videoPath),
			parentName: filepath.Base(filepath.Dir(c.videoPath)),
		}
	}
	ids := identifyAdultFiles(ctx, sess, hasher, prober, files)

	for i, c := range candidates {
		p := buildAdultLibraryProposal(ctx, libStore, rootFolderPath, c.entry, c.videoPath, ids[i])
		if !keepAdultOrganizeProposal(ctx, libStore, aspect, p, ids[i]) {
			continue
		}
		out = append(out, p)
	}
	return out, nil
}

func keepAdultOrganizeProposal(ctx context.Context, libStore *library.Store, aspect string, p proposals.Proposal, id adultIdentification) bool {
	if aspect == "" {
		return true
	}
	if p.GiveBackBox != "" && p.GiveBackSceneID != "" {
		if sc, err := libStore.GetScene(ctx, p.GiveBackBox, p.GiveBackSceneID); err == nil {
			return library.MatchesPosterAspect(sc.PosterAspectClass, aspect)
		}
	}
	img := ""
	if id.match != nil {
		img = id.match.Image
	}
	return library.ProbePosterAspect(ctx, img) == aspect
}

// buildAdultLibraryProposal assembles one library-backed Adult proposal from
// an already-resolved identification, mapping the identify.MatchResult's
// fields (Title/Studio/Date/ForeignID/ItemType/GiveBackBox/GiveBackSceneID),
// minus any Servarr-only QualityProfileID plumbing. The one behavioral
// addition: a Pending match
// whose (box, scene_id) is already tracked is demoted to Unmatched here
// (pre-Apply dedup), instead of being proposed again.
func buildAdultLibraryProposal(
	ctx context.Context, libStore *library.Store, rootFolderPath string,
	entry library.UnmappedEntry, videoPath string, id adultIdentification,
) proposals.Proposal {
	p := proposals.Proposal{
		Mode: mode.Adult, Workflow: proposals.Rename,
		SourceName: entry.Name, SourcePath: videoPath, RootFolderPath: rootFolderPath,
	}
	status, reason, title, foreignID, itemType := classifyAdultMatch(id.match, id.err)

	// Pre-Apply dedup — the improvement the (box, scene_id) library key unlocks.
	// classifyAdultMatch only returns Pending when the match carries a valid
	// Box+SceneID (WhisparrForeignID ok), so GetScene's key is safe here. Keyed
	// on the RAW Box/SceneID, never WhisparrForeignID(), which collapses
	// stashdb/fansdb to the same shape and tpdb-prefixes — the library table is
	// keyed on the raw pair for exactly that reason.
	//
	// Claude 2026-08-12: was demote-to-Unmatched; now becomes Pending alternate.
	// Reason: deep-interview-adult-rename-review-alts B3 — a second copy of a
	//   tracked scene should fold as primary-or-alternate by quality, not sit
	//   forever as an Unmatched manual-review row. acceptDuplicatePendingScene
	//   copies identity from the tracked row (the authoritative source) so
	//   applyAdultAlternate can build correct filenames for both the incumbent
	//   and the orphan. The proposal must NOT return early after the duplicate
	//   check — it must fall through to the phash/duration capture below so the
	//   alternate has its own hash for its filename. identitySet guards the
	//   second assignment block so it doesn't overwrite what acceptDuplicatePendingScene set.
	// Troubleshooting: was: p.Status=Unmatched, "leaving in place for manual review".
	// Review if: the alternate fold gate in ApplyLibraryAdult is removed.
	identitySet := false
	if status == proposals.Pending {
		existing, err := libStore.GetScene(ctx, id.match.Box, id.match.SceneID)
		switch {
		case err == nil:
			// acceptDuplicatePendingScene overwrites Title/Studio/Date/GiveBack*
			// from the tracked row, which is authoritative. Set the base fields
			// first (ForeignID/ItemType come from the identify result, not the row).
			p.ForeignID, p.ItemType = foreignID, itemType
			if id.match != nil {
				p.GiveBackBox, p.GiveBackSceneID = id.match.Box, id.match.SceneID
			}
			acceptDuplicatePendingScene(&p, existing)
			identitySet = true
			// Fall through to phash/duration capture below — do NOT return.
		case !errors.Is(err, library.ErrNotFound):
			p.Status = proposals.Unmatched
			p.Reason = fmt.Sprintf("could not check whether %q is already tracked: %v", title, err)
			return p
		}
	}

	if !identitySet {
		p.Status, p.Reason, p.Title, p.ForeignID, p.ItemType = status, reason, title, foreignID, itemType
	}
	if id.match != nil {
		if !identitySet {
			// Captured regardless of match outcome: an Unmatched (web-identified-
			// only) proposal still needs Studio/Date for SubmitDraft's give-back.
			p.Studio, p.Date = id.match.Studio, id.match.Date
			// GiveBackBox/GiveBackSceneID are the raw Box/SceneID, kept separate
			// from ForeignID for the same reason the library key is (above).
			p.GiveBackBox, p.GiveBackSceneID = id.match.Box, id.match.SceneID
		}
	}
	if id.hashed {
		p.PHash = id.phash
		p.DurationSeconds = id.duration
	}

	// Phash library dedup for web-identified-only matches: after Review mints a
	// local identity, duplicate downloads in scenes/ keep identifying as
	// web-identified because the stash-box cascade does not consult local rows.
	// Route into the same Pending alternate fold catalog duplicates use.
	if !identitySet && p.PHash != "" {
		existing, err := libStore.GetSceneByPHash(ctx, p.PHash)
		switch {
		case err == nil && existing != nil && existing.FilePath != videoPath:
			acceptDuplicatePendingScene(&p, existing)
		case err != nil && !errors.Is(err, library.ErrNotFound):
			p.Status = proposals.Unmatched
			p.Reason = fmt.Sprintf("could not check whether phash is already tracked: %v", err)
		}
	}
	return p
}

// ApplyLibraryAdult is Rename's Adult-library counterpart to ApplyLibrary.
// p must be Pending. It resolves the video file, relocates+renames it to the
// naming.AdultFileName-computed target directly under p.RootFolderPath (an
// Adult scene is a flat one-file thing — no wrapping folder like Movies), then
// records it in libStore via UpsertScene. libStore.UpsertScene itself IS the
// "now tracked" state, immediately — no registration/rescan round trip.
//
// sess is threaded in ONLY for fingerprint give-back (submitFingerprintGiveBack
// needs sess.Identify.GiveBack) — best-effort, never turning an otherwise-
// successful Apply into an error. There is no Servarr write and no
// QualityProfileID here. fingerprintSubmitted is returned so the caller can
// persist FingerprintSubmittedAt (the never-submit-twice guard).
//
// changes is a named return so a post-move failure (e.g. UpsertScene) still
// reports the committed file move to the caller for Session.NotifyPlayers —
// the physical relocate already happened by then (partial-success rule).
//
// tier is the operator's configured adult_quality_tier, recorded on the scene
// row. Plain string, not a settings lookup, because this package deliberately
// does not import internal/settings — the internal/api caller resolves it.
//
// ACCEPTED IMPRECISION, deliberate — do not "fix" this by adding a
// grabs.quality_tier column: for Adult, this is the tier in force at APPLY
// time, not at grab time. When importGrabContent's OrganizeImportedAdult
// succeeds, the scene row is written at import with that moment's tier — this
// Apply path only runs for files that landed without identity (identify miss
// or pre-auto-organize imports). If an operator changes adult_quality_tier
// between a bare import and this Apply, the recorded tier is the new one.
// Movies/Series don't have this window because their rows are always written
// at import. The only thing that would close the bare-import window is a
// schema column on grabs existing solely for it, which is exactly the
// premature abstraction CLAUDE.md bans.
// Claude 2026-08-12: added prober parameter; added alternate-fold gate.
// Reason: deep-interview-adult-rename-review-alts C6 — ApplyLibraryAdult now has
//
//	a promote/demote branch (applyAdultAlternate). prober is required for quality
//	ranking; the fold gate fires when the target (box, scene_id) is already tracked
//	with a live file. viaAlternateFold is promoted, NEVER true — undo_apply.go:190-193
//	short-circuits the file move on that flag and blanket-setting it would make
//	every lose/tie Adult Apply un-undoable. The stale Claude block below
//	("viaAlternateFold is ALWAYS false for Adult — no promote/demote branch") has
//	been met (Review if: ApplyLibraryAdult gains an alternate/promote branch) and
//	is removed and replaced with this one.
//
// Review if: the alternate fold is removed or quality ranking moves off prober.
func ApplyLibraryAdult(ctx context.Context, sess *mode.Session, libStore *library.Store, p proposals.Proposal, tier string, prober Prober) (sceneID int64, fingerprintSubmitted bool, changes []mode.PathChange, err error) {
	if p.Status != proposals.Pending {
		return 0, false, nil, fmt.Errorf("proposal %d is %q, not pending — nothing to apply", p.ID, p.Status)
	}
	// Structural safety guard at the mutation boundary: a scene's library row
	// is keyed on (box, scene_id), so refuse to record one without a real
	// identity rather than writing a row keyed on empty strings.
	if p.GiveBackBox == "" || p.GiveBackSceneID == "" {
		return 0, false, nil, fmt.Errorf("proposal %d has no scene identifier — refusing to record it", p.ID)
	}

	videoPath, err := library.ResolveVideoFile(p.SourcePath)
	if err != nil {
		return 0, false, nil, fmt.Errorf("resolving the video file under %q: %w", p.SourcePath, err)
	}

	// Claude 2026-08-10: read the pre-Apply scene row before anything mutates it.
	// Reason: deep-interview-rename-undo — undo needs the prior row to restore,
	//   and UpsertScene's ON CONFLICT(box, scene_id) destroys it. Unlike Movies
	//   (GetByTMDBID) and Series (the fold-in gate's `resolved` map), Adult's
	//   Apply had no pre-existing lookup to reuse, so this read is ADDED here
	//   rather than derived later — there is no later. A lookup failure other
	//   than "not found" is swallowed, leaving existingScene nil, which undo
	//   reads as "this Apply INSERTed the row": losing undoability must never
	//   fail an Apply that would otherwise succeed.
	// Troubleshooting: undoing an Adult Apply DELETED a scene row that existed
	//   beforehand instead of restoring it — this read ran too late, or its
	//   error was treated as fatal.
	// Review if: library_scenes stops being keyed UNIQUE(box, scene_id), or
	//   ApplyLibraryAdult grows an earlier lookup this can reuse.
	existingScene, sceneErr := libStore.GetScene(ctx, p.GiveBackBox, p.GiveBackSceneID)
	if sceneErr != nil {
		existingScene = nil
	}

	// Alternate fold gate: if the scene is already tracked with a live file,
	// route into applyAdultAlternate instead of the simple path.
	// fileExists reuses series_alternates.go:324 — same package, not duplicated.
	// A stale row pointing at a deleted file is not an occupied slot.
	if existingScene != nil && existingScene.FilePath != "" && fileExists(existingScene.FilePath) {
		altSceneID, altChanges, promoted, altErr := applyAdultAlternate(ctx, libStore, existingScene, p, videoPath, tier, prober)
		if altErr != nil {
			return altSceneID, false, altChanges, altErr
		}
		persistAdultSceneCatalogTags(ctx, sess, libStore, altSceneID, p.GiveBackBox, p.GiveBackSceneID, "")
		submitted := submitFingerprintGiveBack(ctx, sess, p)
		landed := lastCreatedPath(altChanges, videoPath)
		recordUndoArchive(ctx, mode.Adult, p, videoPath,
			sceneTouchedRows(existingScene, altSceneID),
			landed, library.FileSize(landed), promoted, submitted)
		return altSceneID, submitted, altChanges, nil
	}

	destPath, err := RelocateAdultScene(videoPath, p.RootFolderPath, p.Studio, p.Title, p.Date, p.PHash)
	if err != nil {
		return 0, false, nil, fmt.Errorf("relocating %q into %q: %w", videoPath, p.RootFolderPath, err)
	}
	// RelocateAdultScene's self-collision guard means destPath can equal
	// videoPath (already correctly named — no os.Rename happened). Only report
	// a change when a move actually occurred, to avoid a bogus notify.
	if destPath != videoPath {
		changes = []mode.PathChange{{Path: videoPath, Kind: mode.Deleted}, {Path: destPath, Kind: mode.Created}}
	}

	// Cache the SAK-computed phash against the moved file's identity key so
	// Stage-2 Dedup can trust it without recomputing (os.Rename preserves
	// mtime, so a stat of destPath describes the exact file this hash is for).
	var fileSize int64
	var fileMTime string
	if info, statErr := os.Stat(destPath); statErr == nil {
		fileSize = info.Size()
		fileMTime = info.ModTime().UTC().Format(time.RFC3339Nano)
	}

	scene, err := libStore.UpsertScene(ctx, library.Scene{
		Box: p.GiveBackBox, SceneID: p.GiveBackSceneID,
		Title: p.Title, Studio: p.Studio, Date: p.Date,
		FilePath: destPath, RootFolderPath: p.RootFolderPath,
		// Reuses the fileSize stat'd just above — Size and PHashFileSize hold
		// the same bytes here but mean different things (PHashFileSize is a
		// cache-validation key allowed to go stale; Size is the storage total),
		// so they are written as separate fields rather than one shared column.
		Size: fileSize, QualityTier: tier,
		PHash: p.PHash, PHashFileSize: fileSize, PHashFileMTime: fileMTime,
	})
	if err != nil {
		return 0, false, changes, fmt.Errorf("recording scene %q in the library: %w", p.Title, err)
	}
	persistAdultSceneCatalogTags(ctx, sess, libStore, scene.ID, p.GiveBackBox, p.GiveBackSceneID, "")

	submitted := submitFingerprintGiveBack(ctx, sess, p)
	// Claude 2026-08-12: simple path — viaAlternateFold is false because no
	//   promote/demote occurred here (the fold gate above handled that case).
	//   `submitted` is threaded in because it is otherwise unrecoverable at undo
	//   time: it is persisted onto the PROPOSAL via MarkFingerprintSubmitted, and
	//   this entry's proposal snapshot is the PRE-Apply row, where it is empty by
	//   definition — so reading the live proposal later cannot distinguish "this
	//   Apply submitted" from "a later manual retry did".
	// Review if: fingerprint submission moves off the proposal row.
	recordUndoArchive(ctx, mode.Adult, p, videoPath,
		sceneTouchedRows(existingScene, scene.ID), destPath, fileSize, false, submitted)
	return scene.ID, submitted, changes, nil
}

// RelocateAdultScene moves sourcePath directly under destRoot, renaming it to
// naming.AdultFileName — Adult's flat-file counterpart to RelocateMovie/
// RelocateEpisode (a scene has no wrapping folder or season structure of its
// own to impose). If the computed destination already equals sourcePath, this
// is a no-op — the same self-collision guard RelocateMovie/RelocateEpisode use,
// so re-applying an already-correctly-named scene doesn't append a ".2" suffix.
func RelocateAdultScene(sourcePath, destRoot, studio, title, date, phash string) (string, error) {
	dest := filepath.Join(destRoot, naming.AdultFileName(studio, title, date, phash, filepath.Ext(sourcePath)))
	if dest == sourcePath {
		return dest, nil
	}
	if err := os.MkdirAll(destRoot, 0o755); err != nil {
		return "", fmt.Errorf("creating %q: %w", destRoot, err)
	}
	unique, err := place.UniquePath(dest, func(p string) bool {
		_, err := os.Stat(p)
		return err == nil
	})
	if err != nil {
		return "", err
	}
	if err := place.Move(sourcePath, unique); err != nil {
		return "", fmt.Errorf("moving %q to %q: %w", sourcePath, unique, err)
	}
	return unique, nil
}

// OrganizeImportedAdult identifies a just-imported Adult video and, on a
// confident match with (box, scene_id), renames/moves it via RelocateAdultScene
// and records the library_scenes row — the same end state as ApplyLibraryAdult,
// without a Rename proposal. Grab approval is the operator consent.
//
// Claude 2026-08-11: auto rename+move on Adult grab import.
// Reason: finished grabs were left as raw torrent basenames under /adult until
// a manual Rename Apply; operator expects download → library name in one step.
// Troubleshooting: Step Sis landed as "[Familylust.com] …mp4" on Adult-NAS.
// Review if: Adult import gains a settings toggle to keep proposal-gated Apply.
//
// On identify failure or Unmatched, returns ("", nil, nil) so the caller keeps
// the bare Relocate result and a later Rename scan can still pick the file up.
func OrganizeImportedAdult(ctx context.Context, sess *mode.Session, libStore *library.Store, hasher PHasher, prober Prober, videoPath, rootFolderPath, tier string) (finalPath string, changes []mode.PathChange, err error) {
	if videoPath == "" || rootFolderPath == "" {
		return "", nil, nil
	}
	if naming.MatchesAdultSchema(videoPath) {
		return videoPath, nil, nil
	}
	if sess == nil || sess.Identify == nil || hasher == nil {
		return "", nil, nil
	}
	ids := identifyAdultFiles(ctx, sess, hasher, prober, []adultFileID{{
		path:       videoPath,
		stem:       filepath.Base(videoPath),
		parentName: filepath.Base(filepath.Dir(videoPath)),
	}})
	if len(ids) == 0 {
		return "", nil, nil
	}
	id := ids[0]
	status, _, title, _, _ := classifyAdultMatch(id.match, id.err)
	if status != proposals.Pending || id.match == nil || id.match.Box == "" || id.match.SceneID == "" {
		return "", nil, nil
	}
	if existing, gerr := libStore.GetScene(ctx, id.match.Box, id.match.SceneID); gerr == nil {
		log.Printf("rename: organize import skipped %q — already tracked as %q", videoPath, existing.Title)
		return "", nil, nil
	} else if gerr != nil && !errors.Is(gerr, library.ErrNotFound) {
		return "", nil, fmt.Errorf("checking library for %q: %w", title, gerr)
	}

	phash := id.phash
	destPath, err := RelocateAdultScene(videoPath, rootFolderPath, id.match.Studio, id.match.Title, id.match.Date, phash)
	if err != nil {
		return "", nil, fmt.Errorf("relocating identified scene: %w", err)
	}
	if destPath != videoPath {
		changes = []mode.PathChange{{Path: videoPath, Kind: mode.Deleted}, {Path: destPath, Kind: mode.Created}}
	}

	var fileSize int64
	var fileMTime string
	if info, statErr := os.Stat(destPath); statErr == nil {
		fileSize = info.Size()
		fileMTime = info.ModTime().UTC().Format(time.RFC3339Nano)
	}
	aspect := library.ProbePosterAspect(ctx, id.match.Image)
	scene, err := libStore.UpsertScene(ctx, library.Scene{
		Box: id.match.Box, SceneID: id.match.SceneID,
		Title: id.match.Title, Studio: id.match.Studio, Date: id.match.Date,
		FilePath: destPath, RootFolderPath: rootFolderPath,
		Size: fileSize, QualityTier: tier,
		PHash: phash, PHashFileSize: fileSize, PHashFileMTime: fileMTime,
		PosterAspectClass: aspect,
		PosterURL:         id.match.Image,
	})
	if err != nil {
		return destPath, changes, fmt.Errorf("recording organized scene: %w", err)
	}
	// Claude 2026-08-14: persist catalog tags at grab-import.
	// Reason: Clean-up matches library_scene_tags only; MatchResult.Tags was
	//   populated at identify time and never written, so tags-only rules
	//   scanned 0 hits against a fully identified Adult library.
	// Troubleshooting: BDSM Clean-up rule (Bondage/Bound/Dungeon/Pee/Peeing)
	//   previewed and scanned empty while 250 stashdb/tpdb scenes existed.
	// Review if: UpsertScene itself writes tags from a Scene.Tags field.
	if err := libStore.ApplyCatalogTags(ctx, scene.ID, id.match.Tags); err != nil {
		return destPath, changes, fmt.Errorf("recording catalog tags for %q: %w", id.match.Title, err)
	}
	return destPath, changes, nil
}

// persistAdultSceneCatalogTags fill-if-empty writes catalog genre names onto
// library_scene_tags. Best-effort: a lookup or insert failure is logged, never
// turned into an Apply error — the file is already in the library.
func persistAdultSceneCatalogTags(ctx context.Context, sess *mode.Session, libStore *library.Store, sceneID int64, box, catalogID, joined string) {
	if sceneID == 0 {
		return
	}
	existing, err := libStore.SceneTags(ctx, sceneID)
	if err != nil {
		log.Printf("rename: listing tags for scene %d: %v", sceneID, err)
		return
	}
	if len(existing) > 0 {
		return
	}
	if joined == "" {
		joined = catalogTagsByID(ctx, sess, box, catalogID)
	}
	if joined == "" {
		return
	}
	if err := libStore.ApplyCatalogTags(ctx, sceneID, joined); err != nil {
		log.Printf("rename: persisting catalog tags for scene %d: %v", sceneID, err)
	}
}

func catalogTagsByID(ctx context.Context, sess *mode.Session, box, catalogID string) string {
	if sess == nil || sess.Identify == nil || sess.Identify.Boxes == nil || box == "" || catalogID == "" {
		return ""
	}
	if sess.Identify.Throttle != nil {
		if err := sess.Identify.Throttle.Wait(ctx, box); err != nil {
			return ""
		}
	}
	res, err := sess.Identify.Boxes.ResolveCatalogRef(ctx, box, catalogID, false)
	if err != nil || res == nil {
		return ""
	}
	return res.Tags
}
