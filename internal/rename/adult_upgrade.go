package rename

// Claude 2026-08-12: Phase D4 — auto-upgrade local Adult scenes to catalog identity
// Reason: deep-interview-adult-rename-review-alts — when UpgradeLocalAdultScenes
//   runs before ScanLibraryAdult it silently upgrades any local (box="local")
//   scenes whose phash now resolves to a catalog identity: rename the file(s) to
//   the catalog AdultFileName/AdultAlternateFileName and rewrite the DB row in
//   place (keyed on id, not box/scene_id, so tags, file rows, and undo references
//   all survive). No proposal is created — spec constraint: "auto-upgrade, no Rename
//   queue row." KindScan is logged (not KindApply) because there is no proposal id.
// Troubleshooting: a Reviewed local scene did not automatically rename when the
//   catalog later had a fingerprint hit; UpgradeLocalAdultScenes must run before
//   ScanLibraryAdult at every scan site.
// Review if: local identity scheme changes away from phash:, or the upgrade
//   should merge catalog rows rather than skip them.

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/naming"
	"github.com/labbersanon/sakms/internal/organizeevents"
	"github.com/labbersanon/sakms/internal/quality"
)

// UpgradeLocalAdultScenes looks up every local (box="local") Adult scene's phash
// against the catalog. For each hit, it renames the primary and alternate files
// to their catalog-based names and rewrites the DB identity in place (same
// library_scenes.id, new box/scene_id/title/studio/date). Soft-fail throughout:
// errors are logged and swallowed so the caller's Scan still runs.
//
// Deliberately NOT folded into ScanLibraryAdult: it produces file moves rather
// than proposals, forcing a second return value onto ScanLibraryAdult and every
// call site. Adding three call sites is strictly less invasive than changing a
// signature at three call sites.
//
// Collision guard: if a catalog row already exists for the matched (box, scene_id),
// the local scene is skipped and logged — a blind merge of two rows with
// independent file sets, tags, and undo references is not safe. Dedup is the
// right tool for cross-row merging (plan §8 R10).
func UpgradeLocalAdultScenes(
	ctx context.Context, sess *mode.Session, libStore *library.Store,
	prober Prober,
) (changes []mode.PathChange, err error) {
	if sess == nil || sess.Identify == nil {
		return nil, nil // no identifier configured; skip silently
	}

	scenes, lerr := libStore.ListScenes(ctx)
	if lerr != nil {
		return nil, fmt.Errorf("upgrade: listing scenes: %w", lerr)
	}

	type localScene struct {
		scene library.Scene
		phash string
	}
	var locals []localScene
	for _, sc := range scenes {
		if !library.IsLocalScene(sc.Box) {
			continue
		}
		phash, ok := library.LocalScenePHash(sc.SceneID)
		if !ok || phash == "" {
			continue
		}
		locals = append(locals, localScene{scene: sc, phash: phash})
	}
	if len(locals) == 0 {
		return nil, nil // fast path — nothing to upgrade
	}

	phashes := make([]string, len(locals))
	for i, ls := range locals {
		phashes[i] = ls.phash
	}
	matches, lerr := sess.Identify.LookupFingerprints(ctx, phashes)
	if lerr != nil {
		log.Printf("rename: upgrade local adult scenes: fingerprint lookup failed: %v", lerr)
		return nil, nil // soft-fail: caller's Scan still runs
	}

	for _, ls := range locals {
		match, hit := matches[ls.phash]
		if !hit || match == nil || match.Box == "" || match.SceneID == "" {
			continue
		}

		// Collision guard: if the catalog identity is already tracked, skip.
		if existing, gerr := libStore.GetScene(ctx, match.Box, match.SceneID); gerr == nil && existing != nil {
			log.Printf("rename: upgrade local adult scene %q: catalog %s/%s already tracked as %q — skipping (use Dedup to merge)",
				ls.scene.Title, match.Box, match.SceneID, existing.Title)
			continue
		}

		newChanges, upgradeErr := upgradeOneLocalScene(ctx, libStore, ls.scene, ls.phash, match.Box, match.SceneID, match.Title, match.Studio, match.Date, prober)
		if upgradeErr != nil {
			log.Printf("rename: upgrade local adult scene %q: %v", ls.scene.Title, upgradeErr)
			continue
		}
		changes = append(changes, newChanges...)
	}
	return changes, nil
}

// upgradeOneLocalScene does the rename-then-DB-write for one local scene.
// Rename-first ordering: if the file rename succeeds but the DB write fails,
// the file sits at the catalog name while the row still points at the old path.
// The next Scan finds it as an orphan and re-proposes it, which is recoverable.
// The reverse ordering would leave a permanently mis-named tracked row.
func upgradeOneLocalScene(
	ctx context.Context, libStore *library.Store,
	sc library.Scene, phash, box, sceneID, title, studio, date string,
	prober Prober,
) ([]mode.PathChange, error) {
	if sc.FilePath == "" {
		return nil, fmt.Errorf("scene %d has no file_path to rename", sc.ID)
	}

	primaryDest := filepath.Join(sc.RootFolderPath, naming.AdultFileName(
		studio, title, date, phash, filepath.Ext(sc.FilePath)))
	movedPrimary, ch, moveErr := moveUnique(sc.FilePath, primaryDest)
	if moveErr != nil {
		return nil, fmt.Errorf("renaming primary %q: %w", sc.FilePath, moveErr)
	}
	changes := ch

	// Rename non-primary files (alternates accumulated before the upgrade).
	files, fileErr := libStore.ListSceneFiles(ctx, sc.ID)
	if fileErr != nil {
		log.Printf("rename: upgrade scene %d: listing files: %v", sc.ID, fileErr)
	}
	for _, f := range files {
		if f.IsPrimary || f.FilePath == "" {
			continue
		}
		altFilePHash, _ := library.LocalScenePHash(sc.SceneID)
		if f.PHash != "" {
			altFilePHash = f.PHash
		}
		// Probe quality labels for the alternate name; fail-open with empty labels.
		altMeta := probeFileMeta(ctx, prober, f.FilePath, f.QualityTier)
		altName := naming.AdultAlternateFileName(
			studio, title, date, altFilePHash,
			quality.ResolutionLabel(altMeta.Height),
			quality.CodecLabel(altMeta.Codec),
			quality.BitrateLabel(altMeta.BitRate),
			filepath.Ext(f.FilePath),
		)
		movedAlt, altCh, altMoveErr := moveUnique(f.FilePath, filepath.Join(sc.RootFolderPath, altName))
		if altMoveErr != nil {
			log.Printf("rename: upgrade scene %d: renaming alternate %q: %v", sc.ID, f.FilePath, altMoveErr)
			continue
		}
		changes = append(changes, altCh...)
		f.FilePath = movedAlt
		if _, uErr := libStore.UpsertSceneFile(ctx, f); uErr != nil {
			log.Printf("rename: upgrade scene %d: updating file row for %q: %v", sc.ID, movedAlt, uErr)
		}
	}

	// Rewrite the DB identity, keyed on sc.ID so tags, file rows, and undo
	// references all survive (see D5 / UpgradeSceneIdentity doc comment).
	if iErr := libStore.UpgradeSceneIdentity(ctx, sc.ID, box, sceneID, title, studio, date); iErr != nil {
		return changes, fmt.Errorf("upgrading identity for scene %d: %w", sc.ID, iErr)
	}

	// Update the primary path and phash triple.
	var phashSize int64
	var phashMTime string
	if info, statErr := os.Stat(movedPrimary); statErr == nil {
		phashSize = info.Size()
		phashMTime = info.ModTime().UTC().Format(time.RFC3339Nano)
	}
	if pErr := libStore.UpdateScenePrimaryPath(ctx, sc.ID, movedPrimary, sc.QualityTier,
		library.FileSize(movedPrimary), phash, phashSize, phashMTime); pErr != nil {
		return changes, fmt.Errorf("updating primary path for scene %d: %w", sc.ID, pErr)
	}

	organizeevents.Log(ctx, organizeevents.Event{
		Workflow: "rename",
		Mode:     string(mode.Adult),
		Kind:     organizeevents.KindScan,
		Message:  fmt.Sprintf("upgraded local scene %q to %s/%s and renamed to %q", sc.Title, box, sceneID, filepath.Base(movedPrimary)),
	})
	return changes, nil
}
