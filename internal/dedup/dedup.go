// Package dedup implements SAK's Dedup workflow for Movies: find
// content that's been identified twice — once as an already-tracked item,
// once (or more) as an orphaned file that resolves to the same TMDB ID — and
// stage a proposal to keep the better-quality copy instead of leaving both
// silently in place (today's behavior in both source CLIs).
//
// Series (Sonarr) isn't implemented yet: Sonarr's per-episode file model
// means "a duplicate" doesn't reduce to "two candidate files for one
// tracked thing" the way it does for a movie — a real design needs to name
// which episode(s) collide, which is a meaningfully different shape from
// what's built here. Scan refuses Series sessions with a clear error rather
// than silently doing the wrong thing.
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
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/curtiswtaylorjr/sak/internal/config"
	"github.com/curtiswtaylorjr/sak/internal/identify"
	"github.com/curtiswtaylorjr/sak/internal/mediainfo"
	"github.com/curtiswtaylorjr/sak/internal/mode"
	"github.com/curtiswtaylorjr/sak/internal/place"
	"github.com/curtiswtaylorjr/sak/internal/proposals"
	"github.com/curtiswtaylorjr/sak/internal/searchterm"
	"github.com/curtiswtaylorjr/sak/internal/servarr"
)

// Prober is the subset of *mediainfo.Prober Scan needs — an interface so
// tests can inject a fake without a real ffprobe binary or media file.
type Prober interface {
	Probe(ctx context.Context, path string) (*mediainfo.Probe, error)
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
// (scanAdult, keyed by foreignID); Sonarr and anything else is refused, since
// Series' per-episode file model is a different shape (see the package doc).
func Scan(ctx context.Context, sess *mode.Session, prober Prober) ([]proposals.Proposal, error) {
	switch sess.Servarr.AppType() {
	case servarr.Radarr:
		return scanMovies(ctx, sess, prober)
	case servarr.Whisparr:
		if sess.Identify == nil {
			return nil, fmt.Errorf("adult identification isn't configured — add an Ollama connection and set the Ollama model in Settings, plus at least one of StashDB/FansDB/TPDB")
		}
		return scanAdult(ctx, sess, prober)
	default:
		return nil, fmt.Errorf("dedup: only Movies and Adult are implemented so far, not %v", sess.Mode)
	}
}

// scanMovies identifies every unmapped file and groups it (and any
// already-tracked item) by resolved TMDB ID. A group with 2+ probeable
// candidates becomes a Pending Dedup proposal; a lone new item is left for
// Rename to handle, not reported here.
func scanMovies(ctx context.Context, sess *mode.Session, prober Prober) ([]proposals.Proposal, error) {
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
// logs directly (only cmd/sak/main.go does).
func scanAdult(ctx context.Context, sess *mode.Session, prober Prober) ([]proposals.Proposal, error) {
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
