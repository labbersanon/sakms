package dedup

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/curtiswtaylorjr/sakms/internal/config"
	"github.com/curtiswtaylorjr/sakms/internal/identify"
	"github.com/curtiswtaylorjr/sakms/internal/library"
	"github.com/curtiswtaylorjr/sakms/internal/mode"
	"github.com/curtiswtaylorjr/sakms/internal/phash"
	"github.com/curtiswtaylorjr/sakms/internal/proposals"
)

// sceneDedupKey groups Adult duplicates by a scene's stable stash-box
// identity — the direct analogue of Series' episodeDedupKey, with (box,
// scene_id) as SEPARATE fields for the same reason library.Scene stores
// them separately (a StashDB and a FansDB match both yield raw UUIDs in the
// same shape, and give-back needs to know which box a scene came from; see
// internal/library/library_scene.go). "The tracked copy" for a key is just
// the one library.Scene row for that exact (box, scene_id) — the schema's
// own UNIQUE(box, scene_id) constraint rules out there ever being more than
// one, so a duplicate only arises when the SAME scene is also found sitting
// at another file path on disk (an untracked copy ScanRootFolder discovers
// that re-identifies to an already-tracked key).
type sceneDedupKey struct {
	box, sceneID string
}

// attachPHashesScene is attachPHashes' Adult-library sibling — identical body,
// differing only in the tracked type (*library.Scene) and the write-back
// method (UpdateScenePHash on library_scenes). Kept as a parallel sibling
// rather than a shared generic helper (CLAUDE.md's "prefer parallel sibling
// functions" convention), so the Movies (attachPHashes) and Series
// (attachPHashesSeries) paths stay untouched. Unlike the Servarr-backed
// attachPHashesAdult (which has no SAK-owned row to cache against and so
// recomputes every scan), this one reuses a tracked scene's cached library
// hash when the file's identity (size+mtime) AND the hash's scheme still
// match — the decode-once win — and writes a freshly computed tracked hash
// back via UpdateScenePHash.
func attachPHashesScene(ctx context.Context, hasher PHasher, libStore *library.Store, candidates []proposals.Candidate, tracked *library.Scene) []proposals.Candidate {
	out := make([]proposals.Candidate, 0, len(candidates))
	for _, c := range candidates {
		if c.TrackedID != 0 && tracked != nil && tracked.PHash != "" &&
			strings.HasPrefix(tracked.PHash, phash.Scheme+":") {
			if size, mtime, err := fileIdentity(c.Path); err == nil &&
				size == tracked.PHashFileSize && mtime == tracked.PHashFileMTime {
				c.PHash = tracked.PHash
				out = append(out, c)
				continue
			}
		}
		h, err := hasher.Hash(ctx, c.Path)
		if err != nil {
			continue // uncomputable — drop this candidate, same tolerance as probeCandidate
		}
		c.PHash = h
		if c.TrackedID != 0 {
			if size, mtime, statErr := fileIdentity(c.Path); statErr == nil {
				_ = libStore.UpdateScenePHash(ctx, int64(c.TrackedID), h, size, mtime)
			}
		}
		out = append(out, c)
	}
	return out
}

// ScanLibraryAdult is Dedup's Adult-library counterpart to ScanLibrary/
// ScanLibrarySeries — used once Adult owns its own library (library_scenes)
// instead of depending on Whisparr's tracked-item registry. It groups a
// tracked scene and any untracked copies of it by sceneDedupKey (box +
// scene_id), the direct analogue of Series' (show, season, episode)
// grouping. Orphans are identified with sess.Identify exactly as the
// Servarr-backed scanAdult does — the same phash-first-then-Identify cascade
// is Rename's concern; Dedup only needs the identity to group by.
//
// Both guards live inside this function (unlike scanAdult, whose Identify
// guard sits in Scan's dispatch), mirroring ScanLibrary's own two in-function
// guards now that Scan no longer dispatches here.
func ScanLibraryAdult(ctx context.Context, sess *mode.Session, libStore *library.Store, rootFolderPath string, prober Prober, hasher PHasher, perFrameThreshold int) ([]proposals.Proposal, error) {
	if sess.Identify == nil {
		return nil, fmt.Errorf("adult identification isn't configured — add an Ollama connection and set the Ollama model in Settings, plus at least one of StashDB/FansDB/TPDB")
	}
	if rootFolderPath == "" {
		return nil, fmt.Errorf("no Adult library root folder configured yet — add one in Settings first")
	}

	scenes, err := libStore.ListScenes(ctx)
	if err != nil {
		return nil, fmt.Errorf("loading scenes: %w", err)
	}

	trackedByKey := make(map[sceneDedupKey]library.Scene, len(scenes))
	known := make(map[string]bool, len(scenes))
	for _, sc := range scenes {
		if sc.FilePath != "" {
			// Marking just the file path is enough — ScanRootFolder's recursive
			// walk decides atomicity dynamically from known at whatever depth it
			// encounters a directory, so it doesn't need the wrapping folder
			// pre-marked too.
			known[sc.FilePath] = true
		}
		trackedByKey[sceneDedupKey{box: sc.Box, sceneID: sc.SceneID}] = sc
	}

	type orphanHit struct {
		name, path, title, studio, date, itemType string
	}
	orphansByKey := make(map[sceneDedupKey][]orphanHit)

	entries, err := library.ScanRootFolder(rootFolderPath, known)
	if err != nil {
		return nil, fmt.Errorf("scanning %s: %w", rootFolderPath, err)
	}
	for _, entry := range entries {
		if config.SidecarExts[strings.ToLower(filepath.Ext(entry.Name))] {
			continue
		}
		res, idErr := sess.Identify.Identify(ctx, entry.Name, filepath.Base(filepath.Dir(entry.Path)))
		box, sceneID, itemType, title, studio, date, ok := adultSceneIdentity(res, idErr)
		if !ok {
			continue // web-only / no scene id / identify error — Rename's concern, not Dedup's
		}
		key := sceneDedupKey{box: box, sceneID: sceneID}
		orphansByKey[key] = append(orphansByKey[key], orphanHit{name: entry.Name, path: entry.Path, title: title, studio: studio, date: date, itemType: itemType})
	}

	var out []proposals.Proposal
	for key, orphans := range orphansByKey {
		trackedScene, isTracked := trackedByKey[key]
		if !isTracked && len(orphans) < 2 {
			continue // a single new, untracked scene — nothing to dedup
		}

		title, studio, date := orphans[0].title, orphans[0].studio, orphans[0].date
		rootPath := ""
		var candidates []proposals.Candidate
		if isTracked {
			if c := probeCandidate(ctx, prober, "tracked", trackedScene.FilePath, int(trackedScene.ID)); c != nil {
				candidates = append(candidates, *c)
			}
			title, studio, date = trackedScene.Title, trackedScene.Studio, trackedScene.Date
			rootPath = trackedScene.RootFolderPath
		}
		for _, o := range orphans {
			if c := probeCandidate(ctx, prober, o.name, o.path, 0); c != nil {
				candidates = append(candidates, *c)
				if rootPath == "" {
					rootPath = filepath.Dir(o.path)
				}
			}
		}
		if len(candidates) < 2 {
			continue // couldn't probe enough of the group to compare
		}

		// Refine the same-(box, scene_id) group by perceptual similarity,
		// exactly as ScanLibrary/ScanLibrarySeries do: hash each candidate
		// (reusing a tracked scene's cached hash when its file is unchanged),
		// then drop any candidate outside the threshold of the group's
		// reference. A group refined below 2 survivors is not a duplicate — the
		// strictly-more-conservative keep-both behavior.
		var trackedPtr *library.Scene
		if isTracked {
			ts := trackedScene
			trackedPtr = &ts
		}
		candidates = attachPHashesScene(ctx, hasher, libStore, candidates, trackedPtr)
		candidates = refineByPHash(candidates, phash.Frames, perFrameThreshold)
		if len(candidates) < 2 {
			continue // perceptually dissimilar — keep both, no proposal
		}
		markWinner(candidates)

		out = append(out, proposals.Proposal{
			Mode: mode.Adult, Workflow: proposals.Dedup, Status: proposals.Pending,
			SourceName: title, Title: title, Studio: studio, Date: date,
			ItemType: orphans[0].itemType, RootFolderPath: rootPath,
			GiveBackBox: key.box, GiveBackSceneID: key.sceneID,
			Candidates: candidates,
			Reason:     fmt.Sprintf("%d copies identified as %q", len(candidates), title),
		})
	}
	return out, nil
}

// adultSceneIdentity maps an Identify result to the (box, scene_id) pair
// ScanLibraryAdult groups by, plus the scene's type/title/studio/date. ok is
// false for an identify error, a nil result, or a match with no valid
// stash-box identity (SceneID=="" || Box==""). That ok-condition is the exact
// same one identify.MatchResult.WhisparrForeignID uses to decide a match has a
// valid ForeignID, so the library-backed grouping key can never silently
// diverge from the Servarr-backed scanAdult's — it just keeps box and scene id
// as separate columns instead of collapsing them into one string.
func adultSceneIdentity(res *identify.MatchResult, err error) (box, sceneID, itemType, title, studio, date string, ok bool) {
	if err != nil || res == nil {
		return "", "", "", "", "", "", false
	}
	if res.SceneID == "" || res.Box == "" {
		return "", "", "", "", "", "", false
	}
	return res.Box, res.SceneID, res.Type, res.Title, res.Studio, res.Date, true
}

// ApplyLibraryAdult is Dedup's Adult-library counterpart to ApplyLibrary/
// ApplyLibrarySeries — resolves p against libStore instead of Whisparr: a
// tracked loser's file is removed and its library_scenes row deleted, an
// untracked orphan loser's file is removed directly, and an untracked winner
// (an orphan that beat the tracked copy) is recorded via libStore.UpsertScene
// so the duplicate group always resolves to exactly one tracked scene with a
// file behind it — never zero, the same invariant the Movies/Series siblings
// hold.
//
// Unlike ApplyLibrary (Movies), a tracked loser's authoritative path is not
// re-fetched by id (library exposes only GetScene(box, sceneID), not a
// get-by-id) — its file is removed at c.Path Series-style. changes still
// accumulates one Deleted PathChange per removed loser, captured at the point
// the physical deletion lands so a subsequent DeleteScene failure still
// reports the committed file removal to the caller for Session.NotifyPlayers
// (Critic fix #3). keepAll never removes anything, so it always returns nil
// changes.
func ApplyLibraryAdult(ctx context.Context, libStore *library.Store, p proposals.Proposal, keepIndex *int, keepAll bool) (sceneID int64, changes []mode.PathChange, err error) {
	if p.Status != proposals.Pending {
		return 0, nil, fmt.Errorf("proposal %d is %q, not pending — nothing to apply", p.ID, p.Status)
	}
	if len(p.Candidates) < 2 {
		return 0, nil, fmt.Errorf("proposal %d has fewer than 2 candidates to resolve", p.ID)
	}

	if keepAll {
		for _, c := range p.Candidates {
			if c.TrackedID != 0 {
				return int64(c.TrackedID), nil, nil
			}
		}
		return 0, nil, nil
	}

	idx := winnerIndex(p.Candidates)
	if keepIndex != nil {
		if *keepIndex < 0 || *keepIndex >= len(p.Candidates) {
			return 0, nil, fmt.Errorf("proposal %d: keepIndex %d out of range", p.ID, *keepIndex)
		}
		idx = *keepIndex
	}
	winner := p.Candidates[idx]

	for i, c := range p.Candidates {
		if i == idx {
			continue
		}
		removedPath, err := removeSceneCandidate(ctx, libStore, c)
		// Capture the committed physical deletion before checking err — the
		// file can already be gone even when the subsequent DB row deletion
		// fails, and NotifyPlayers must still learn about it (Critic fix #3).
		if removedPath != "" {
			changes = append(changes, mode.PathChange{Path: removedPath, Kind: mode.Deleted})
		}
		if err != nil {
			return 0, changes, fmt.Errorf("removing %s: %w", c.Path, err)
		}
	}

	if winner.TrackedID != 0 {
		return int64(winner.TrackedID), changes, nil
	}

	// Persist the winner's phash + file identity so the next Scan finds it
	// cached and skips re-decoding this file. winner.PHash was computed at Scan
	// time (attachPHashesScene) and rode through candidates_json; a stat failure
	// just leaves the identity empty, self-invalidating on the next Scan. The
	// scene's (box, scene_id) identity comes from the proposal's GiveBackBox/
	// GiveBackSceneID — captured from the identify.MatchResult at Scan time, the
	// same separate-column pair library.Scene is keyed on.
	winnerSize, winnerMTime, _ := fileIdentity(winner.Path)
	scene, err := libStore.UpsertScene(ctx, library.Scene{
		Box: p.GiveBackBox, SceneID: p.GiveBackSceneID,
		Title: p.Title, Studio: p.Studio, Date: p.Date,
		FilePath: winner.Path, RootFolderPath: p.RootFolderPath,
		PHash: winner.PHash, PHashFileSize: winnerSize, PHashFileMTime: winnerMTime,
	})
	if err != nil {
		return 0, changes, fmt.Errorf("registering surviving copy %q: %w", p.Title, err)
	}
	return scene.ID, changes, nil
}

// removeSceneCandidate removes c's file (and, for a tracked candidate, its
// library_scenes row) and returns the exact path that was removed — "" if
// nothing was actually deleted. A tracked loser's file is removed at c.Path
// (library exposes no get-scene-by-id to re-resolve an authoritative path, so
// this is the Series-style removal, not the Movies-style Get-then-remove), and
// its DeleteScene is attempted after; the removed path is returned even when
// DeleteScene fails so the caller can still report the committed deletion.
func removeSceneCandidate(ctx context.Context, libStore *library.Store, c proposals.Candidate) (string, error) {
	if c.TrackedID == 0 {
		if err := os.Remove(c.Path); err != nil {
			return "", err
		}
		return c.Path, nil
	}
	removedPath := ""
	if c.Path != "" {
		if err := os.Remove(c.Path); err != nil && !os.IsNotExist(err) {
			return "", fmt.Errorf("deleting %q: %w", c.Path, err)
		}
		removedPath = c.Path
	}
	if err := libStore.DeleteScene(ctx, int64(c.TrackedID)); err != nil {
		// The file is already physically gone (os.Remove above succeeded) even
		// though the DB row deletion failed — return removedPath alongside the
		// error so the caller still reports the committed deletion to
		// NotifyPlayers, matching removeLibraryCandidate's sibling behavior and
		// the "capture at the point the os-level mutation lands" rule.
		return removedPath, err
	}
	return removedPath, nil
}
