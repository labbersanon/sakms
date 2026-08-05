// Package dedup implements SAK's Dedup workflow: find content that's been
// identified twice — once as an already-tracked item, once (or more) as an
// orphaned file that resolves to the same identity — and stage a proposal
// to keep the better-quality copy instead of leaving both silently in
// place (today's behavior in both source CLIs).
//
// Movies and Series both group PURELY by perceptual hash now (ScanLibraryPHash /
// ScanLibrarySeriesPHash, shipped 2026-07-18 in 50dd970) — no TMDB id or
// (show,season,episode) key gates the union; two files perceptually similar
// enough group regardless of what either resolves to, or whether either
// resolves at all (see dedup_phash_primary.go's own doc comments and
// .omc/specs/deep-interview-phash-grouping.md). Adult still groups by the
// resolved scene's foreignID (ScanLibraryAdult) and refines with the shared
// refineByPHash helper. Every mode runs its own libStore-backed
// ScanLibrary*/ApplyLibrary* sibling, dispatched at the API layer.
//
// CORRECTION (logical episode-splitting): Series' UNIQUE(series_id, season,
// episode) constraint rules out ambiguity for ONE (series,season,episode)
// row, but does NOT mean a file backing that row is exclusively used
// there — a logical-episode-split file (library.ParseEpisodeNumbers)
// legitimately backs TWO rows' FilePath at once (e.g. a "S01E01-E02" file is
// both episode 1's and episode 2's row). ApplyLibrarySeries' delete step
// accounts for this: see its own doc comment and
// library.Store.CountEpisodesByFilePath.
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
	"slices"
	"time"

	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mediainfo"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/phash"
	"github.com/labbersanon/sakms/internal/place"
	"github.com/labbersanon/sakms/internal/proposals"
)

// Prober is the subset of *mediainfo.Prober Scan needs — an interface so
// tests can inject a fake without a real ffprobe binary or media file.
type Prober interface {
	Probe(ctx context.Context, path string) (*mediainfo.Probe, error)
}

// PHasher is the subset of *phash.Hasher the phash-driven Scans need — an
// interface so tests can inject a fake without a real ffmpeg binary or video
// file, exactly as Prober does for ffprobe. Movies (ScanLibraryPHash) and
// Series (ScanLibrarySeriesPHash) use it to compute every candidate's hash
// for union-find grouping; Adult (ScanLibraryAdult) uses it to refine an
// already-TMDB-grouped set via refineByPHash. Adult alone recomputes every
// scan (no SAK-owned row to cache against).
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
		// so the caller's own len<2 check (ScanLibraryAdult) makes the
		// no-proposal call; indexing candidates[0] below would panic on the 0 case.
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

// findVideoFile resolves an orphan/tracked path to an allowlisted video file
// via library.ResolveVideoFile (Jellyfin-parity VideoExts).
func findVideoFile(path string) (string, error) {
	return library.ResolveVideoFile(path)
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
//
// tier is the operator's configured quality tier for this mode, recorded on
// the winner's library row alongside its size. Dedup captures both even though
// the feature's ACs only name Apply/Grab time: a Dedup Apply replaces the
// tracked file with a different one, so leaving the old size on the row is
// exactly the UI-vs-filesystem drift this project's Mission declares the bar
// against. Plain string, not a settings lookup — this package deliberately
// does not import internal/settings; the internal/api caller resolves it.
func ApplyLibrary(ctx context.Context, libStore *library.Store, p proposals.Proposal, keepIndex *int, additionalKeepIndices []int, keepAll bool, tier string) (itemID int64, changes []mode.PathChange, err error) {
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
		// Reuses winnerSize from the fileIdentity stat above — zero new I/O.
		Size: winnerSize, QualityTier: tier,
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
//
// tier is the operator's configured quality tier for this mode — see
// ApplyLibrary for why Dedup captures size/tier at all and why it's a plain
// string parameter.
func ApplyLibrarySeries(ctx context.Context, libStore *library.Store, p proposals.Proposal, keepIndex *int, additionalKeepIndices []int, keepAll bool, tier string) (episodeID int64, changes []mode.PathChange, err error) {
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
		// Reuses winnerSize from the fileIdentity stat above — zero new I/O.
		Size: winnerSize, QualityTier: tier,
		PHash: winner.PHash, PHashFileSize: winnerSize, PHashFileMTime: winnerMTime,
	})
	if err != nil {
		return 0, changes, fmt.Errorf("registering surviving copy %q: %w", p.Title, err)
	}
	return ep.ID, changes, nil
}
