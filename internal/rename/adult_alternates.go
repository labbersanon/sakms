package rename

// Claude 2026-08-12: Adult alternate-version fold (Scan + Apply sides)
// Reason: deep-interview-adult-rename-review-alts — Adult gains Movies/Series parity for
//   duplicate files: a second copy of an already-tracked scene becomes a Pending proposal
//   (Scan) and is folded as primary-or-alternate by quality (Apply), never overwriting the
//   first. Mirrors acceptDuplicatePending / applyLibraryAlternate.
// Troubleshooting: a second copy of a tracked scene used to surface as Unmatched with
//   "leaving in place for manual review" — now it surfaces as Pending with an alternate: reason.
// Review if: equal-tier policy changes from keep-existing-primary.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/naming"
	"github.com/labbersanon/sakms/internal/proposals"
	"github.com/labbersanon/sakms/internal/quality"
)

// acceptDuplicatePendingScene fills a Proposal for an Adult file whose (box,
// scene_id) is already tracked in the library. It sets Status=Pending and
// copies the identity fields from the tracked row.
//
// Follows the Movies shape (acceptDuplicatePending, rename_alternates.go:198),
// NOT the Series shape (acceptDuplicatePendingEpisode, series_alternates.go:63).
// Series' helper deliberately does NOT overwrite Title/Studio/Date because its
// seven call sites each already have a correct, independently-pinned show.
// Adult has exactly ONE call site and the library row is the authoritative
// identity — copying from existing is both safe and required, because
// applyAdultAlternate reads p.Studio/Title/Date to build both filenames. If we
// did not copy them here, a web-identified orphan whose catalog fields differed
// from the tracked row would produce a wrong alternate name.
func acceptDuplicatePendingScene(p *proposals.Proposal, existing *library.Scene) {
	p.Status = proposals.Pending
	p.Title = existing.Title
	p.Studio = existing.Studio
	p.Date = existing.Date
	p.GiveBackBox = existing.Box
	p.GiveBackSceneID = existing.SceneID
	p.Reason = fmt.Sprintf(
		"alternate: already in library as %q — apply will fold as primary or alternate by quality",
		existing.Title)
}

// applyAdultAlternate folds the incoming orphan file into an already-tracked
// Adult scene. Equal probed tier keeps the existing primary (orphan becomes the
// alternate). promoted reports which branch ran, exactly as applyLibraryAlternate
// does — the caller threads it into viaAlternateFold so undo can distinguish the
// two branches (see undo_apply.go:190-193).
//
// Adult is flat — there is no wrapping folder. The destination directory is:
//
//	root := existing.RootFolderPath; if root == "" { root = p.RootFolderPath }
//
// Prefer existing.RootFolderPath (the OPPOSITE of Movies). For Movies, the
// "folder" is a per-title subdirectory computed from p.RootFolderPath, so using
// p is safe. For Adult the "folder" IS the root, and the semantic of an alternate
// is "beside its primary". Using p.RootFolderPath when the scan root differs from
// the tracked scene's root would scatter the alternate away from the file it
// belongs to.
//
// Reuses probeFileMeta, moveUnique, and fileMeta from rename_alternates.go
// unchanged — same package, deliberately not duplicated.
func applyAdultAlternate(
	ctx context.Context, libStore *library.Store, existing *library.Scene,
	p proposals.Proposal, videoPath, settingsTier string, prober Prober,
) (sceneID int64, changes []mode.PathChange, promoted bool, err error) {
	root := existing.RootFolderPath
	if root == "" {
		root = p.RootFolderPath
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return 0, nil, false, fmt.Errorf("creating %q: %w", root, err)
	}

	orphanMeta := probeFileMeta(ctx, prober, videoPath, settingsTier)
	primaryPath := existing.FilePath
	if primaryPath == "" {
		files, listErr := libStore.ListSceneFiles(ctx, existing.ID)
		if listErr == nil {
			for _, f := range files {
				if f.IsPrimary {
					primaryPath = f.FilePath
					break
				}
			}
		}
	}
	primaryMeta := probeFileMeta(ctx, prober, primaryPath, existing.QualityTier)
	if primaryMeta.Tier == "" {
		primaryMeta.Tier = existing.QualityTier
	}

	orphanWins := quality.RankString(orphanMeta.Tier) > quality.RankString(primaryMeta.Tier)
	promoted = orphanWins

	if orphanWins {
		// Demote the incumbent: give it the alternate name with ITS OWN quality labels.
		altName := naming.AdultAlternateFileName(
			p.Studio, p.Title, p.Date, existing.PHash,
			quality.ResolutionLabel(primaryMeta.Height),
			quality.CodecLabel(primaryMeta.Codec),
			quality.BitrateLabel(primaryMeta.BitRate),
			filepath.Ext(primaryPath),
		)
		movedPrimary, ch, moveErr := moveUnique(primaryPath, filepath.Join(root, altName))
		if moveErr != nil {
			return 0, changes, promoted, fmt.Errorf("demoting previous primary %q: %w", primaryPath, moveErr)
		}
		changes = append(changes, ch...)

		// Promote the orphan: give it the canonical primary name.
		primaryDest := filepath.Join(root, naming.AdultFileName(p.Studio, p.Title, p.Date, p.PHash, filepath.Ext(videoPath)))
		movedOrphan, ch, moveErr := moveUnique(videoPath, primaryDest)
		if moveErr != nil {
			return 0, changes, promoted, fmt.Errorf("promoting orphan to primary %q: %w", primaryDest, moveErr)
		}
		changes = append(changes, ch...)

		// Rewrite library_scenes to point at the new primary and update its phash
		// triple. This is a CORRECTNESS requirement: Adult's filename embeds the
		// phash, and Dedup validates its cache against phash_file_size/mtime. Without
		// this rewrite, Dedup would trust a cached hash against a file that is now an
		// alternate.
		var phashSize int64
		var phashMTime string
		if info, statErr := os.Stat(movedOrphan); statErr == nil {
			phashSize = info.Size()
			phashMTime = info.ModTime().UTC().Format(time.RFC3339Nano)
		}
		if err := libStore.UpdateScenePrimaryPath(ctx, existing.ID, movedOrphan, orphanMeta.Tier,
			library.FileSize(movedOrphan), p.PHash, phashSize, phashMTime); err != nil {
			return 0, changes, promoted, err
		}
		// Delete the stale row for the original primary path. For Adult the
		// phash is in the filename, so the new primary (movedOrphan, newhash)
		// lands at a DIFFERENT path than the old primary (primaryPath, oldhash).
		// The old row cannot be updated in-place by the UpsertSceneFile below
		// (which targets movedOrphan), so it becomes a stale non-primary row.
		// Delete it first so only the two meaningful rows survive.
		// (For Movies this is a no-op: the orphan lands at the same canonical
		// path as the old primary, so UpsertSceneFile updates that row in-place.)
		if primaryPath != "" && primaryPath != movedOrphan {
			if err := libStore.DeleteSceneFileByPath(ctx, primaryPath); err != nil {
				return 0, changes, promoted, fmt.Errorf("cleaning stale primary file row for %q: %w", primaryPath, err)
			}
		}
		// Primary first: the ordering constraint mirrors series_alternates.go:238-239.
		if _, err := libStore.UpsertSceneFile(ctx, library.SceneFile{
			SceneID: existing.ID, FilePath: movedOrphan, IsPrimary: true,
			QualityTier: orphanMeta.Tier, Size: library.FileSize(movedOrphan),
			Width: orphanMeta.Width, Height: orphanMeta.Height, VideoCodec: orphanMeta.Codec,
			BitRate: orphanMeta.BitRate, DurationSec: orphanMeta.Duration,
			PHash: p.PHash,
		}); err != nil {
			return 0, changes, promoted, err
		}
		if _, err := libStore.UpsertSceneFile(ctx, library.SceneFile{
			SceneID: existing.ID, FilePath: movedPrimary, IsPrimary: false,
			QualityTier: primaryMeta.Tier, Size: library.FileSize(movedPrimary),
			Width: primaryMeta.Width, Height: primaryMeta.Height, VideoCodec: primaryMeta.Codec,
			BitRate: primaryMeta.BitRate, DurationSec: primaryMeta.Duration,
			PHash: existing.PHash,
		}); err != nil {
			return 0, changes, promoted, err
		}
		return existing.ID, changes, promoted, nil
	}

	// Orphan loses or ties — keep existing primary; place orphan as alternate.
	altName := naming.AdultAlternateFileName(
		p.Studio, p.Title, p.Date, p.PHash,
		quality.ResolutionLabel(orphanMeta.Height),
		quality.CodecLabel(orphanMeta.Codec),
		quality.BitrateLabel(orphanMeta.BitRate),
		filepath.Ext(videoPath),
	)
	movedOrphan, ch, moveErr := moveUnique(videoPath, filepath.Join(root, altName))
	if moveErr != nil {
		return 0, nil, promoted, fmt.Errorf("placing alternate %q: %w", altName, moveErr)
	}
	changes = append(changes, ch...)

	// Refresh the primary's file row if we have a path (records probed labels).
	if primaryPath != "" {
		if _, err := libStore.UpsertSceneFile(ctx, library.SceneFile{
			SceneID: existing.ID, FilePath: primaryPath, IsPrimary: true,
			QualityTier: primaryMeta.Tier, Size: library.FileSize(primaryPath),
			Width: primaryMeta.Width, Height: primaryMeta.Height, VideoCodec: primaryMeta.Codec,
			BitRate: primaryMeta.BitRate, DurationSec: primaryMeta.Duration,
			PHash: existing.PHash,
		}); err != nil {
			return 0, changes, promoted, err
		}
	}
	if _, err := libStore.UpsertSceneFile(ctx, library.SceneFile{
		SceneID: existing.ID, FilePath: movedOrphan, IsPrimary: false,
		QualityTier: orphanMeta.Tier, Size: library.FileSize(movedOrphan),
		Width: orphanMeta.Width, Height: orphanMeta.Height, VideoCodec: orphanMeta.Codec,
		BitRate: orphanMeta.BitRate, DurationSec: orphanMeta.Duration,
		PHash: p.PHash,
	}); err != nil {
		return 0, changes, promoted, err
	}
	return existing.ID, changes, promoted, nil
}
