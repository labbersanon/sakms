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

// ScanLibraryAdult is Purge's Adult-library counterpart to ScanLibrary — used
// once Adult stops requiring Whisparr (see the plan this was built from,
// Stage 2). A scene is a flat one-file-done-once thing like a Movie Item, so
// tags live at the scene level and Purge proposes one row per matched SCENE,
// the direct analogue of ScanLibrary's Movies path. Matches each scene's own
// local tags against allowlist with the exact same MatchedEntries rule.
//
// SourcePath is set to the scene's on-disk file: ApplyLibraryAdult trusts it
// for the file removal (there is no GetScene-by-id to re-fetch through, unlike
// Movies' ApplyLibrary), so it is load-bearing, not merely informational.
func ScanLibraryAdult(ctx context.Context, libStore *library.Store, allowlist []string) ([]proposals.Proposal, error) {
	scenes, err := libStore.ListScenes(ctx)
	if err != nil {
		return nil, fmt.Errorf("loading scenes: %w", err)
	}

	var out []proposals.Proposal
	for _, sc := range scenes {
		tags, err := libStore.SceneTags(ctx, sc.ID)
		if err != nil {
			return nil, fmt.Errorf("loading tags for %q: %w", sc.Title, err)
		}
		var matched []string
		for _, tag := range tags {
			matched = append(matched, MatchedEntries(tag, allowlist)...)
		}
		if len(matched) == 0 {
			continue
		}
		out = append(out, proposals.Proposal{
			Mode: mode.Adult, Workflow: proposals.Purge, Status: proposals.Pending,
			SourceName: sc.Title, SourcePath: sc.FilePath, RootFolderPath: sc.RootFolderPath,
			Title: sc.Title, Studio: sc.Studio, Date: sc.Date, TrackedID: int(sc.ID),
			Reason: fmt.Sprintf("matched allowlist tag(s): %s", strings.Join(matched, ", ")),
		})
	}
	return out, nil
}

// ApplyLibraryAdult is Purge's Adult-library counterpart to ApplyLibrary —
// removes the scene's file directly (no *arr app to ask) and deletes its
// record from libStore. p must be Pending and carry a TrackedID from
// ScanLibraryAdult (the scene's own id, following the same field convention
// the other purge paths established).
//
// Unlike ApplyLibrary (Movies), the file path is taken from p.SourcePath
// rather than re-fetched from the store: a scene has no GetScene-by-id lookup,
// only GetScene(box, sceneID), so the path captured at scan time is what
// Apply acts on.
//
// changes is a named return so a post-delete failure (libStore.DeleteScene)
// still reports the committed removal to the caller for
// Session.NotifyPlayers. p.SourcePath can legitimately be "" (a tracked scene
// with no file) — the Deleted PathChange is only ever appended inside the
// non-empty guard, and an already-gone file (os.IsNotExist) is not an error.
func ApplyLibraryAdult(ctx context.Context, libStore *library.Store, p proposals.Proposal) (changes []mode.PathChange, err error) {
	if p.Status != proposals.Pending {
		return nil, fmt.Errorf("proposal %d is %q, not pending — nothing to apply", p.ID, p.Status)
	}
	if p.TrackedID == 0 {
		return nil, fmt.Errorf("proposal %d has no scene id to delete", p.ID)
	}

	if p.SourcePath != "" {
		if err := os.Remove(p.SourcePath); err != nil && !os.IsNotExist(err) {
			return nil, fmt.Errorf("deleting %q: %w", p.SourcePath, err)
		}
		changes = append(changes, mode.PathChange{Path: p.SourcePath, Kind: mode.Deleted})
	}
	if err := libStore.DeleteScene(ctx, int64(p.TrackedID)); err != nil {
		return changes, err
	}
	return changes, nil
}
