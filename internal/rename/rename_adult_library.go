package rename

import (
	"context"
	"errors"
	"fmt"
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
func ScanLibraryAdult(ctx context.Context, sess *mode.Session, libStore *library.Store, hasher PHasher, prober Prober, rootFolderPath string) ([]proposals.Proposal, error) {
	if sess.Identify == nil {
		return nil, fmt.Errorf("adult identification isn't configured — add a connection for your chosen AI provider and set the AI model in Settings, plus at least one of StashDB/FansDB/TPDB")
	}
	if rootFolderPath == "" {
		return nil, fmt.Errorf("no Adult library root folder configured yet — add one in Settings first")
	}

	scenes, err := libStore.ListScenes(ctx)
	if err != nil {
		return nil, fmt.Errorf("loading library scenes: %w", err)
	}
	known := make(map[string]bool, len(scenes))
	for _, sc := range scenes {
		// Marking just the file path is enough — ScanRootFolder's recursive
		// walk decides atomicity dynamically from known, same as Movies/Series.
		known[sc.FilePath] = true
	}

	entries, err := library.ScanRootFolder(rootFolderPath, known)
	if err != nil {
		return nil, fmt.Errorf("scanning %s: %w", rootFolderPath, err)
	}

	// First pass: resolve each entry to its video file, dropping sidecars,
	// surfacing an unresolvable entry as Unmatched (never silently), and
	// skipping anything already named to SAK's Adult scheme. Only the survivors
	// go through the (batched) identification cascade.
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
			out = append(out, proposals.Proposal{
				Mode: mode.Adult, Workflow: proposals.Rename, Status: proposals.Unmatched,
				SourceName: entry.Name, SourcePath: entry.Path, RootFolderPath: rootFolderPath,
				Reason: fmt.Sprintf("no video file found under %q: %v", entry.Path, err),
			})
			continue
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
		out = append(out, buildAdultLibraryProposal(ctx, libStore, rootFolderPath, c.entry, c.videoPath, ids[i]))
	}
	return out, nil
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
	if status == proposals.Pending {
		if existing, err := libStore.GetScene(ctx, id.match.Box, id.match.SceneID); err == nil {
			p.Status = proposals.Unmatched
			p.Reason = fmt.Sprintf("already tracked in the library as %q — leaving in place for manual review", existing.Title)
			return p
		} else if !errors.Is(err, library.ErrNotFound) {
			p.Status = proposals.Unmatched
			p.Reason = fmt.Sprintf("checking whether %q is already tracked: %v", title, err)
			return p
		}
	}

	p.Status, p.Reason, p.Title, p.ForeignID, p.ItemType = status, reason, title, foreignID, itemType
	if id.match != nil {
		// Captured regardless of match outcome: an Unmatched (web-identified-
		// only) proposal still needs Studio/Date for SubmitDraft's give-back.
		p.Studio, p.Date = id.match.Studio, id.match.Date
		// GiveBackBox/GiveBackSceneID are the raw Box/SceneID, kept separate
		// from ForeignID for the same reason the library key is (above).
		p.GiveBackBox, p.GiveBackSceneID = id.match.Box, id.match.SceneID
	}
	if id.hashed {
		p.PHash = id.phash
		p.DurationSeconds = id.duration
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
func ApplyLibraryAdult(ctx context.Context, sess *mode.Session, libStore *library.Store, p proposals.Proposal) (sceneID int64, fingerprintSubmitted bool, changes []mode.PathChange, err error) {
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
		PHash: p.PHash, PHashFileSize: fileSize, PHashFileMTime: fileMTime,
	})
	if err != nil {
		return 0, false, changes, fmt.Errorf("recording scene %q in the library: %w", p.Title, err)
	}

	return scene.ID, submitFingerprintGiveBack(ctx, sess, p), changes, nil
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
	if err := os.Rename(sourcePath, unique); err != nil {
		return "", fmt.Errorf("moving %q to %q: %w", sourcePath, unique, err)
	}
	return unique, nil
}
