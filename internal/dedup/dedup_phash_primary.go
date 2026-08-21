// Package dedup — phash-primary scan (Movies + Series).
//
// ScanLibraryPHash and ScanLibrarySeriesPHash group ALL video files —
// tracked primaries, extra copies recorded on the library row, and orphans —
// independently of Rename. Two files group when they are perceptually similar
// OR they share a TMDB identity (Movies: same TMDB id; Series: same TMDB id
// plus the same season/episode). TMDB search is still used for unlabeled
// orphans; a [tmdbid-N] path tag is enough on its own.
//
// Phash still catches the cases identity cannot: orphan-vs-orphan with no
// id, and two tracked rows that resolved to the wrong TMDB ids.
//
// ApplyLibrary / ApplyLibrarySeries delete only the losing candidate's own
// file — removing an extra copy never deletes the title it belongs to.

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
	"github.com/labbersanon/sakms/internal/naming"
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

func sameMovieIdentity(a, b pHashFileItem) bool {
	return a.tmdbID != 0 && a.tmdbID == b.tmdbID
}

// sameEpisodeIdentity requires a real episode slot, not a failed filename
// parse (0, 0). Series TMDB id alone would merge every episode of a show.
func sameEpisodeIdentity(a, b pHashFileItem) bool {
	if a.tmdbID == 0 || a.tmdbID != b.tmdbID {
		return false
	}
	if a.season == 0 && a.episode == 0 {
		return false
	}
	return a.season == b.season && a.episode == b.episode
}

// phashWithin reports whether a and b both hashed successfully and are within
// perFrameThreshold of each other. A missing hash never groups.
func phashWithin(a, b pHashFileItem, perFrameThreshold int) bool {
	if a.phashVal == "" || b.phashVal == "" {
		return false
	}
	within, err := phash.SimilarityWithin(a.phashVal, b.phashVal, phash.Frames, perFrameThreshold)
	return err == nil && within
}

// anyPair reports whether match holds for at least one pair in items.
func anyPair(items []pHashFileItem, match func(a, b pHashFileItem) bool) bool {
	for i := 0; i < len(items); i++ {
		for j := i + 1; j < len(items); j++ {
			if match(items[i], items[j]) {
				return true
			}
		}
	}
	return false
}

// unionByPHashAndIdentity groups every pair that either shares an identity
// (sameID) or is perceptually within perFrameThreshold. A nil sameID groups by
// perceptual similarity alone.
func unionByPHashAndIdentity(items []pHashFileItem, perFrameThreshold int, sameID func(a, b pHashFileItem) bool) *pHashUnionFind {
	uf := newPHashUnionFind(len(items))
	for i := 0; i < len(items); i++ {
		for j := i + 1; j < len(items); j++ {
			if (sameID != nil && sameID(items[i], items[j])) || phashWithin(items[i], items[j], perFrameThreshold) {
				uf.union(i, j)
			}
		}
	}
	return uf
}

// pHashGroupReason describes why a group was formed, so the operator can tell
// a shared-identity grouping from a perceptual one before deleting anything.
func pHashGroupReason(group []pHashFileItem, n int, similarity float64, perFrameThreshold int, sameID func(a, b pHashFileItem) bool) string {
	byID := sameID != nil && anyPair(group, sameID)
	byHash := anyPair(group, func(a, b pHashFileItem) bool {
		return phashWithin(a, b, perFrameThreshold)
	})
	switch {
	case byID && byHash:
		return fmt.Sprintf("%d copies share TMDB identity and are perceptually similar (%.0f%% similar)", n, similarity*100)
	case byID:
		return fmt.Sprintf("%d copies share the same TMDB identity", n)
	default:
		return fmt.Sprintf("%d copies found to be perceptually similar (%.0f%% similar)", n, similarity*100)
	}
}

func orphanMovieTMDB(ctx context.Context, sess *mode.Session, path, name string) (tmdbID int, title string) {
	if id := naming.TMDBIDFromPath(path); id != 0 {
		return id, ""
	}
	if sess != nil && sess.TMDB != nil {
		if results, sErr := sess.TMDB.SearchMovies(ctx, searchterm.FromName(name)); sErr == nil && len(results) > 0 {
			return results[0].ID, results[0].Title
		}
	}
	return 0, ""
}

func orphanSeriesTMDB(ctx context.Context, sess *mode.Session, path, name string) (tmdbID int, title string) {
	if id := naming.TMDBIDFromPath(path); id != 0 {
		return id, ""
	}
	if sess != nil && sess.TMDB != nil {
		if results, sErr := sess.TMDB.SearchTV(ctx, searchterm.FromName(library.StripEpisodeMarker(name))); sErr == nil && len(results) > 0 {
			return results[0].ID, results[0].Title
		}
	}
	return 0, ""
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

// ScanLibraryPHash is Dedup's Movies scan. Files group by perceptual
// similarity or by shared TMDB identity, taken from a [tmdbid-N] path tag, the
// library row, or a TMDB search. Extra copies on library_item_files are
// candidates too — Rename having folded them in does not exempt them.
//
// perFrameThreshold is the Movies per-frame Hamming distance ceiling —
// default 64 bits (of 256 per frame, PDQ scale — see phash.DefaultMoviesThreshold's
// doc comment for the measured calibration this was chosen against),
// configurable via movies_phash_dedup_threshold.
func ScanLibraryPHash(ctx context.Context, sess *mode.Session, libStore *library.Store, rootFolderPath string, prober Prober, hasher PHasher, perFrameThreshold int, onProgress ProgressFunc) ([]proposals.Proposal, error) {
	if rootFolderPath == "" {
		return nil, fmt.Errorf("no Movies library root folder configured yet — add one in Settings first")
	}

	tracked, err := libStore.List(ctx, mode.Movies)
	if err != nil {
		return nil, fmt.Errorf("loading library items: %w", err)
	}

	// known doubles as this pass's seen-path set: a path already claimed by an
	// earlier row (a shared file, or the same path listed twice) is one
	// candidate, not a self-duplicate group.
	var items []pHashFileItem
	known := map[string]bool{}
	for i := range tracked {
		t := &tracked[i]
		for _, path := range movieTrackedPaths(ctx, libStore, t) {
			if known[path] {
				continue
			}
			known[path] = true
			items = append(items, pHashFileItem{
				path:      path,
				label:     filepath.Base(path),
				trackedID: int(t.ID),
				tmdbID:    t.TMDBID,
				title:     t.Title,
				phashVal:  hashTrackedMoviePath(ctx, hasher, libStore, t, path),
			})
		}
	}

	entries, err := library.ScanRootFolder(rootFolderPath, known)
	if err != nil {
		return nil, fmt.Errorf("scanning %s: %w", rootFolderPath, err)
	}

	type movieOrphan struct{ name, path string }
	var orphans []movieOrphan
	for _, entry := range entries {
		if config.SidecarExts[strings.ToLower(filepath.Ext(entry.Name))] {
			continue
		}
		videoPath, err := findVideoFile(entry.Path)
		if err != nil {
			continue
		}
		orphans = append(orphans, movieOrphan{name: entry.Name, path: videoPath})
	}

	// Progress unit: files whose hash step has completed. The tracked files
	// above are already hashed by the time this reports them.
	total := len(items) + len(orphans)
	current := 0
	for _, item := range items {
		current++
		if onProgress != nil {
			onProgress(ProgressEvent{Current: current, Total: total, Name: item.label, Phase: "hashing"})
		}
	}

	for _, o := range orphans {
		h := libStore.LoadOrComputeOrphanPHash(ctx, hasher, o.path)
		current++
		if onProgress != nil {
			onProgress(ProgressEvent{Current: current, Total: total, Name: o.name, Phase: "hashing"})
		}
		tmdbID, title := orphanMovieTMDB(ctx, sess, o.path, o.name)
		items = append(items, pHashFileItem{
			path:     o.path,
			label:    o.name,
			tmdbID:   tmdbID,
			title:    title,
			phashVal: h,
		})
	}

	// Every path this scan saw keeps its cached hash — a library extra hashes
	// through the orphan cache too (hashTrackedMoviePath), so pruning it here
	// would force a re-decode on the next scan.
	keepCached := make([]string, 0, len(orphans)+len(known))
	for _, o := range orphans {
		keepCached = append(keepCached, o.path)
	}
	for p := range known {
		keepCached = append(keepCached, p)
	}
	_ = libStore.DeleteOrphanPHashesNotIn(ctx, keepCached)

	uf := unionByPHashAndIdentity(items, perFrameThreshold, sameMovieIdentity)
	groups := pHashGroupComponents(items, uf)

	var out []proposals.Proposal
	for _, group := range groups {
		similarity := minPairwiseSimilarity(group, phash.Frames)
		title, tmdbID, rootPath := pHashGroupLabel(group)
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
			Reason:          pHashGroupReason(group, len(candidates), similarity, perFrameThreshold, sameMovieIdentity),
		})
	}
	return out, nil
}

// ScanLibrarySeriesPHash is Dedup's phash-primary scan for Series mode — the
// Movies sibling with two differences: (1) it loads tracked episodes (not items)
// and resolves orphan entries through library.ResolveEpisodeVideoFiles, and
// (2) it uses a stricter default threshold (40 Hamming bits/frame, of 256 per
// frame — same PDQ scale as phash.DefaultThreshold) to reduce false positives
// from shared intros/credits between genuinely different episodes of the same
// show.
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

	type trackedEpFile struct {
		ep   library.Episode
		path string
	}
	seriesByID := make(map[int64]library.Series, len(allSeries))
	// known doubles as this pass's seen-path set: a logical-episode-split file
	// (S01E01-E02) backs two library_episodes rows at the same path, and must
	// stay one candidate rather than becoming a self-duplicate group.
	known := map[string]bool{}
	var trackedFiles []trackedEpFile
	for _, s := range allSeries {
		seriesByID[s.ID] = s
		episodes, epErr := libStore.ListEpisodes(ctx, s.ID)
		if epErr != nil {
			return nil, fmt.Errorf("loading episodes for %q: %w", s.Title, epErr)
		}
		for _, ep := range episodes {
			for _, path := range episodeTrackedPaths(ctx, libStore, ep) {
				if known[path] {
					continue
				}
				known[path] = true
				trackedFiles = append(trackedFiles, trackedEpFile{ep: ep, path: path})
			}
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
	// ResolveEpisodeVideoFiles, so len(trackedFiles)+len(entries) is NOT a
	// valid denominator (Current, which counts video files, could exceed it).
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

	total := len(trackedFiles) + len(orphanPaths)
	current := 0

	var items []pHashFileItem
	for i := range trackedFiles {
		tf := &trackedFiles[i]
		ep := &tf.ep
		h := hashTrackedEpisodePath(ctx, hasher, libStore, ep, tf.path)
		current++
		if onProgress != nil {
			onProgress(ProgressEvent{Current: current, Total: total, Name: filepath.Base(tf.path), Phase: "hashing"})
		}
		seriesTitle := ""
		seriesTMDBID := 0
		if s, ok := seriesByID[ep.SeriesID]; ok {
			seriesTitle = s.Title
			seriesTMDBID = s.TMDBID
		}
		items = append(items, pHashFileItem{
			path:      tf.path,
			label:     filepath.Base(tf.path),
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
		tmdbID, title := orphanSeriesTMDB(ctx, sess, videoPath, name)
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

	// Every path this scan saw keeps its cached hash — see the Movies sibling.
	keepCached := make([]string, 0, len(orphanPaths)+len(known))
	keepCached = append(keepCached, orphanPaths...)
	for p := range known {
		keepCached = append(keepCached, p)
	}
	_ = libStore.DeleteOrphanPHashesNotIn(ctx, keepCached)

	uf := unionByPHashAndIdentity(items, perFrameThreshold, sameEpisodeIdentity)
	groups := pHashGroupComponents(items, uf)

	var out []proposals.Proposal
	for _, group := range groups {
		similarity := minPairwiseSimilarity(group, phash.Frames)
		title, tmdbID, rootPath := pHashGroupLabel(group)
		season, episode := 0, 0
		for _, item := range group {
			if item.season != 0 || item.episode != 0 {
				season = item.season
				episode = item.episode
				break
			}
		}
		for i := range trackedFiles {
			for _, c := range group {
				if c.trackedID == int(trackedFiles[i].ep.ID) {
					if s, ok := seriesByID[trackedFiles[i].ep.SeriesID]; ok && s.RootFolderPath != "" {
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
			Reason:          pHashGroupReason(group, len(candidates), similarity, perFrameThreshold, sameEpisodeIdentity),
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
		if item.tmdbID != 0 {
			title = item.title
			if title == "" {
				title = item.label
			}
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
	seenPath := make(map[string]struct{}, len(group))
	var out []proposals.Candidate
	for _, item := range group {
		if _, dup := seenPath[item.path]; dup {
			continue
		}
		seenPath[item.path] = struct{}{}
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

// movieTrackedPaths returns every on-disk copy of a tracked title: its
// denormalized primary plus its library_item_files rows.
func movieTrackedPaths(ctx context.Context, libStore *library.Store, t *library.Item) []string {
	var extras []string
	if files, err := libStore.ListFiles(ctx, t.ID); err == nil {
		for _, f := range files {
			extras = append(extras, f.FilePath)
		}
	}
	return pathsWithExtras(t.FilePath, extras)
}

// episodeTrackedPaths is movieTrackedPaths' Series sibling, over
// library_episode_files.
func episodeTrackedPaths(ctx context.Context, libStore *library.Store, ep library.Episode) []string {
	var extras []string
	if files, err := libStore.ListEpisodeFiles(ctx, ep.ID); err == nil {
		for _, f := range files {
			extras = append(extras, f.FilePath)
		}
	}
	return pathsWithExtras(ep.FilePath, extras)
}

// hashTrackedMoviePath hashes one copy of a tracked title. Only the primary
// has a phash column on its own row; an extra copy caches through the orphan
// phash table instead.
func hashTrackedMoviePath(ctx context.Context, hasher PHasher, libStore *library.Store, item *library.Item, path string) string {
	if path == item.FilePath {
		return loadOrComputeTrackedItemPHash(ctx, hasher, libStore, item)
	}
	return libStore.LoadOrComputeOrphanPHash(ctx, hasher, path)
}

// hashTrackedEpisodePath is hashTrackedMoviePath's Series sibling.
func hashTrackedEpisodePath(ctx context.Context, hasher PHasher, libStore *library.Store, ep *library.Episode, path string) string {
	if path == ep.FilePath {
		return loadOrComputeTrackedEpisodePHash(ctx, hasher, libStore, ep)
	}
	return libStore.LoadOrComputeOrphanPHash(ctx, hasher, path)
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
