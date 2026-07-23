// Package dedup implements SAK's Dedup workflow: find content that's been
// identified twice — once as an already-tracked item, once (or more) as an
// orphaned file that resolves to the same identity — and stage a proposal
// to keep the better-quality copy instead of leaving both silently in
// place (today's behavior in both source CLIs).
//
// Movies groups by TMDB id (ScanLibrary); Adult groups by the resolved
// scene's foreignID (ScanLibraryAdult); Series groups by
// (show TMDB id, season, episode) — see ScanLibrarySeries, whose grouping
// resolves both questions an earlier version of this comment used to flag
// as undecided: "the tracked copy" is just the one library.Episode row for
// that exact key (the schema's own UNIQUE constraint rules out ambiguity),
// and a duplicate season-pack file groups with a duplicate single-episode
// file naturally, since a season pack is broken into individual files
// (library.ResolveEpisodeVideoFiles) before grouping ever happens. Every mode
// runs its own libStore-backed ScanLibrary*/ApplyLibrary* sibling, dispatched
// at the API layer.
//
// CORRECTION (logical episode-splitting): the UNIQUE(series_id, season,
// episode) constraint rules out ambiguity for ONE key, but does NOT mean a
// file backing that key is exclusively used there — a logical-episode-split
// file (library.ParseEpisodeNumbers) legitimately backs TWO keys' FilePath
// at once (e.g. a "S01E01-E02" file is both episode 1's and episode 2's
// row). ApplyLibrarySeries' delete step accounts for this: see its own doc
// comment and library.Store.CountEpisodesByFilePath.
//
// Quality comparison never trusts a *arr app's own reported file quality —
// every candidate, tracked or not, gets ffprobed directly by SAK itself
// (see internal/mediainfo and internal/place). This sidesteps depending on
// Radarr's nested moviefile-quality API shape (unverified against a live
// instance) and matches the design spec's own framing: Dedup is "always a
// filesystem-scan-and-compare workflow," never a *arr-database one.
package dedup

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/labbersanon/sakms/internal/config"
	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mediainfo"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/phash"
	"github.com/labbersanon/sakms/internal/place"
	"github.com/labbersanon/sakms/internal/proposals"
	"github.com/labbersanon/sakms/internal/searchterm"
)

// Prober is the subset of *mediainfo.Prober Scan needs — an interface so
// tests can inject a fake without a real ffprobe binary or media file.
type Prober interface {
	Probe(ctx context.Context, path string) (*mediainfo.Probe, error)
}

// PHasher is the subset of *phash.Hasher the phash-refined Scans need — an
// interface so tests can inject a fake without a real ffmpeg binary or video
// file, exactly as Prober does for ffprobe. All three modes refine their groups
// with it: the library-backed Movies (ScanLibrary), Series (ScanLibrarySeries),
// and Adult (ScanLibraryAdult). Adult alone recomputes every scan (no SAK-owned
// row to cache against).
type PHasher interface {
	Hash(ctx context.Context, path string) (string, error)
}

// fileIdentity returns the size and a UTC RFC3339Nano mtime string used as the
// phash cache key — a cached hash is valid only while a file's current
// size+mtime still match what was stored, detecting a replaced/re-encoded file
// at the same path. Written and compared only here, so the mtime string format
// is internally consistent between the cache write and the later cache check.
func fileIdentity(path string) (size int64, mtime string, err error) {
	fi, err := os.Stat(path)
	if err != nil {
		return 0, "", err
	}
	return fi.Size(), fi.ModTime().UTC().Format(time.RFC3339Nano), nil
}

// attachPHashes computes a perceptual hash for each candidate and returns only
// those it could hash, each with PHash set. The tracked candidate reuses its
// cached library hash when the file's identity (size+mtime) AND the hash's
// scheme still match — the decode-once win — otherwise it, like every orphan,
// is hashed fresh, and a freshly computed tracked hash is written back via
// UpdatePHash. A candidate whose hash can't be computed is dropped, matching
// probeCandidate's tolerant "report whatever could be measured" posture. A
// UpdatePHash failure is a best-effort cache miss (dedup has no logger — see
// the package doc): it only costs a recompute next Scan, never a failed Scan.
func attachPHashes(ctx context.Context, hasher PHasher, libStore *library.Store, candidates []proposals.Candidate, tracked *library.Item) []proposals.Candidate {
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
				_ = libStore.UpdatePHash(ctx, int64(c.TrackedID), h, size, mtime)
			}
		}
		out = append(out, c)
	}
	return out
}

// attachPHashesSeries is attachPHashes' Series-typed sibling — identical body,
// differing only in the tracked type (*library.Episode) and the write-back
// method (UpdateEpisodePHash on library_episodes). Kept as a parallel sibling
// rather than a shared generic helper (CLAUDE.md's "prefer parallel sibling
// functions" convention), so the just-shipped Movies path stays untouched.
func attachPHashesSeries(ctx context.Context, hasher PHasher, libStore *library.Store, candidates []proposals.Candidate, tracked *library.Episode) []proposals.Candidate {
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
				_ = libStore.UpdateEpisodePHash(ctx, int64(c.TrackedID), h, size, mtime)
			}
		}
		out = append(out, c)
	}
	return out
}

// refineByPHash keeps only the candidates perceptually similar to a reference:
// the tracked candidate if the group has one, else the first candidate. A
// candidate whose hash is outside perFrameThreshold average Hamming bits/frame
// of the reference is removed from the GROUP — it stays on disk untouched.
// This is the strictly-more-conservative "keep both" behavior: files sharing a
// TMDB id but looking different (a wrong match, a different cut, an extras
// file) are not treated as duplicates. The reference itself is always kept,
// and input order is preserved so markWinner/the proposal see the same shape.
func refineByPHash(candidates []proposals.Candidate, frames, perFrameThreshold int) []proposals.Candidate {
	if len(candidates) < 2 {
		// Nothing to refine — 0 survivors (every candidate was uncomputable,
		// e.g. ffmpeg missing or every file corrupt) or 1 survivor. Return as-is
		// so the caller's own len<2 check (ScanLibrary) makes the no-proposal
		// call; indexing candidates[0] below would panic on the 0 case.
		return candidates
	}
	refIdx := 0
	for i, c := range candidates {
		if c.TrackedID != 0 {
			refIdx = i
			break
		}
	}
	ref := candidates[refIdx]
	out := make([]proposals.Candidate, 0, len(candidates))
	for i, c := range candidates {
		if i == refIdx {
			out = append(out, c)
			continue
		}
		within, err := phash.SimilarityWithin(ref.PHash, c.PHash, frames, perFrameThreshold)
		if err == nil && within {
			out = append(out, c)
		}
	}
	return out
}

var videoExts = map[string]bool{
	".mkv": true, ".mp4": true, ".avi": true, ".m4v": true,
	".ts": true, ".wmv": true, ".mov": true, ".webm": true,
}

// findVideoFile resolves an unmapped-folder or tracked-item path to an
// actual video file: itself, if it already is one, or the largest
// video-extensioned file directly inside it, if it's a directory.
func findVideoFile(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("stat %s: %w", path, err)
	}
	if !info.IsDir() {
		return path, nil
	}

	entries, err := os.ReadDir(path)
	if err != nil {
		return "", fmt.Errorf("reading %s: %w", path, err)
	}
	var best string
	var bestSize int64
	for _, e := range entries {
		if e.IsDir() || !videoExts[strings.ToLower(filepath.Ext(e.Name()))] {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		if fi.Size() > bestSize {
			bestSize = fi.Size()
			best = filepath.Join(path, e.Name())
		}
	}
	if best == "" {
		return "", fmt.Errorf("no video file found under %s", path)
	}
	return best, nil
}

// probeCandidate resolves path to a real video file and ffprobes it,
// returning nil (not an error) if either step fails — a duplicate group
// that can't be probed on one side still gets reported with whatever
// candidates could be measured, rather than the whole group disappearing.
func probeCandidate(ctx context.Context, prober Prober, label, path string, trackedID int) *proposals.Candidate {
	videoPath, err := findVideoFile(path)
	if err != nil {
		return nil
	}
	probe, err := prober.Probe(ctx, videoPath)
	if err != nil {
		return nil
	}
	return &proposals.Candidate{
		Label: label, Path: videoPath, TrackedID: trackedID,
		Resolution: probe.Height, Codec: probe.CodecName, BitRate: probe.BitRate,
	}
}

// markWinner sets Winner on whichever candidate place.QualityKey ranks
// highest. SourceRank is always 0 (see the package doc comment).
func markWinner(candidates []proposals.Candidate) {
	best := 0
	bestKey := place.NewQualityKey(candidates[0].Resolution, 0, candidates[0].Codec, candidates[0].BitRate)
	for i := 1; i < len(candidates); i++ {
		key := place.NewQualityKey(candidates[i].Resolution, 0, candidates[i].Codec, candidates[i].BitRate)
		if key.Greater(bestKey) {
			best, bestKey = i, key
		}
	}
	candidates[best].Winner = true
}

func winnerIndex(candidates []proposals.Candidate) int {
	for i, c := range candidates {
		if c.Winner {
			return i
		}
	}
	return 0
}

// ScanLibrary is Dedup's Movies-library scan — used only for Movies mode now
// that Radarr no longer sits between SAK and the filesystem/TMDB (see
// internal/library's package doc). Identifies every unmapped file (via TMDB
// search instead of Servarr's Lookup) and groups it, and any already-tracked
// library item, by TMDB id.
func ScanLibrary(ctx context.Context, sess *mode.Session, libStore *library.Store, rootFolderPath string, prober Prober, hasher PHasher, perFrameThreshold int) ([]proposals.Proposal, error) {
	if sess.TMDB == nil {
		return nil, fmt.Errorf("tmdb isn't configured yet — add it in Settings first")
	}
	if rootFolderPath == "" {
		return nil, fmt.Errorf("no Movies library root folder configured yet — add one in Settings first")
	}

	tracked, err := libStore.List(ctx, mode.Movies)
	if err != nil {
		return nil, fmt.Errorf("loading library items: %w", err)
	}
	trackedByTMDB := make(map[int]library.Item, len(tracked))
	known := make(map[string]bool, len(tracked))
	for _, t := range tracked {
		trackedByTMDB[t.TMDBID] = t
		// Marking just the file path is enough — ScanRootFolder's recursive
		// walk decides atomicity dynamically from known at whatever depth it
		// encounters a directory, so it doesn't need the wrapping folder
		// pre-marked too.
		known[t.FilePath] = true
	}

	type orphanHit struct {
		name, path, title string
	}
	orphansByTMDB := make(map[int][]orphanHit)

	entries, err := library.ScanRootFolder(rootFolderPath, known)
	if err != nil {
		return nil, fmt.Errorf("scanning %s: %w", rootFolderPath, err)
	}
	for _, entry := range entries {
		if config.SidecarExts[strings.ToLower(filepath.Ext(entry.Name))] {
			continue
		}
		items, err := sess.TMDB.SearchMovies(ctx, searchterm.FromName(entry.Name))
		if err != nil || len(items) == 0 {
			continue // not Dedup's concern — Rename's own ScanLibrary surfaces unmatched items
		}
		match := items[0]
		orphansByTMDB[match.ID] = append(orphansByTMDB[match.ID], orphanHit{name: entry.Name, path: entry.Path, title: match.Title})
	}

	var out []proposals.Proposal
	for tmdbID, orphans := range orphansByTMDB {
		trackedItem, isTracked := trackedByTMDB[tmdbID]
		if !isTracked && len(orphans) < 2 {
			continue // a single new, untracked item — nothing to dedup
		}

		title := orphans[0].title
		rootPath := ""
		var candidates []proposals.Candidate
		if isTracked {
			if c := probeCandidate(ctx, prober, "tracked", trackedItem.FilePath, int(trackedItem.ID)); c != nil {
				candidates = append(candidates, *c)
			}
			title, rootPath = trackedItem.Title, trackedItem.RootFolderPath
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

		// Refine the same-TMDB group by perceptual similarity: hash each
		// candidate (reusing a tracked item's cached hash when its file is
		// unchanged), then drop any candidate outside the threshold of the
		// group's reference. A group refined below 2 survivors is not a
		// duplicate — the strictly-more-conservative keep-both behavior.
		var trackedPtr *library.Item
		if isTracked {
			ti := trackedItem
			trackedPtr = &ti
		}
		candidates = attachPHashes(ctx, hasher, libStore, candidates, trackedPtr)
		candidates = refineByPHash(candidates, phash.Frames, perFrameThreshold)
		if len(candidates) < 2 {
			continue // perceptually dissimilar — keep both, no proposal
		}
		markWinner(candidates)

		out = append(out, proposals.Proposal{
			Mode: mode.Movies, Workflow: proposals.Dedup, Status: proposals.Pending,
			SourceName: title, Title: title, TMDBID: tmdbID, RootFolderPath: rootPath,
			Candidates: candidates,
			Reason:     fmt.Sprintf("%d copies identified as %q", len(candidates), title),
		})
	}
	return out, nil
}

// ApplyLibrary is Dedup's Movies-library apply — resolves p against libStore:
// a tracked loser's file is removed and its library record deleted, an
// untracked orphan loser's file is removed directly, and an untracked winner
// is recorded via libStore.Upsert
// (no registration/rescan round trip needed — Upsert itself IS the "now
// tracked" state).
//
// changes accumulates one Deleted PathChange per removed loser (the winner
// never moves, so it never appears in changes) — a named return so a
// post-removal failure (the winner's libStore.Upsert) still reports every
// loser that was actually removed to the caller for Session.NotifyPlayers.
// keepAll never removes anything, so it always returns nil changes.
//
// additionalKeepIndices generalizes the delete step to multi-keep: besides the
// single primary keeper (idx), every candidate whose index is in this set is
// also left on disk untouched, exactly as the winner is. Only the primary is
// ever tracked (Upsert) — additional keepers are files kept but not recorded,
// matching keepAll's documented "extra kept-but-untracked file" behavior (see
// .omc/plans/dedup-ux-refine.md AC6/AC10). Nil/empty means single-keep, the
// original behavior. keepAll (track nothing) stays a distinct third state and
// is never combined with this (rejected upstream by validateApplyRequest).
func ApplyLibrary(ctx context.Context, libStore *library.Store, p proposals.Proposal, keepIndex *int, additionalKeepIndices []int, keepAll bool) (itemID int64, changes []mode.PathChange, err error) {
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
		// Skip the primary keeper and every additional checked keeper — those
		// files stay on disk (only the primary is tracked below).
		if i == idx || slices.Contains(additionalKeepIndices, i) {
			continue
		}
		removedPath, err := removeLibraryCandidate(ctx, libStore, c)
		// Capture the committed physical deletion before checking err — the
		// file can already be gone even when the subsequent DB row deletion
		// fails, and NotifyPlayers must still learn about it (Critic fix #3).
		if removedPath != "" {
			changes = append(changes, mode.PathChange{Path: removedPath, Kind: mode.Deleted})
			// Event-driven vmaf_scores cleanup: the file is physically gone
			// (captured even when the DB row delete failed), so any cached VMAF
			// pair naming it — on either side — is now dead. Best-effort, not
			// transactional with os.Remove (see PruneVMAFScoresForPath).
			libStore.PruneVMAFScoresForPath(ctx, removedPath)
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
	// time (attachPHashes) and rode through candidates_json; a stat failure
	// just leaves the identity empty, self-invalidating on the next Scan.
	winnerSize, winnerMTime, _ := fileIdentity(winner.Path)
	item, err := libStore.Upsert(ctx, library.Item{
		Mode: mode.Movies, TMDBID: p.TMDBID, Title: p.Title,
		FilePath: winner.Path, RootFolderPath: p.RootFolderPath,
		PHash: winner.PHash, PHashFileSize: winnerSize, PHashFileMTime: winnerMTime,
	})
	if err != nil {
		return 0, changes, fmt.Errorf("registering surviving copy %q: %w", p.Title, err)
	}
	return item.ID, changes, nil
}

// removeLibraryCandidate removes c's file (and, for a tracked candidate, its
// libStore record) and returns the exact path that was removed — "" if
// nothing was actually deleted (a tracked candidate whose FilePath is
// already empty), so callers must guard against appending an empty-path
// PathChange (see ApplyLibrary).
func removeLibraryCandidate(ctx context.Context, libStore *library.Store, c proposals.Candidate) (string, error) {
	if c.TrackedID == 0 {
		if err := os.Remove(c.Path); err != nil {
			return "", err
		}
		return c.Path, nil
	}
	item, err := libStore.Get(ctx, int64(c.TrackedID))
	if err != nil {
		return "", fmt.Errorf("loading library item %d: %w", c.TrackedID, err)
	}
	removedPath := ""
	if item.FilePath != "" {
		if err := os.Remove(item.FilePath); err != nil && !os.IsNotExist(err) {
			return "", fmt.Errorf("deleting %q: %w", item.FilePath, err)
		}
		removedPath = item.FilePath
	}
	if err := libStore.Delete(ctx, int64(c.TrackedID)); err != nil {
		// The file is already physically gone (os.Remove above succeeded) even
		// though the DB row deletion failed — return removedPath alongside the
		// error so the caller still reports the committed deletion to
		// NotifyPlayers, matching purge.ApplyLibrary's sibling behavior and the
		// "capture at the point the os-level mutation lands" rule used
		// throughout this feature (Critic fix #3).
		return removedPath, err
	}
	return removedPath, nil
}

// episodeDedupKey groups Series duplicates at the episode level — the
// answer to "what does a duplicate mean for Series": two files are
// duplicates of each other only if they resolve to the same show AND the
// same season/episode. A season-pack orphan is broken into individual
// files (library.ResolveEpisodeVideoFiles) before this key is ever
// computed, so a duplicate episode inside a pack groups naturally with a
// duplicate loose file for that same episode.
type episodeDedupKey struct {
	tmdbID, season, episode int
}

// ScanLibrarySeries is Dedup's Series-library counterpart to ScanLibrary —
// identifies every unmapped file (or file inside an unmapped season-pack
// directory) via TMDB TV search, and groups it, and any already-tracked
// episode, by episodeDedupKey. "The tracked copy" for a key is simply the
// one library.Episode row for that exact (series, season, episode) —
// the schema's own UNIQUE(series_id, season_number, episode_number)
// constraint already rules out there ever being more than one, unlike
// Adult's string-matched foreignID grouping.
func ScanLibrarySeries(ctx context.Context, sess *mode.Session, libStore *library.Store, rootFolderPath string, prober Prober, hasher PHasher, perFrameThreshold int) ([]proposals.Proposal, error) {
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

	trackedByKey := make(map[episodeDedupKey]library.Episode)
	seriesByID := make(map[int64]library.Series, len(allSeries))
	known := map[string]bool{}
	for _, s := range allSeries {
		seriesByID[s.ID] = s
		episodes, err := libStore.ListEpisodes(ctx, s.ID)
		if err != nil {
			return nil, fmt.Errorf("loading episodes for %q: %w", s.Title, err)
		}
		for _, ep := range episodes {
			if ep.FilePath == "" {
				continue // known from TMDB but not on disk — not a duplicate target
			}
			// Marking just the file path is enough — ScanRootFolder's
			// recursive walk decides atomicity dynamically from known at
			// whatever depth it encounters a directory.
			known[ep.FilePath] = true
			trackedByKey[episodeDedupKey{tmdbID: s.TMDBID, season: ep.SeasonNumber, episode: ep.EpisodeNumber}] = ep
		}
	}

	type orphanHit struct {
		name, path, title string
	}
	orphansByKey := make(map[episodeDedupKey][]orphanHit)

	entries, err := library.ScanRootFolder(rootFolderPath, known)
	if err != nil {
		return nil, fmt.Errorf("scanning %s: %w", rootFolderPath, err)
	}
	for _, entry := range entries {
		if config.SidecarExts[strings.ToLower(filepath.Ext(entry.Name))] {
			continue
		}
		videoFiles, err := library.ResolveEpisodeVideoFiles(entry.Path)
		if err != nil {
			continue // not Dedup's concern — Rename's own ScanLibrarySeries surfaces unmatched items
		}
		for _, videoPath := range videoFiles {
			name := filepath.Base(videoPath)
			season, episode, ok := library.ParseEpisodeFilename(name)
			if !ok {
				continue
			}
			items, err := sess.TMDB.SearchTV(ctx, searchterm.FromName(library.StripEpisodeMarker(name)))
			if err != nil || len(items) == 0 {
				continue
			}
			match := items[0]
			key := episodeDedupKey{tmdbID: match.ID, season: season, episode: episode}
			orphansByKey[key] = append(orphansByKey[key], orphanHit{name: name, path: videoPath, title: match.Title})
		}
	}

	var out []proposals.Proposal
	for key, orphans := range orphansByKey {
		trackedEp, isTracked := trackedByKey[key]
		if !isTracked && len(orphans) < 2 {
			continue // a single new, untracked episode — nothing to dedup
		}

		title := orphans[0].title
		rootPath := ""
		var candidates []proposals.Candidate
		if isTracked {
			if c := probeCandidate(ctx, prober, "tracked", trackedEp.FilePath, int(trackedEp.ID)); c != nil {
				candidates = append(candidates, *c)
			}
			if s, ok := seriesByID[trackedEp.SeriesID]; ok {
				title, rootPath = s.Title, s.RootFolderPath
			}
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

		// Refine the same-(show,season,episode) group by perceptual
		// similarity, exactly as ScanLibrary does per-TMDB: hash each
		// candidate (reusing a tracked episode's cached hash when its file is
		// unchanged), then drop any candidate outside the threshold of the
		// group's reference. A group refined below 2 survivors is not a
		// duplicate — the strictly-more-conservative keep-both behavior.
		var trackedPtr *library.Episode
		if isTracked {
			te := trackedEp
			trackedPtr = &te
		}
		candidates = attachPHashesSeries(ctx, hasher, libStore, candidates, trackedPtr)
		candidates = refineByPHash(candidates, phash.Frames, perFrameThreshold)
		if len(candidates) < 2 {
			continue // perceptually dissimilar — keep both, no proposal
		}
		markWinner(candidates)

		out = append(out, proposals.Proposal{
			Mode: mode.Series, Workflow: proposals.Dedup, Status: proposals.Pending,
			SourceName: title, Title: title, TMDBID: key.tmdbID,
			SeasonNumber: key.season, EpisodeNumber: key.episode, RootFolderPath: rootPath,
			Candidates: candidates,
			Reason:     fmt.Sprintf("%d copies identified as %q S%02dE%02d", len(candidates), title, key.season, key.episode),
		})
	}
	return out, nil
}

// ApplyLibrarySeries is Dedup's Series-library counterpart to ApplyLibrary.
// Unlike Movies, a losing tracked candidate never needs an explicit row
// delete: the (series, season, episode) row the tracked loser occupied is
// simply overwritten by the winner's file path via UpsertEpisode.
//
// A losing candidate's FILE, however, is NOT always safe to delete — a
// logical-episode-split file (library.ParseEpisodeNumbers) can legitimately
// back a DIFFERENT episode's row too (e.g. episode 1 and episode 2 sharing
// one "S01E01-E02" file). If episode 1's dedup group picks a better
// standalone copy of episode 1 and the shared file loses, deleting it
// outright would orphan episode 2's row — a live, silent violation of this
// project's "no drift" mission (see CLAUDE.md's Mission section), not a
// hypothetical. Each losing candidate's path is checked via
// library.Store.CountEpisodesByFilePath before removal: a count <= 1 means
// only the row this Apply call is about to overwrite (or nothing) claims
// it, safe to delete exactly as before; a count > 1 means another episode's
// row still needs this file — skip the physical delete and log why, but
// still let this proposal's own key move on to the winner via UpsertEpisode
// below (that row's own reference to the shared path is what's being
// replaced, not the file itself).
//
// changes accumulates one Deleted PathChange per ACTUALLY removed loser
// (the winner never moves, so it never appears in changes; a loser skipped
// via the shared-file guard above doesn't either, since nothing was
// deleted) — a named return so a post-removal failure further down still
// reports every loser that was actually removed to the caller for
// Session.NotifyPlayers. keepAll never removes anything, so it always
// returns nil changes.
//
// additionalKeepIndices generalizes the delete step to multi-keep exactly as
// ApplyLibrary's does: a candidate whose index is in this set is left on disk
// untouched alongside the primary keeper. This OR's cleanly with the
// shared-file guard below — a candidate is skipped from deletion if it is the
// primary, an additional keeper, OR still referenced by another episode's row.
// Only the primary is tracked (UpsertEpisode); additional keepers are kept but
// not recorded (see .omc/plans/dedup-ux-refine.md AC6/AC10/AC11). keepAll (track
// nothing) stays a distinct third state, never combined with this.
func ApplyLibrarySeries(ctx context.Context, libStore *library.Store, p proposals.Proposal, keepIndex *int, additionalKeepIndices []int, keepAll bool) (episodeID int64, changes []mode.PathChange, err error) {
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
		// Skip the primary keeper and every additional checked keeper — those
		// files stay on disk (only the primary is tracked via UpsertEpisode).
		if i == idx || slices.Contains(additionalKeepIndices, i) {
			continue
		}
		if c.Path == "" {
			continue
		}
		// Shared-file guard (see this function's doc comment): don't delete
		// a losing candidate's file out from under a DIFFERENT episode's
		// row that still references it. refCount <= 1 means only the row
		// this Apply call is about to overwrite (or nothing) claims this
		// path — a count that already existed and was safe to delete before
		// logical episode-splitting existed.
		refCount, countErr := libStore.CountEpisodesByFilePath(ctx, c.Path)
		if countErr != nil {
			return 0, changes, fmt.Errorf("checking whether %s is still referenced: %w", c.Path, countErr)
		}
		if refCount > 1 {
			log.Printf("dedup: skipping delete of %s — still referenced by %d episode row(s) other than this proposal's own (logical episode-split file)", c.Path, refCount)
			continue
		}
		if err := os.Remove(c.Path); err != nil && !os.IsNotExist(err) {
			return 0, changes, fmt.Errorf("removing %s: %w", c.Path, err)
		}
		changes = append(changes, mode.PathChange{Path: c.Path, Kind: mode.Deleted})
		// Event-driven vmaf_scores cleanup for the just-deleted loser. Reached
		// only after the refCount>1 shared-file guard above lets the delete
		// through, so a still-referenced file (skipped by continue) is never
		// pruned. Best-effort (see PruneVMAFScoresForPath).
		libStore.PruneVMAFScoresForPath(ctx, c.Path)
	}

	if winner.TrackedID != 0 {
		return int64(winner.TrackedID), changes, nil
	}

	series, err := libStore.UpsertSeries(ctx, library.Series{
		TMDBID: p.TMDBID, Title: p.Title, RootFolderPath: p.RootFolderPath,
	})
	if err != nil {
		return 0, changes, fmt.Errorf("recording series %q: %w", p.Title, err)
	}

	title, airDate := "", ""
	if existing, err := libStore.GetEpisode(ctx, series.ID, p.SeasonNumber, p.EpisodeNumber); err == nil {
		title, airDate = existing.Title, existing.AirDate
	} else if !errors.Is(err, library.ErrNotFound) {
		return 0, changes, fmt.Errorf("checking existing episode metadata: %w", err)
	}

	// Persist the winner's phash + file identity so the next Scan finds it
	// cached and skips re-decoding this file. winner.PHash was computed at Scan
	// time (attachPHashesSeries) and rode through candidates_json; a stat
	// failure just leaves the identity empty, self-invalidating on the next Scan.
	winnerSize, winnerMTime, _ := fileIdentity(winner.Path)
	ep, err := libStore.UpsertEpisode(ctx, library.Episode{
		SeriesID: series.ID, SeasonNumber: p.SeasonNumber, EpisodeNumber: p.EpisodeNumber,
		Title: title, AirDate: airDate, FilePath: winner.Path,
		PHash: winner.PHash, PHashFileSize: winnerSize, PHashFileMTime: winnerMTime,
	})
	if err != nil {
		return 0, changes, fmt.Errorf("registering surviving copy %q: %w", p.Title, err)
	}
	return ep.ID, changes, nil
}
