// Package dedup implements SAK's Dedup workflow: find content that's been
// identified twice — once as an already-tracked item, once (or more) as an
// orphaned file that resolves to the same identity — and stage a proposal
// to keep the better-quality copy instead of leaving both silently in
// place (today's behavior in both source CLIs).
//
// Movies groups by TMDB id (scanMovies/ScanLibrary); Adult groups by the
// resolved scene's foreignID (scanAdult); Series groups by
// (show TMDB id, season, episode) — see ScanLibrarySeries, whose grouping
// resolves both questions an earlier version of this comment used to flag
// as undecided: "the tracked copy" is just the one library.Episode row for
// that exact key (the schema's own UNIQUE constraint rules out ambiguity),
// and a duplicate season-pack file groups with a duplicate single-episode
// file naturally, since a season pack is broken into individual files
// (library.ResolveEpisodeVideoFiles) before grouping ever happens. Scan/
// Apply are the generic Servarr-backed pair, serving only Adult now
// (Movies/Series both moved to their own libStore-backed
// ScanLibrary*/ApplyLibrary* siblings, dispatched at the API layer); Scan
// still refuses a Series session (Sonarr-backed or not) since that
// dispatch never routes here for Series.
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
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/curtiswtaylorjr/sakms/internal/config"
	"github.com/curtiswtaylorjr/sakms/internal/identify"
	"github.com/curtiswtaylorjr/sakms/internal/library"
	"github.com/curtiswtaylorjr/sakms/internal/mediainfo"
	"github.com/curtiswtaylorjr/sakms/internal/mode"
	"github.com/curtiswtaylorjr/sakms/internal/phash"
	"github.com/curtiswtaylorjr/sakms/internal/place"
	"github.com/curtiswtaylorjr/sakms/internal/proposals"
	"github.com/curtiswtaylorjr/sakms/internal/searchterm"
	"github.com/curtiswtaylorjr/sakms/internal/servarr"
)

// Prober is the subset of *mediainfo.Prober Scan needs — an interface so
// tests can inject a fake without a real ffprobe binary or media file.
type Prober interface {
	Probe(ctx context.Context, path string) (*mediainfo.Probe, error)
}

// PHasher is the subset of *phash.Hasher the phash-refined Scans need — an
// interface so tests can inject a fake without a real ffmpeg binary or video
// file, exactly as Prober does for ffprobe. All three modes refine their groups
// with it: the library-backed Movies (ScanLibrary) and Series
// (ScanLibrarySeries), and the Servarr-backed Adult (scanAdult). Adult alone
// recomputes every scan (no SAK-owned row to cache against — see
// attachPHashesAdult).
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

// attachPHashesAdult is attachPHashes' Servarr/Adult-typed sibling, stripped to
// its essence: hash every candidate fresh and drop any it can't hash. Adult is
// Whisparr-backed with no SAK-owned library row to cache a hash against (unlike
// Movies' library_items / Series' library_episodes), so there is deliberately no
// cache-read and no write-back — every Adult Dedup scan recomputes. That is a
// smaller, equally-correct scope, not a missing feature: refineByPHash's
// keep-both-on-<2 behavior is identical with or without a cache (see the package
// doc and CHANGELOG). A candidate whose hash can't be computed is dropped, the
// same tolerant posture as probeCandidate and attachPHashes.
func attachPHashesAdult(ctx context.Context, hasher PHasher, candidates []proposals.Candidate) []proposals.Candidate {
	out := make([]proposals.Candidate, 0, len(candidates))
	for _, c := range candidates {
		h, err := hasher.Hash(ctx, c.Path)
		if err != nil {
			continue // uncomputable — drop, same tolerance as attachPHashes/probeCandidate
		}
		c.PHash = h
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

// Scan dispatches on the session's app: Radarr runs the Movies duplicate
// detection (scanMovies, keyed by TMDB ID); Whisparr runs the Adult one
// (scanAdult, keyed by foreignID); anything else (including a Series
// session, whether it's still Sonarr-backed or already on its own library —
// sess.Servarr is nil in the latter case) is refused, since Series dedup
// isn't built yet (see the package doc).
//
// hasher and perFrameThreshold refine the Adult path by perceptual similarity
// exactly as ScanLibrary/ScanLibrarySeries do (see scanAdult). They are
// threaded through to scanMovies too for signature consistency, but the legacy
// Radarr path does not use them — Movies' real dedup is ScanLibrary.
func Scan(ctx context.Context, sess *mode.Session, prober Prober, hasher PHasher, perFrameThreshold int) ([]proposals.Proposal, error) {
	if sess.Servarr == nil {
		return nil, fmt.Errorf("dedup: Series-library dedup isn't implemented yet (tracked separately) — Movies and Adult only")
	}
	switch sess.Servarr.AppType() {
	case servarr.Radarr:
		return scanMovies(ctx, sess, prober, hasher, perFrameThreshold)
	case servarr.Whisparr:
		if sess.Identify == nil {
			return nil, fmt.Errorf("adult identification isn't configured — add an Ollama connection and set the Ollama model in Settings, plus at least one of StashDB/FansDB/TPDB")
		}
		return scanAdult(ctx, sess, prober, hasher, perFrameThreshold)
	default:
		return nil, fmt.Errorf("dedup: Series-library dedup isn't implemented yet (tracked separately) — Movies and Adult only, not %v", sess.Mode)
	}
}

// scanMovies identifies every unmapped file and groups it (and any
// already-tracked item) by resolved TMDB ID. A group with 2+ probeable
// candidates becomes a Pending Dedup proposal; a lone new item is left for
// Rename to handle, not reported here.
//
// hasher and perFrameThreshold are accepted for signature consistency with the
// phash-refined Scan dispatch but deliberately unused here: this is the legacy
// Radarr path, and Movies' real (phash-refined) dedup is ScanLibrary.
func scanMovies(ctx context.Context, sess *mode.Session, prober Prober, _ PHasher, _ int) ([]proposals.Proposal, error) {
	client := sess.Servarr

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

	trackedByTMDB := make(map[int]servarr.TrackedItem, len(tracked))
	for _, t := range tracked {
		if t.TMDBID != 0 {
			trackedByTMDB[t.TMDBID] = t
		}
	}

	type orphanHit struct {
		name, path string
		title      string
	}
	orphansByTMDB := make(map[int][]orphanHit)

	for _, root := range folders {
		for _, uf := range root.UnmappedFolders {
			if config.SidecarExts[strings.ToLower(filepath.Ext(uf.Name))] {
				continue
			}
			results, err := client.Lookup(ctx, searchterm.FromName(uf.Name))
			if err != nil || len(results) == 0 || results[0].TMDBID == 0 {
				continue // not Dedup's concern — Rename's own Scan already surfaces unmatched items
			}
			lr := results[0]
			orphansByTMDB[lr.TMDBID] = append(orphansByTMDB[lr.TMDBID], orphanHit{name: uf.Name, path: uf.Path, title: lr.Title})
		}
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
			if c := probeCandidate(ctx, prober, "tracked", trackedItem.Path, trackedItem.ID); c != nil {
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
		markWinner(candidates)

		out = append(out, proposals.Proposal{
			Mode: sess.Mode, Workflow: proposals.Dedup, Status: proposals.Pending,
			SourceName: title, Title: title, TMDBID: tmdbID, RootFolderPath: rootPath,
			QualityProfileID: servarr.DefaultQualityProfileID(tracked, rootPath, profiles),
			Candidates:       candidates,
			Reason:           fmt.Sprintf("%d copies identified as %q", len(candidates), title),
		})
	}
	return out, nil
}

// scanAdult is scanMovies' Whisparr counterpart: it groups a tracked scene and
// any unmapped copies of it by the normalized foreignID string (raw stash-box
// UUID, or "tpdbId:<id>" for a TPDB-only match), identifying orphans via
// sess.Identify exactly as Rename does. Structure mirrors scanMovies
// deliberately (per the plan) rather than sharing a parameterized helper.
//
// The tracked side skips any item whose ForeignID is empty — the same
// graceful-degradation posture Movies uses for TMDBID==0. If a real Whisparr
// GET /movie doesn't report foreignId (or reports it in a different format than
// Add sent), then len(tracked) > 0 but trackedByForeignID stays empty, and this
// silently degrades to orphan-vs-orphan dedup (keys computed locally from
// sess.Identify, independent of any Whisparr response) — no crash, no misgroup,
// no misfile. This is an UNVERIFIED assumption (no live Whisparr here); see the
// commit body. Deliberately not logged: no internal/* package in this codebase
// logs directly (only cmd/sakms/main.go does).
func scanAdult(ctx context.Context, sess *mode.Session, prober Prober, hasher PHasher, perFrameThreshold int) ([]proposals.Proposal, error) {
	client := sess.Servarr

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

	trackedByForeignID := make(map[string]servarr.TrackedItem, len(tracked))
	for _, t := range tracked {
		if t.ForeignID != "" {
			trackedByForeignID[t.ForeignID] = t
		}
	}

	type orphanHit struct {
		name, path, title, itemType string
	}
	orphansByForeignID := make(map[string][]orphanHit)

	for _, root := range folders {
		for _, uf := range root.UnmappedFolders {
			if config.SidecarExts[strings.ToLower(filepath.Ext(uf.Name))] {
				continue
			}
			res, err := sess.Identify.Identify(ctx, uf.Name, filepath.Base(root.Path))
			fid, itemType, title, ok := adultForeignID(res, err)
			if !ok {
				continue // web-only / no scene id / identify error — Rename's concern, not Dedup's
			}
			orphansByForeignID[fid] = append(orphansByForeignID[fid], orphanHit{name: uf.Name, path: uf.Path, title: title, itemType: itemType})
		}
	}

	var out []proposals.Proposal
	for fid, orphans := range orphansByForeignID {
		trackedItem, isTracked := trackedByForeignID[fid]
		if !isTracked && len(orphans) < 2 {
			continue // a single new, untracked scene — nothing to dedup
		}

		title := orphans[0].title
		rootPath := ""
		var candidates []proposals.Candidate
		if isTracked {
			if c := probeCandidate(ctx, prober, "tracked", trackedItem.Path, trackedItem.ID); c != nil {
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

		// Refine the same-foreignID group by perceptual similarity, exactly as
		// ScanLibrary/ScanLibrarySeries do: hash each candidate (always fresh —
		// Adult has no library row to cache against, see attachPHashesAdult), then
		// drop any candidate outside the threshold of the group's reference (the
		// tracked scene if present via its nonzero TrackedID, else the first
		// orphan). A group refined below 2 survivors is not a duplicate — the
		// strictly-more-conservative keep-both.
		candidates = attachPHashesAdult(ctx, hasher, candidates)
		candidates = refineByPHash(candidates, phash.Frames, perFrameThreshold)
		if len(candidates) < 2 {
			continue // perceptually dissimilar — keep both, no proposal
		}
		markWinner(candidates)

		out = append(out, proposals.Proposal{
			Mode: sess.Mode, Workflow: proposals.Dedup, Status: proposals.Pending,
			SourceName: title, Title: title,
			ForeignID: fid, ItemType: orphans[0].itemType, RootFolderPath: rootPath,
			QualityProfileID: servarr.DefaultQualityProfileID(tracked, rootPath, profiles),
			Candidates:       candidates,
			Reason:           fmt.Sprintf("%d copies identified as %q", len(candidates), title),
		})
	}
	return out, nil
}

// adultForeignID maps an Identify result to the normalized foreignID both sides
// group by, the item's type, and its title. ok is false for an identify error,
// a nil result, or a match with no valid Whisparr ForeignID. The actual
// derivation is delegated to identify.MatchResult.WhisparrForeignID so dedup
// and rename can never silently diverge on what a scene's foreignID is.
func adultForeignID(res *identify.MatchResult, err error) (fid, itemType, title string, ok bool) {
	if err != nil || res == nil {
		return "", "", "", false
	}
	fid, ok = res.WhisparrForeignID()
	if !ok {
		return "", "", "", false
	}
	return fid, res.Type, res.Title, true
}

// Apply resolves p by keeping exactly one candidate and removing the rest.
// keepIndex selects which candidate survives; nil means "auto" — whichever
// candidate Scan already marked Winner. keepAll skips all removal (both/all
// copies stay, matching the design's "Keep both" action) and takes
// precedence over keepIndex.
//
// If the surviving candidate wasn't already tracked (either it never was,
// or the tracked copy just lost), Apply registers it the same way Rename
// does, so the duplicate group always resolves to exactly one tracked item
// with a file behind it — never zero.
func Apply(ctx context.Context, sess *mode.Session, p proposals.Proposal, keepIndex *int, keepAll bool) (trackedID int, err error) {
	if p.Status != proposals.Pending {
		return 0, fmt.Errorf("proposal %d is %q, not pending — nothing to apply", p.ID, p.Status)
	}
	if len(p.Candidates) < 2 {
		return 0, fmt.Errorf("proposal %d has fewer than 2 candidates to resolve", p.ID)
	}

	// Structural safety guard at the TOP of Apply — before the removal loop and
	// both early-return paths (keepAll, already-tracked winner). A Whisparr scene
	// needs BOTH a ForeignID and an ItemType, or the Add below silently files the
	// surviving copy as a mis-typed movie (Whisparr's ItemType enum zero value is
	// "movie"). Placed here, not just before Add: the removal loop deletes losing
	// candidates first, so a guard placed late would destroy files and THEN refuse
	// — partial destruction. For a real Scan-produced Adult proposal these fields
	// are always set, so it only catches a hand-crafted / future-buggy proposal.
	if sess.Servarr.AppType() == servarr.Whisparr && (p.ForeignID == "" || p.ItemType == "") {
		return 0, fmt.Errorf("proposal %d has no scene identifier — refusing to register it as a mis-typed movie", p.ID)
	}

	if keepAll {
		for _, c := range p.Candidates {
			if c.TrackedID != 0 {
				return c.TrackedID, nil
			}
		}
		return 0, nil
	}

	idx := winnerIndex(p.Candidates)
	if keepIndex != nil {
		if *keepIndex < 0 || *keepIndex >= len(p.Candidates) {
			return 0, fmt.Errorf("proposal %d: keepIndex %d out of range", p.ID, *keepIndex)
		}
		idx = *keepIndex
	}
	winner := p.Candidates[idx]

	for i, c := range p.Candidates {
		if i == idx {
			continue
		}
		if err := removeCandidate(ctx, sess, c); err != nil {
			return 0, fmt.Errorf("removing %s: %w", c.Path, err)
		}
	}

	if winner.TrackedID != 0 {
		return winner.TrackedID, nil
	}

	id, err := sess.Servarr.Add(ctx, servarr.AddRequest{
		Title: p.Title, TMDBID: p.TMDBID,
		ForeignID: p.ForeignID, ItemType: p.ItemType, // Radarr proposals carry "" here and Add ignores them
		QualityProfileID: p.QualityProfileID, RootFolderPath: p.RootFolderPath, Monitored: true,
	})
	if err != nil {
		return 0, fmt.Errorf("registering surviving copy %q: %w", p.Title, err)
	}
	if err := sess.Servarr.ScanForDownloaded(ctx); err != nil {
		return id, fmt.Errorf("registered as id=%d but triggering the downloaded-files scan failed: %w", id, err)
	}
	return id, nil
}

func winnerIndex(candidates []proposals.Candidate) int {
	for i, c := range candidates {
		if c.Winner {
			return i
		}
	}
	return 0
}

func removeCandidate(ctx context.Context, sess *mode.Session, c proposals.Candidate) error {
	if c.TrackedID != 0 {
		return sess.Servarr.DeleteTracked(ctx, c.TrackedID)
	}
	return os.Remove(c.Path)
}

// ScanLibrary is Dedup's Movies-library counterpart to scanMovies — used
// only for Movies mode now that Radarr no longer sits between SAK and the
// filesystem/TMDB (see internal/library's package doc). Identifies every
// unmapped file (via TMDB search instead of Servarr's Lookup) and groups it,
// and any already-tracked library item, by TMDB id — the same shape
// scanMovies already established, just reading/writing internal/library
// instead of Servarr.
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

// ApplyLibrary is Dedup's Movies-library counterpart to Apply — resolves p
// against libStore instead of Servarr: a tracked loser's file is removed
// and its library record deleted, an untracked orphan loser's file is
// removed directly, and an untracked winner is recorded via libStore.Upsert
// (no registration/rescan round trip needed — Upsert itself IS the "now
// tracked" state).
//
// changes accumulates one Deleted PathChange per removed loser (the winner
// never moves, so it never appears in changes) — a named return so a
// post-removal failure (the winner's libStore.Upsert) still reports every
// loser that was actually removed to the caller for Session.NotifyPlayers.
// keepAll never removes anything, so it always returns nil changes.
func ApplyLibrary(ctx context.Context, libStore *library.Store, p proposals.Proposal, keepIndex *int, keepAll bool) (itemID int64, changes []mode.PathChange, err error) {
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
		removedPath, err := removeLibraryCandidate(ctx, libStore, c)
		if err != nil {
			return 0, changes, fmt.Errorf("removing %s: %w", c.Path, err)
		}
		if removedPath != "" {
			changes = append(changes, mode.PathChange{Path: removedPath, Kind: mode.Deleted})
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
		return "", err
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
// simply overwritten by the winner's file path via UpsertEpisode — there's
// nothing else that could ever point at that exact slot.
//
// changes accumulates one Deleted PathChange per removed loser (the winner
// never moves, so it never appears in changes) — a named return so a
// post-removal failure further down still reports every loser that was
// actually removed to the caller for Session.NotifyPlayers. keepAll never
// removes anything, so it always returns nil changes.
func ApplyLibrarySeries(ctx context.Context, libStore *library.Store, p proposals.Proposal, keepIndex *int, keepAll bool) (episodeID int64, changes []mode.PathChange, err error) {
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
		if err := os.Remove(c.Path); err != nil && !os.IsNotExist(err) {
			return 0, changes, fmt.Errorf("removing %s: %w", c.Path, err)
		}
		if c.Path != "" {
			changes = append(changes, mode.PathChange{Path: c.Path, Kind: mode.Deleted})
		}
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
