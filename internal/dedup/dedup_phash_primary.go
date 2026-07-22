// Package dedup — phash-primary scan (Movies + Series).
//
// ScanLibraryPHash and ScanLibrarySeriesPHash group ALL files — tracked items
// and orphans alike — by perceptual similarity using an all-pairs O(n²)
// comparison and union-find connected components. TMDB is used only for
// display labels; it never determines whether files are grouped. This catches
// three cases the legacy TMDB-keyed scan misses:
//
//  1. Orphan-vs-orphan: two copies of the same film, both untracked, different
//     filenames — TMDB searches may diverge, but phash always matches.
//  2. Cross-ID mis-assignment: both tracked, but one resolved to the wrong TMDB
//     ID — different TMDB buckets, but phash sees identical content.
//  3. Named-vs-unnamed: one file is tracked, the other's filename is too
//     generic for a TMDB match — the orphan was never reachable before.
//
// Apply path is unchanged: ApplyLibrary / ApplyLibrarySeries handle the
// resulting proposals as before.

package dedup

import (
	"context"
	"fmt"
	"math"
	"path/filepath"
	"strings"

	"github.com/labbersanon/sakms/internal/config"
	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/phash"
	"github.com/labbersanon/sakms/internal/proposals"
	"github.com/labbersanon/sakms/internal/searchterm"
)

// pHashFileItem holds one candidate (tracked or orphan) for the phash-primary
// all-pairs comparison pass. Both kinds of file go through this representation
// before the union-find grouping step.
type pHashFileItem struct {
	path      string
	label     string // filename basename — display fallback when TMDB search fails
	trackedID int    // 0 for orphans
	tmdbID    int    // from tracked identity or TMDB search; 0 if unknown
	title     string // TMDB-resolved title; "" if unavailable
	season    int    // Series only — from library.ParseEpisodeFilename
	episode   int    // Series only
	phashVal  string // "" means computation failed; item skipped during comparison
}

// pHashUnionFind is a path-compressed union-find for connected-component
// grouping during the all-pairs pass.
type pHashUnionFind struct {
	parent []int
}

func newPHashUnionFind(n int) *pHashUnionFind {
	uf := &pHashUnionFind{parent: make([]int, n)}
	for i := range uf.parent {
		uf.parent[i] = i
	}
	return uf
}

func (uf *pHashUnionFind) find(x int) int {
	if uf.parent[x] != x {
		uf.parent[x] = uf.find(uf.parent[x])
	}
	return uf.parent[x]
}

func (uf *pHashUnionFind) union(x, y int) {
	uf.parent[uf.find(x)] = uf.find(y)
}

// pHashGroupComponents returns all connected components with ≥ 2 members.
// Union-find produces transitive clusters: if A~B and B~C, {A,B,C} are grouped
// even when A and C don't meet the threshold directly. This is intentional —
// minPairwiseSimilarity surfaces the worst-case pair similarity so the operator
// can review before any delete. To get strict pairwise grouping instead, re-check
// every member against the chosen winner and drop those beyond threshold.
func pHashGroupComponents(items []pHashFileItem, uf *pHashUnionFind) [][]pHashFileItem {
	byRoot := make(map[int][]pHashFileItem)
	for i, item := range items {
		root := uf.find(i)
		byRoot[root] = append(byRoot[root], item)
	}
	var out [][]pHashFileItem
	for _, g := range byRoot {
		if len(g) >= 2 {
			out = append(out, g)
		}
	}
	return out
}

// minPairwiseSimilarity returns the minimum phash similarity across all pairs
// in the group as a value in [0.0, 1.0]. Returns 0.0 when fewer than two
// members have a valid hash (the zero-value sentinel on Proposal.PHashSimilarity).
func minPairwiseSimilarity(group []pHashFileItem, frames int) float64 {
	min := math.MaxFloat64
	found := false
	for i := 0; i < len(group); i++ {
		if group[i].phashVal == "" {
			continue
		}
		for j := i + 1; j < len(group); j++ {
			if group[j].phashVal == "" {
				continue
			}
			s, err := phash.SimilarityScore(group[i].phashVal, group[j].phashVal, frames)
			if err != nil {
				continue
			}
			found = true
			if s < min {
				min = s
			}
		}
	}
	if !found {
		return 0.0
	}
	return min
}

// ScanLibraryPHash is Dedup's phash-primary scan for Movies mode. Unlike the
// legacy ScanLibrary (TMDB-keyed), this scan groups all files by perceptual
// similarity. TMDB is consulted only for display labels; it never gates
// whether two files are considered duplicates.
//
// perFrameThreshold is the Movies per-frame Hamming distance ceiling — default
// 25 bits (~60% similarity), configurable via movies_phash_dedup_threshold.
func ScanLibraryPHash(ctx context.Context, sess *mode.Session, libStore *library.Store, rootFolderPath string, prober Prober, hasher PHasher, perFrameThreshold int, onProgress ProgressFunc) ([]proposals.Proposal, error) {
	if rootFolderPath == "" {
		return nil, fmt.Errorf("no Movies library root folder configured yet — add one in Settings first")
	}

	tracked, err := libStore.List(ctx, mode.Movies)
	if err != nil {
		return nil, fmt.Errorf("loading library items: %w", err)
	}

	known := make(map[string]bool, len(tracked))
	for _, t := range tracked {
		known[t.FilePath] = true
	}

	entries, err := library.ScanRootFolder(rootFolderPath, known)
	if err != nil {
		return nil, fmt.Errorf("scanning %s: %w", rootFolderPath, err)
	}

	// Progress unit: files whose analyze (hash) step has completed. Total is an
	// upper bound — len(tracked)+len(entries); Movies orphans are one file each,
	// so a skipped sidecar/unprobeable entry can only make Current fall short of
	// Total, never exceed it. The done event carries the authoritative final
	// count (see the handler), so a short live Total is corrected on completion.
	total := len(tracked) + len(entries)
	current := 0

	// Build the flat candidate list: tracked items then orphan entries.
	var items []pHashFileItem
	for i := range tracked {
		t := &tracked[i]
		h := loadOrComputeTrackedItemPHash(ctx, hasher, libStore, t)
		current++
		if onProgress != nil {
			onProgress(ProgressEvent{Current: current, Total: total, Name: filepath.Base(t.FilePath), Phase: "hashing"})
		}
		items = append(items, pHashFileItem{
			path:      t.FilePath,
			label:     filepath.Base(t.FilePath),
			trackedID: int(t.ID),
			tmdbID:    t.TMDBID,
			title:     t.Title,
			phashVal:  h,
		})
	}

	var orphanPaths []string
	for _, entry := range entries {
		if config.SidecarExts[strings.ToLower(filepath.Ext(entry.Name))] {
			continue
		}
		videoPath, err := findVideoFile(entry.Path)
		if err != nil {
			continue
		}
		orphanPaths = append(orphanPaths, videoPath)
		h := libStore.LoadOrComputeOrphanPHash(ctx, hasher, videoPath)
		current++
		if onProgress != nil {
			onProgress(ProgressEvent{Current: current, Total: total, Name: entry.Name, Phase: "hashing"})
		}
		var tmdbID int
		var title string
		if sess.TMDB != nil {
			if results, sErr := sess.TMDB.SearchMovies(ctx, searchterm.FromName(entry.Name)); sErr == nil && len(results) > 0 {
				tmdbID = results[0].ID
				title = results[0].Title
			}
		}
		items = append(items, pHashFileItem{
			path:     videoPath,
			label:    entry.Name,
			tmdbID:   tmdbID,
			title:    title,
			phashVal: h,
		})
	}

	// Remove orphan_phashes rows for files no longer present in this scan.
	_ = libStore.DeleteOrphanPHashesNotIn(ctx, orphanPaths)

	// All-pairs phash comparison → union-find connected components.
	uf := newPHashUnionFind(len(items))
	for i := 0; i < len(items); i++ {
		if items[i].phashVal == "" {
			continue
		}
		for j := i + 1; j < len(items); j++ {
			if items[j].phashVal == "" {
				continue
			}
			within, err := phash.SimilarityWithin(items[i].phashVal, items[j].phashVal, phash.Frames, perFrameThreshold)
			if err == nil && within {
				uf.union(i, j)
			}
		}
	}

	groups := pHashGroupComponents(items, uf)

	var out []proposals.Proposal
	for _, group := range groups {
		similarity := minPairwiseSimilarity(group, phash.Frames)

		// Title/TMDB ID: prefer a tracked item's resolved identity, then the
		// best orphan TMDB search result, then the first filename as fallback.
		title, tmdbID, rootPath := pHashGroupLabel(group)

		// Prefer the tracked item's own root folder path over the derived one.
		for i := range tracked {
			for _, c := range group {
				if c.trackedID == int(tracked[i].ID) && tracked[i].RootFolderPath != "" {
					rootPath = tracked[i].RootFolderPath
				}
			}
		}

		candidates := pHashBuildCandidates(ctx, prober, group)
		if len(candidates) < 2 {
			continue
		}
		markWinner(candidates)

		out = append(out, proposals.Proposal{
			Mode: mode.Movies, Workflow: proposals.Dedup, Status: proposals.Pending,
			SourceName: title, Title: title, TMDBID: tmdbID, RootFolderPath: rootPath,
			Candidates:      candidates,
			PHashSimilarity: similarity,
			Reason:          fmt.Sprintf("%d copies found to be perceptually similar (%.0f%% similar)", len(candidates), similarity*100),
		})
	}
	return out, nil
}

// ScanLibrarySeriesPHash is Dedup's phash-primary scan for Series mode — the
// Movies sibling with two differences: (1) it loads tracked episodes (not items)
// and resolves orphan entries through library.ResolveEpisodeVideoFiles, and
// (2) it uses a stricter default threshold (40 Hamming bits/frame, same as
// phash.DefaultThreshold) to reduce false positives from shared intros/credits
// between genuinely different episodes of the same show.
//
// perFrameThreshold is configurable via series_phash_dedup_threshold.
func ScanLibrarySeriesPHash(ctx context.Context, sess *mode.Session, libStore *library.Store, rootFolderPath string, prober Prober, hasher PHasher, perFrameThreshold int, onProgress ProgressFunc) ([]proposals.Proposal, error) {
	if rootFolderPath == "" {
		return nil, fmt.Errorf("no Series library root folder configured yet — add one in Settings first")
	}

	allSeries, err := libStore.ListSeries(ctx)
	if err != nil {
		return nil, fmt.Errorf("loading series: %w", err)
	}

	seriesByID := make(map[int64]library.Series, len(allSeries))
	known := map[string]bool{}
	var trackedEpisodes []library.Episode
	for _, s := range allSeries {
		seriesByID[s.ID] = s
		episodes, epErr := libStore.ListEpisodes(ctx, s.ID)
		if epErr != nil {
			return nil, fmt.Errorf("loading episodes for %q: %w", s.Title, epErr)
		}
		for _, ep := range episodes {
			if ep.FilePath == "" {
				continue
			}
			known[ep.FilePath] = true
			trackedEpisodes = append(trackedEpisodes, ep)
		}
	}

	entries, err := library.ScanRootFolder(rootFolderPath, known)
	if err != nil {
		return nil, fmt.Errorf("scanning %s: %w", rootFolderPath, err)
	}

	// Pre-resolve every orphan entry into ONE flat list of video paths BEFORE
	// emitting any progress, so the denominator counts video files — the exact
	// unit the emitting loops iterate. This is the fix for the >100% bug: a
	// single season-pack entry expands to several files via
	// ResolveEpisodeVideoFiles, so len(trackedEpisodes)+len(entries) is NOT a
	// valid denominator (Current, which counts video files, could exceed it).
	// ScanRootFolder-order + ResolveEpisodeVideoFiles-order are preserved, so
	// orphanPaths is byte-identical to what the old inline loop produced; the
	// item-building loop below consumes this same slice (no double resolve), and
	// DeleteOrphanPHashesNotIn is fed from it unchanged.
	var orphanPaths []string
	for _, entry := range entries {
		if config.SidecarExts[strings.ToLower(filepath.Ext(entry.Name))] {
			continue
		}
		videoFiles, err := library.ResolveEpisodeVideoFiles(entry.Path)
		if err != nil {
			continue
		}
		orphanPaths = append(orphanPaths, videoFiles...)
	}

	total := len(trackedEpisodes) + len(orphanPaths)
	current := 0

	var items []pHashFileItem
	for i := range trackedEpisodes {
		ep := &trackedEpisodes[i]
		h := loadOrComputeTrackedEpisodePHash(ctx, hasher, libStore, ep)
		current++
		if onProgress != nil {
			onProgress(ProgressEvent{Current: current, Total: total, Name: filepath.Base(ep.FilePath), Phase: "hashing"})
		}
		seriesTitle := ""
		seriesTMDBID := 0
		if s, ok := seriesByID[ep.SeriesID]; ok {
			seriesTitle = s.Title
			seriesTMDBID = s.TMDBID
		}
		items = append(items, pHashFileItem{
			path:      ep.FilePath,
			label:     filepath.Base(ep.FilePath),
			trackedID: int(ep.ID),
			tmdbID:    seriesTMDBID,
			season:    ep.SeasonNumber,
			episode:   ep.EpisodeNumber,
			title:     seriesTitle,
			phashVal:  h,
		})
	}

	for _, videoPath := range orphanPaths {
		name := filepath.Base(videoPath)
		h := libStore.LoadOrComputeOrphanPHash(ctx, hasher, videoPath)
		current++
		if onProgress != nil {
			onProgress(ProgressEvent{Current: current, Total: total, Name: name, Phase: "hashing"})
		}
		season, episode, _ := library.ParseEpisodeFilename(name)
		var tmdbID int
		var title string
		if sess.TMDB != nil {
			if results, sErr := sess.TMDB.SearchTV(ctx, searchterm.FromName(library.StripEpisodeMarker(name))); sErr == nil && len(results) > 0 {
				tmdbID = results[0].ID
				title = results[0].Title
			}
		}
		items = append(items, pHashFileItem{
			path:     videoPath,
			label:    name,
			tmdbID:   tmdbID,
			season:   season,
			episode:  episode,
			title:    title,
			phashVal: h,
		})
	}

	_ = libStore.DeleteOrphanPHashesNotIn(ctx, orphanPaths)

	uf := newPHashUnionFind(len(items))
	for i := 0; i < len(items); i++ {
		if items[i].phashVal == "" {
			continue
		}
		for j := i + 1; j < len(items); j++ {
			if items[j].phashVal == "" {
				continue
			}
			within, err := phash.SimilarityWithin(items[i].phashVal, items[j].phashVal, phash.Frames, perFrameThreshold)
			if err == nil && within {
				uf.union(i, j)
			}
		}
	}

	groups := pHashGroupComponents(items, uf)

	var out []proposals.Proposal
	for _, group := range groups {
		similarity := minPairwiseSimilarity(group, phash.Frames)

		title, tmdbID, rootPath := pHashGroupLabel(group)

		// Season/episode: prefer the tracked episode's values; fall back to
		// the first orphan with a parseable filename.
		season, episode := 0, 0
		for _, item := range group {
			if item.season != 0 || item.episode != 0 {
				season = item.season
				episode = item.episode
				break
			}
		}

		// Prefer the tracked episode's series root folder path.
		for i := range trackedEpisodes {
			for _, c := range group {
				if c.trackedID == int(trackedEpisodes[i].ID) {
					if s, ok := seriesByID[trackedEpisodes[i].SeriesID]; ok && s.RootFolderPath != "" {
						rootPath = s.RootFolderPath
					}
				}
			}
		}

		candidates := pHashBuildCandidates(ctx, prober, group)
		if len(candidates) < 2 {
			continue
		}
		markWinner(candidates)

		label := fmt.Sprintf("%s S%02dE%02d", title, season, episode)
		out = append(out, proposals.Proposal{
			Mode: mode.Series, Workflow: proposals.Dedup, Status: proposals.Pending,
			SourceName: label, Title: title, TMDBID: tmdbID, SeasonNumber: season, EpisodeNumber: episode,
			RootFolderPath:  rootPath,
			Candidates:      candidates,
			PHashSimilarity: similarity,
			Reason:          fmt.Sprintf("%d copies found to be perceptually similar (%.0f%% similar)", len(candidates), similarity*100),
		})
	}
	return out, nil
}

// pHashGroupLabel returns the best title, TMDB ID, and root folder path for a
// phash-primary duplicate group: a tracked item's identity wins over any orphan
// TMDB search result, which wins over the first candidate's filename.
func pHashGroupLabel(group []pHashFileItem) (title string, tmdbID int, rootPath string) {
	for _, item := range group {
		if item.trackedID != 0 && item.title != "" {
			title = item.title
			tmdbID = item.tmdbID
			rootPath = filepath.Dir(item.path)
			return
		}
	}
	for _, item := range group {
		if item.title != "" {
			title = item.title
			tmdbID = item.tmdbID
			rootPath = filepath.Dir(item.path)
			return
		}
	}
	title = group[0].label
	rootPath = filepath.Dir(group[0].path)
	return
}

// pHashBuildCandidates ffprobes each item in a phash group, returning only
// those that could be measured (same tolerant posture as probeCandidate in
// the legacy scan path). The PHash field on each returned Candidate is set
// from the item's already-computed phashVal.
func pHashBuildCandidates(ctx context.Context, prober Prober, group []pHashFileItem) []proposals.Candidate {
	var out []proposals.Candidate
	for _, item := range group {
		label := item.title
		if label == "" {
			label = item.label
		}
		c := probeCandidate(ctx, prober, label, item.path, item.trackedID)
		if c == nil {
			continue
		}
		c.PHash = item.phashVal
		out = append(out, *c)
	}
	return out
}

// loadOrComputeTrackedItemPHash returns a valid cached phash for a tracked
// library.Item (reusing the stored hash when size+mtime match) or computes
// and caches a fresh one. Returns "" on any failure — same tolerance as
// attachPHashes in the legacy scan path.
func loadOrComputeTrackedItemPHash(ctx context.Context, hasher PHasher, libStore *library.Store, item *library.Item) string {
	if item.PHash != "" && strings.HasPrefix(item.PHash, phash.Scheme+":") {
		if size, mtime, err := fileIdentity(item.FilePath); err == nil &&
			size == item.PHashFileSize && mtime == item.PHashFileMTime {
			return item.PHash
		}
	}
	h, err := hasher.Hash(ctx, item.FilePath)
	if err != nil {
		return ""
	}
	if size, mtime, err := fileIdentity(item.FilePath); err == nil {
		_ = libStore.UpdatePHash(ctx, item.ID, h, size, mtime)
	}
	return h
}

// loadOrComputeTrackedEpisodePHash is loadOrComputeTrackedItemPHash's
// Series-typed sibling — identical body operating on *library.Episode and
// UpdateEpisodePHash.
func loadOrComputeTrackedEpisodePHash(ctx context.Context, hasher PHasher, libStore *library.Store, ep *library.Episode) string {
	if ep.PHash != "" && strings.HasPrefix(ep.PHash, phash.Scheme+":") {
		if size, mtime, err := fileIdentity(ep.FilePath); err == nil &&
			size == ep.PHashFileSize && mtime == ep.PHashFileMTime {
			return ep.PHash
		}
	}
	h, err := hasher.Hash(ctx, ep.FilePath)
	if err != nil {
		return ""
	}
	if size, mtime, err := fileIdentity(ep.FilePath); err == nil {
		_ = libStore.UpdateEpisodePHash(ctx, ep.ID, h, size, mtime)
	}
	return h
}
