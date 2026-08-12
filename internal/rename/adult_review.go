package rename

// Claude 2026-08-12: Phase D1+D2 — Adult Review: preview and local confirm
// Reason: deep-interview-adult-rename-review-alts — operators can name and track
//   a web-identified-only Adult scene without a catalog scene ID. The Review
//   popup gets its preview from BuildAdultReview (GET) and commits via
//   ConfirmAdultReviewLocal (POST local branch). The catalog branch re-uses the
//   ordinary ApplyLibraryAdult after RepickAdultScene flips the proposal to
//   Pending — that wiring lives in internal/api/adult_review.go.
// Troubleshooting: web-identified-only rows were stuck as Unmatched forever;
//   Review provides the operator escape hatch.
// Review if: the local identity scheme changes away from phash-backed keys.

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
)

// AdultReviewPreview is the response body for GET …/review: the current
// source basename, the proposed canonical name, the phash (so the frontend
// can warn when it is absent), the result of a fresh DB recheck, and any
// soft errors accumulated along the way.
type AdultReviewPreview struct {
	ProposedName string
	Studio       string
	Title        string
	Date         string
	PHash        string
	// CatalogBox/CatalogSceneID are non-empty only when the recheck found a
	// confident catalog match. When both are present the Confirm modal must
	// clearly tell the operator that FileName will be ignored and the catalog
	// identity will be used.
	CatalogBox     string
	CatalogSceneID string
	CatalogTitle   string
	CatalogStudio  string
	CatalogDate    string
	// RecheckError is a soft error — populated but never returned as err. The
	// preview is still usable; the modal shows this as a muted notice.
	RecheckError string
}

// adultReviewGuards enforces the four preconditions shared by BuildAdultReview
// and ConfirmAdultReviewLocal.
func adultReviewGuards(p proposals.Proposal) error {
	if p.Workflow != proposals.Rename {
		return fmt.Errorf("proposal %d is not a Rename proposal", p.ID)
	}
	if p.Mode != mode.Adult {
		return fmt.Errorf("proposal %d is not an Adult proposal", p.ID)
	}
	if p.Status != proposals.Unmatched {
		return fmt.Errorf("proposal %d is %q, not unmatched — Review applies only to unmatched rows", p.ID, p.Status)
	}
	if p.GiveBackSceneID != "" {
		return fmt.Errorf("proposal %d already has a catalog scene id — use the ordinary Apply instead of Review", p.ID)
	}
	return nil
}

// BuildAdultReview assembles the preview payload for the Review popup. It
// re-hashes the video on demand if p.PHash is empty, runs a fresh DB recheck,
// and returns a proposed canonical name. Any recheck failure is captured into
// RecheckError and swallowed so the local path remains available.
func BuildAdultReview(
	ctx context.Context, sess *mode.Session, libStore *library.Store,
	hasher PHasher, prober Prober, p proposals.Proposal,
) (AdultReviewPreview, error) {
	if err := adultReviewGuards(p); err != nil {
		return AdultReviewPreview{}, err
	}
	videoPath, err := library.ResolveVideoFile(p.SourcePath)
	if err != nil {
		return AdultReviewPreview{}, fmt.Errorf("resolving source file under %q: %w", p.SourcePath, err)
	}

	preview := AdultReviewPreview{
		Studio: p.Studio,
		Title:  p.Title,
		Date:   p.Date,
		PHash:  p.PHash,
	}

	// Re-hash on demand: a row scanned before the hasher was available, or one
	// whose phash was not cached, needs a fresh hash for the preview name and
	// the identity key.
	if preview.PHash == "" && hasher != nil {
		if h, herr := hasher.Hash(ctx, videoPath); herr == nil {
			preview.PHash = h
		} else {
			// Soft failure — preview is still usable; Confirm will fail.
			preview.RecheckError = fmt.Sprintf("could not compute phash: %v", herr)
		}
	}

	// DB recheck: fingerprint lookup only — NOT the full Identify (AI/text)
	// pipeline. Review GET must return quickly so the modal is usable; scan-time
	// identifyAdultFiles already ran Identify when the row was created. A
	// synchronous AI/web-search replay here left web-identified rows stuck on
	// "Loading preview…" for minutes. Any lookup error is captured into
	// RecheckError; the local confirm path must still work without a catalog hit.
	if sess != nil && sess.Identify != nil && preview.PHash != "" {
		matches, lerr := sess.Identify.LookupFingerprints(ctx, []string{preview.PHash})
		if lerr != nil {
			preview.RecheckError = appendRecheckError(preview.RecheckError,
				fmt.Sprintf("fingerprint lookup: %v", lerr))
		} else if m, hit := matches[preview.PHash]; hit && m != nil && m.Box != "" && m.SceneID != "" {
			preview.CatalogBox = m.Box
			preview.CatalogSceneID = m.SceneID
			preview.CatalogTitle = m.Title
			preview.CatalogStudio = m.Studio
			preview.CatalogDate = m.Date
		}
	}

	// Use catalog values when present; fall back to proposal's web-identified fields.
	studio := preview.Studio
	title := preview.Title
	date := preview.Date
	if preview.CatalogBox != "" {
		studio = preview.CatalogStudio
		title = preview.CatalogTitle
		date = preview.CatalogDate
	}
	preview.ProposedName = naming.AdultFileName(studio, title, date, preview.PHash, filepath.Ext(videoPath))
	return preview, nil
}

// ConfirmAdultReviewLocal renames the video to the operator-supplied fileName
// and records it in the library under a local phash-backed identity
// ("local" / "phash:<hash>"). It is the POST review-confirm local branch; the
// catalog branch lives in the API handler (RepickAdultScene → ApplyLibraryAdult).
//
// Extension policy: a missing extension is filled from the source; a changed
// extension is rejected (renaming .mkv to .mp4 does not transcode and lies
// about the container).
//
// Duplicate-local guard: a second file whose phash collides with an existing
// local row is routed into applyAdultAlternate rather than letting
// UpsertScene's ON CONFLICT silently re-point file_path and orphan the first.
// This is the single most dangerous case if written naively — see §2.4 of the
// plan and the comment in the code below.
func ConfirmAdultReviewLocal(
	ctx context.Context, libStore *library.Store,
	hasher PHasher, prober Prober,
	p proposals.Proposal, fileName, tier string,
) (sceneID int64, changes []mode.PathChange, err error) {
	if err := adultReviewGuards(p); err != nil {
		return 0, nil, err
	}
	videoPath, err := library.ResolveVideoFile(p.SourcePath)
	if err != nil {
		return 0, nil, fmt.Errorf("resolving source file under %q: %w", p.SourcePath, err)
	}

	// Phash is required for the local identity key. Re-hash if absent; hard
	// fail on error — a local identity without a phash is not an identity.
	// Claude 2026-08-12: write recomputed phash back onto p before alternate fold.
	// Reason: applyAdultAlternate reads p.PHash for AdultFileName / file rows /
	//   UpdateScenePrimaryPath; leaving p.PHash empty after a local recompute
	//   named files without [phash-…] and wiped library_scenes.phash.
	// Troubleshooting: Review confirm of a second same-phash file produced an
	//   alternate without a phash tag and left Dedup's cache key empty.
	// Review if: applyAdultAlternate takes an explicit phash argument instead.
	phash := p.PHash
	if phash == "" && hasher != nil {
		if h, herr := hasher.Hash(ctx, videoPath); herr == nil {
			phash = h
		}
	}
	if phash == "" {
		return 0, nil, fmt.Errorf("cannot create a local identity for %q: no perceptual hash could be computed", videoPath)
	}
	p.PHash = phash

	// Sanitise the operator-supplied fileName: strip directory components first
	// (guards against "../../etc/x.mp4"), then neutralise remaining separators.
	name := naming.SafePathComponent(filepath.Base(fileName))
	if name == "" {
		return 0, nil, fmt.Errorf("file name %q is empty after sanitisation", fileName)
	}
	srcExt := filepath.Ext(videoPath)
	if filepath.Ext(name) == "" {
		name += srcExt
	} else if filepath.Ext(name) != srcExt {
		return 0, nil, fmt.Errorf("extension mismatch: source is %q but fileName has %q — renaming does not transcode", srcExt, filepath.Ext(name))
	}

	// Duplicate-local guard BEFORE the move: if another local row already holds
	// this phash with a live different file, route into the alternate fold. Do
	// this before step 5's move so applyAdultAlternate can compute its own destination.
	// Claude 2026-08-12: require fileExists — stale deleted paths are not occupied slots.
	// Reason: mirrors ApplyLibraryAdult's fold gate (rename_adult_library.go); without
	//   it a missing primary makes promote fail or lose/tie leave a dead primary path.
	// Troubleshooting: Review confirm 502'd after an external delete of the first local file.
	// Review if: ConfirmAdultReviewLocal stops routing collisions into applyAdultAlternate.
	existingLocal, gerr := libStore.GetScene(ctx, library.LocalSceneBox, library.LocalSceneID(phash))
	if gerr == nil && existingLocal != nil &&
		existingLocal.FilePath != "" && existingLocal.FilePath != videoPath &&
		fileExists(existingLocal.FilePath) {
		// A second file with the same phash is a genuine duplicate. Route into
		// the alternate fold instead of letting UpsertScene's ON CONFLICT
		// re-point file_path and orphan the first file.
		altSceneID, altChanges, promoted, altErr := applyAdultAlternate(
			ctx, libStore, existingLocal, p, videoPath, tier, prober)
		if altErr != nil {
			return altSceneID, altChanges, altErr
		}
		recordUndoArchive(ctx, mode.Adult, p, videoPath,
			sceneTouchedRows(existingLocal, altSceneID),
			lastCreatedPath(altChanges, videoPath), library.FileSize(lastCreatedPath(altChanges, videoPath)),
			promoted, false)
		return altSceneID, altChanges, nil
	}

	dest := filepath.Join(p.RootFolderPath, name)
	movedDest, moveChanges, moveErr := moveUnique(videoPath, dest)
	if moveErr != nil {
		return 0, nil, fmt.Errorf("renaming %q to %q: %w", videoPath, dest, moveErr)
	}
	changes = append(changes, moveChanges...)

	var fileSize int64
	var fileMTime string
	if info, statErr := os.Stat(movedDest); statErr == nil {
		fileSize = info.Size()
		fileMTime = info.ModTime().UTC().Format(time.RFC3339Nano)
	}

	scene, uErr := libStore.UpsertScene(ctx, library.Scene{
		Box:            library.LocalSceneBox,
		SceneID:        library.LocalSceneID(phash),
		Title:          p.Title,
		Studio:         p.Studio,
		Date:           p.Date,
		FilePath:       movedDest,
		RootFolderPath: p.RootFolderPath,
		Size:           fileSize,
		QualityTier:    tier,
		PHash:          phash,
		PHashFileSize:  fileSize,
		PHashFileMTime: fileMTime,
	})
	if uErr != nil {
		return 0, changes, fmt.Errorf("recording reviewed scene in library: %w", uErr)
	}

	// viaAlternateFold=false: this is a simple new-scene commit; undo should
	// move the file back and delete the local row. giveBackSubmitted=false:
	// Review deliberately never fires an outbound submission (spec constraint:
	// "Do not restore Give back as the Review path").
	recordUndoArchive(ctx, mode.Adult, p, videoPath,
		sceneTouchedRows(existingLocal, scene.ID), movedDest, fileSize, false, false)
	return scene.ID, changes, nil
}

// appendRecheckError joins two soft-error strings. Both are surfaced as
// RecheckError in the preview (non-blocking); only one tends to be populated
// in practice.
func appendRecheckError(existing, next string) string {
	if existing == "" {
		return next
	}
	return existing + "; " + next
}
