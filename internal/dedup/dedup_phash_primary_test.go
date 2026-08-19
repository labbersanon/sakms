// Claude 2026-08-04: new file — AC regression suite for the phash-primary
// scan (ScanLibraryPHash / ScanLibrarySeriesPHash), shipped in 50dd970
// (2026-07-18) with zero dedicated test coverage of its own (only the two
// progress-accounting tests in dedup_progress_test.go touched it at all).
// Reason: spec ACs 1-5 and 7 (.omc/specs/deep-interview-phash-grouping.md)
// were implemented but unproven — see the residual-gap plan's Wave 2
// (.omc/plans/autopilot-impl-phash-grouping.md §3).
// Troubleshooting: n/a — new coverage, no prior bug in this file's own tests.
// Review if: never — this file's purpose is durable regression coverage.
package dedup

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mediainfo"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/phash"
)

// Claude 2026-08-04: ported here (Wave 4, legacy-scan retirement per
// .omc/plans/autopilot-impl-phash-grouping.md) from the now-deleted
// dedup_library_test.go's TestScanLibrary_RequiresRootFolderPath and
// TestScanLibrary_SingleNewOrphanIsNotADuplicate. Reason: ScanLibraryPHash
// has its own identical rootFolderPath=="" guard (dedup_phash_primary.go)
// and single-item-no-group behavior that had zero direct coverage of their
// own — only the deleted legacy scan proved these invariants.
// Note: ScanLibraryPHash has no TMDB-configured guard (by design, D1 —
// it's the TMDB-less path), so TestScanLibrary_RequiresTMDBConfigured has
// no phash-primary equivalent and is not ported.
// Troubleshooting: n/a — regression coverage only.
// Review if: never — durable regression coverage.

// TestScanLibraryPHash_RequiresRootFolderPath proves ScanLibraryPHash
// refuses to scan without a configured root folder path.
func TestScanLibraryPHash_RequiresRootFolderPath(t *testing.T) {
	sess := &mode.Session{Mode: mode.Movies}
	if _, err := ScanLibraryPHash(context.Background(), sess, newTestLibraryStore(t), "", &fakeProber{}, &fakePHasher{}, 2, nil); err == nil {
		t.Fatal("expected an error when no root folder path is configured")
	}
}

// TestScanLibraryPHash_SingleNewOrphanIsNotADuplicate proves a single
// untracked file alone in the library never produces a duplicate group.
func TestScanLibraryPHash_SingleNewOrphanIsNotADuplicate(t *testing.T) {
	dir := t.TempDir()
	writeVideoFile(t, filepath.Join(dir, "New.Movie.2020"), "movie.mkv", 100)

	sess := &mode.Session{Mode: mode.Movies}
	got, err := ScanLibraryPHash(context.Background(), sess, newTestLibraryStore(t), dir, &fakeProber{}, &fakePHasher{}, 2, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected no duplicate groups for a single new item, got %+v", got)
	}
}

// TestScanLibraryPHash_AC1_OrphanVsOrphanGrouped proves spec AC1: two
// untracked files with matching phashes and different filenames — no shared
// TMDB identifier, no tracked row on either side — still group into one
// proposal, with both candidates present and a winner marked.
func TestScanLibraryPHash_AC1_OrphanVsOrphanGrouped(t *testing.T) {
	dir := t.TempDir()
	fileA := writeVideoFile(t, dir, "Movie.Name.2020.1080p.BluRay.x264-GROUP1.mkv", 100)
	fileB := writeVideoFile(t, dir, "MovieName2020.720p.WEB.x264-GROUP2.mkv", 100)

	libStore := newTestLibraryStore(t)
	sess := &mode.Session{Mode: mode.Movies}
	prober := &fakeProber{byPath: map[string]*mediainfo.Probe{
		fileA: {CodecName: "h264", Width: 1280, Height: 720, BitRate: 3000},
		fileB: {CodecName: "h265", Width: 1920, Height: 1080, BitRate: 8000},
	}}

	got, err := ScanLibraryPHash(context.Background(), sess, libStore, dir, prober, matchingPHasher(fileA, fileB), 2, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || len(got[0].Candidates) != 2 {
		t.Fatalf("expected exactly 1 duplicate group with 2 candidates, got %+v", got)
	}
	var sawWinner bool
	for _, c := range got[0].Candidates {
		if c.TrackedID != 0 {
			t.Errorf("expected a pure orphan-vs-orphan group (no tracked candidate), got %+v", c)
		}
		if c.Winner {
			sawWinner = true
		}
	}
	if !sawWinner {
		t.Error("expected exactly one candidate marked as the winner")
	}
}

// TestScanLibraryPHash_AC2_CrossIDMisassignmentGrouped proves spec AC2: two
// TRACKED items resolved to DIFFERENT TMDB ids, but perceptually identical,
// still group into one proposal — TMDB never gates the union. Similarity
// must read >= 0.9 for a near-identical pair.
func TestScanLibraryPHash_AC2_CrossIDMisassignmentGrouped(t *testing.T) {
	dir := t.TempDir()
	fileA := writeVideoFile(t, filepath.Join(dir, "Movie A"), "moviea.mkv", 100)
	fileB := writeVideoFile(t, filepath.Join(dir, "Movie B"), "movieb.mkv", 100)

	libStore := newTestLibraryStore(t)
	ctx := context.Background()
	if _, err := libStore.Upsert(ctx, library.Item{
		Mode: mode.Movies, TMDBID: 1, Title: "Movie A", FilePath: fileA, RootFolderPath: dir,
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := libStore.Upsert(ctx, library.Item{
		Mode: mode.Movies, TMDBID: 2, Title: "Movie B", FilePath: fileB, RootFolderPath: dir,
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	sess := &mode.Session{Mode: mode.Movies}
	prober := &fakeProber{byPath: map[string]*mediainfo.Probe{
		fileA: {CodecName: "h264", Width: 1280, Height: 720, BitRate: 3000},
		fileB: {CodecName: "h264", Width: 1280, Height: 720, BitRate: 3000},
	}}

	got, err := ScanLibraryPHash(ctx, sess, libStore, dir, prober, matchingPHasher(fileA, fileB), 2, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected the two different-TMDBID tracked items to group into 1 proposal, got %+v", got)
	}
	if got[0].PHashSimilarity < 0.9 {
		t.Errorf("expected PHashSimilarity >= 0.9 for an identical-hash pair, got %v", got[0].PHashSimilarity)
	}
}

// TestScanLibraryPHash_AC3_NamedVsUnnamedGrouped proves spec AC3: one tracked
// item plus an orphan whose filename is too generic to ever resolve via TMDB
// search still group by phash alone — the tracked item's resolved title wins
// as the group's display label despite the orphan being unnamed.
func TestScanLibraryPHash_AC3_NamedVsUnnamedGrouped(t *testing.T) {
	dir := t.TempDir()
	trackedFile := writeVideoFile(t, filepath.Join(dir, "Some Movie (2020)"), "movie.mkv", 100)
	orphanFile := writeVideoFile(t, dir, "VIDEO_0001.mkv", 100)

	libStore := newTestLibraryStore(t)
	if _, err := libStore.Upsert(context.Background(), library.Item{
		Mode: mode.Movies, TMDBID: 42, Title: "Some Movie", FilePath: trackedFile, RootFolderPath: dir,
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// No TMDB client at all — mirrors a generic filename that would never
	// resolve, without needing a fake search server that expects no calls.
	sess := &mode.Session{Mode: mode.Movies}
	prober := &fakeProber{byPath: map[string]*mediainfo.Probe{
		trackedFile: {CodecName: "h264", Width: 1280, Height: 720, BitRate: 3000},
		orphanFile:  {CodecName: "h265", Width: 1920, Height: 1080, BitRate: 8000},
	}}

	got, err := ScanLibraryPHash(context.Background(), sess, libStore, dir, prober, matchingPHasher(trackedFile, orphanFile), 2, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || len(got[0].Candidates) != 2 {
		t.Fatalf("expected the generically-named orphan to group with the tracked item, got %+v", got)
	}
	if got[0].Title != "Some Movie" {
		t.Errorf("expected the tracked item's title to label the group despite the unnamed orphan, got %q", got[0].Title)
	}
}

// TestScanLibraryPHash_AC4_DissimilarFilesNotGrouped proves spec AC4: a
// tracked item and an orphan whose hashes fall outside the Movies threshold
// are never grouped, even though nothing else distinguishes them.
func TestScanLibraryPHash_AC4_DissimilarFilesNotGrouped(t *testing.T) {
	dir := t.TempDir()
	trackedFile := writeVideoFile(t, filepath.Join(dir, "Some Movie (2020)"), "movie.mkv", 100)
	orphanFile := writeVideoFile(t, dir, "Different.Movie.2021.mkv", 100)

	libStore := newTestLibraryStore(t)
	if _, err := libStore.Upsert(context.Background(), library.Item{
		Mode: mode.Movies, TMDBID: 42, Title: "Some Movie", FilePath: trackedFile, RootFolderPath: dir,
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	sess := &mode.Session{Mode: mode.Movies}
	prober := &fakeProber{byPath: map[string]*mediainfo.Probe{
		trackedFile: {CodecName: "h264", Width: 1280, Height: 720, BitRate: 3000},
		orphanFile:  {CodecName: "h265", Width: 1920, Height: 1080, BitRate: 8000},
	}}
	hasher := &fakePHasher{byPath: map[string]string{trackedFile: refHash, orphanFile: farHash}}

	got, err := ScanLibraryPHash(context.Background(), sess, libStore, dir, prober, hasher, 2, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected no proposal for perceptually dissimilar files, got %+v", got)
	}
}

// TestScanLibrarySeriesPHash_AC5_DifferentEpisodesNotGrouped proves spec AC5:
// two different episodes of the same show, with hashes outside the (stricter)
// Series threshold, are not grouped — even though they'd share every other
// identifier (series, adjacent episodes).
func TestScanLibrarySeriesPHash_AC5_DifferentEpisodesNotGrouped(t *testing.T) {
	dir := t.TempDir()
	ep1File := writeVideoFile(t, filepath.Join(dir, "Show Name", "Season 01"), "Show Name - S01E01.mkv", 100)
	ep2File := writeVideoFile(t, filepath.Join(dir, "Show Name", "Season 01"), "Show Name - S01E02.mkv", 100)

	libStore := newTestLibraryStore(t)
	ctx := context.Background()
	series, err := libStore.UpsertSeries(ctx, library.Series{TMDBID: 555, Title: "Show Name", RootFolderPath: dir})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := libStore.UpsertEpisode(ctx, library.Episode{
		SeriesID: series.ID, SeasonNumber: 1, EpisodeNumber: 1, FilePath: ep1File,
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := libStore.UpsertEpisode(ctx, library.Episode{
		SeriesID: series.ID, SeasonNumber: 1, EpisodeNumber: 2, FilePath: ep2File,
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	sess := &mode.Session{Mode: mode.Series}
	prober := &fakeProber{byPath: map[string]*mediainfo.Probe{
		ep1File: {CodecName: "h264", Width: 1280, Height: 720, BitRate: 3000},
		ep2File: {CodecName: "h264", Width: 1280, Height: 720, BitRate: 3000},
	}}
	hasher := &fakePHasher{byPath: map[string]string{ep1File: refHash, ep2File: farHash}}

	got, err := ScanLibrarySeriesPHash(ctx, sess, libStore, dir, prober, hasher, 2, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected two dissimilar episodes to stay ungrouped, got %+v", got)
	}
}

// TestScanLibrarySeriesPHash_SharedSplitFileIsNotADuplicate proves a
// logical-episode-split file (two episode rows, one path) is not proposed as
// a duplicate of itself. Live: Burning Love (2012) S01E01-E02 etc.
func TestScanLibrarySeriesPHash_SharedSplitFileIsNotADuplicate(t *testing.T) {
	dir := t.TempDir()
	shared := writeVideoFile(t, filepath.Join(dir, "Show Name", "Season 01"), "Show Name - S01E01-E02.mkv", 100)

	libStore := newTestLibraryStore(t)
	ctx := context.Background()
	series, err := libStore.UpsertSeries(ctx, library.Series{TMDBID: 555, Title: "Show Name", RootFolderPath: dir})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := libStore.UpsertEpisode(ctx, library.Episode{
		SeriesID: series.ID, SeasonNumber: 1, EpisodeNumber: 1, Title: "Part 1", FilePath: shared,
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := libStore.UpsertEpisode(ctx, library.Episode{
		SeriesID: series.ID, SeasonNumber: 1, EpisodeNumber: 2, Title: "Part 2", FilePath: shared,
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	sess := &mode.Session{Mode: mode.Series}
	prober := &fakeProber{byPath: map[string]*mediainfo.Probe{
		shared: {CodecName: "h264", Width: 1280, Height: 720, BitRate: 3000},
	}}
	hasher := matchingPHasher(shared)

	got, err := ScanLibrarySeriesPHash(ctx, sess, libStore, dir, prober, hasher, 2, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected no proposal for one split file backing two episode rows, got %+v", got)
	}
	if hasher.calls[shared] != 1 {
		t.Errorf("expected the shared file to be hashed once, got %d calls", hasher.calls[shared])
	}
}

// TestScanLibrarySeriesPHash_SharedSplitFileStillGroupsRealExtraCopy proves
// unique-by-path does not hide a genuine second file of the same content.
func TestScanLibrarySeriesPHash_SharedSplitFileStillGroupsRealExtraCopy(t *testing.T) {
	dir := t.TempDir()
	shared := writeVideoFile(t, filepath.Join(dir, "Show Name", "Season 01"), "Show Name - S01E01-E02.mkv", 100)
	extra := writeVideoFile(t, dir, "Show.Name.S01E01-E02.REPACK.mkv", 100)

	libStore := newTestLibraryStore(t)
	ctx := context.Background()
	series, err := libStore.UpsertSeries(ctx, library.Series{TMDBID: 555, Title: "Show Name", RootFolderPath: dir})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := libStore.UpsertEpisode(ctx, library.Episode{
		SeriesID: series.ID, SeasonNumber: 1, EpisodeNumber: 1, FilePath: shared,
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := libStore.UpsertEpisode(ctx, library.Episode{
		SeriesID: series.ID, SeasonNumber: 1, EpisodeNumber: 2, FilePath: shared,
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	sess := &mode.Session{Mode: mode.Series}
	prober := &fakeProber{byPath: map[string]*mediainfo.Probe{
		shared: {CodecName: "h264", Width: 1280, Height: 720, BitRate: 3000},
		extra:  {CodecName: "h265", Width: 1920, Height: 1080, BitRate: 8000},
	}}
	hasher := matchingPHasher(shared, extra)

	got, err := ScanLibrarySeriesPHash(ctx, sess, libStore, dir, prober, hasher, 2, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || len(got[0].Candidates) != 2 {
		t.Fatalf("expected one group of the split file plus the extra copy, got %+v", got)
	}
	paths := map[string]bool{}
	for _, c := range got[0].Candidates {
		paths[c.Path] = true
	}
	if !paths[shared] || !paths[extra] {
		t.Errorf("expected candidates to be %q and %q, got %+v", shared, extra, got[0].Candidates)
	}
}

// TestScanLibraryPHash_AC7_CacheHitAvoidsRehashOnSecondScan proves spec AC7:
// a second scan over an unchanged fixture reuses the cached hash for BOTH a
// tracked item (library_items.phash) and an orphan (orphan_phashes) — zero
// additional hasher.Hash calls for either.
func TestScanLibraryPHash_AC7_CacheHitAvoidsRehashOnSecondScan(t *testing.T) {
	dir := t.TempDir()
	trackedFile := writeVideoFile(t, filepath.Join(dir, "Some Movie (2020)"), "movie.mkv", 100)
	orphanFile := writeVideoFile(t, dir, "Some.Movie.2020.REMUX.mkv", 100)

	libStore := newTestLibraryStore(t)
	ctx := context.Background()
	if _, err := libStore.Upsert(ctx, library.Item{
		Mode: mode.Movies, TMDBID: 42, Title: "Some Movie", FilePath: trackedFile, RootFolderPath: dir,
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	sess := &mode.Session{Mode: mode.Movies}
	prober := &fakeProber{byPath: map[string]*mediainfo.Probe{
		trackedFile: {CodecName: "h264", Width: 1280, Height: 720, BitRate: 3000},
		orphanFile:  {CodecName: "h265", Width: 1920, Height: 1080, BitRate: 8000},
	}}
	hasher := matchingPHasher(trackedFile, orphanFile)

	if _, err := ScanLibraryPHash(ctx, sess, libStore, dir, prober, hasher, 2, nil); err != nil {
		t.Fatalf("unexpected error on first scan: %v", err)
	}
	if hasher.calls[trackedFile] != 1 || hasher.calls[orphanFile] != 1 {
		t.Fatalf("expected the first scan to hash each file exactly once, got %+v", hasher.calls)
	}

	if _, err := ScanLibraryPHash(ctx, sess, libStore, dir, prober, hasher, 2, nil); err != nil {
		t.Fatalf("unexpected error on second scan: %v", err)
	}
	if hasher.calls[trackedFile] != 1 {
		t.Errorf("expected the tracked item's cached hash to be reused on the second scan, got %d calls", hasher.calls[trackedFile])
	}
	if hasher.calls[orphanFile] != 1 {
		t.Errorf("expected the orphan's cached hash (orphan_phashes) to be reused on the second scan, got %d calls", hasher.calls[orphanFile])
	}
}

// TestScanLibraryPHash_TransitiveGroupUsesWorstPairSimilarity proves the §1b
// transitive-grouping contract (dedup_phash_primary.go's own doc comment on
// pHashGroupComponents): A~B and B~C group transitively into {A,B,C} even
// though A and C individually fail a direct pairwise comparison, and the
// reported PHashSimilarity is the WORST (minimum) pair in the group — here,
// A-C — not the best or the union-find's own edges.
func TestScanLibraryPHash_TransitiveGroupUsesWorstPairSimilarity(t *testing.T) {
	dir := t.TempDir()
	fileA := writeVideoFile(t, dir, "Movie.A.mkv", 100)
	fileB := writeVideoFile(t, dir, "Movie.B.mkv", 100)
	fileC := writeVideoFile(t, dir, "Movie.C.mkv", 100)

	// perFrameThreshold=2, phash.Frames=5 -> a 10-bit total budget.
	//   distance(A,B) =  4 bits -> within budget, unioned directly.
	//   distance(B,C) =  7 bits -> within budget, unioned directly.
	//   distance(A,C) = 11 bits -> OUTSIDE budget, never unioned directly —
	//   A and C only end up in the same group via B (transitivity).
	hashA := seededHash("")
	hashB := seededHash("0f")
	hashC := seededHash("ff07")

	libStore := newTestLibraryStore(t)
	sess := &mode.Session{Mode: mode.Movies}
	prober := &fakeProber{byPath: map[string]*mediainfo.Probe{
		fileA: {CodecName: "h264", Width: 1280, Height: 720, BitRate: 3000},
		fileB: {CodecName: "h264", Width: 1280, Height: 720, BitRate: 3000},
		fileC: {CodecName: "h264", Width: 1280, Height: 720, BitRate: 3000},
	}}
	hasher := &fakePHasher{byPath: map[string]string{fileA: hashA, fileB: hashB, fileC: hashC}}

	got, err := ScanLibraryPHash(context.Background(), sess, libStore, dir, prober, hasher, 2, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || len(got[0].Candidates) != 3 {
		t.Fatalf("expected all three files transitively grouped via B, got %+v", got)
	}

	wantSimilarity, simErr := phash.SimilarityScore(hashA, hashC, phash.Frames)
	if simErr != nil {
		t.Fatalf("unexpected error computing the expected A-C score: %v", simErr)
	}
	if got[0].PHashSimilarity != wantSimilarity {
		t.Errorf("expected the group's PHashSimilarity to be the worst pair (A-C, %v), got %v", wantSimilarity, got[0].PHashSimilarity)
	}
}

// TestScanLibraryPHash_SkipsAppliedAlternates proves plan §11.2/§9.8: a
// tracked item's applied alternate — a deliberate second copy recorded as a
// non-primary row in library_item_files — is never re-discovered as an
// orphan and phash-grouped against its own primary. Without the AllFilePaths
// known-set pass in ScanLibraryPHash, this fails against main today (the
// pre-existing Movies defect §0.10 describes): the alternate would surface
// as an orphan, phash-match its own primary (same hash on purpose here), and
// produce a Dedup proposal offering to delete the file Rename just placed.
func TestScanLibraryPHash_SkipsAppliedAlternates(t *testing.T) {
	dir := t.TempDir()
	primaryFile := writeVideoFile(t, dir, "Some Movie (2020).mkv", 100)
	alternateFile := writeVideoFile(t, dir, "Some Movie (2020) - 720p h264.mkv", 100)

	libStore := newTestLibraryStore(t)
	ctx := context.Background()
	item, err := libStore.Upsert(ctx, library.Item{
		Mode: mode.Movies, TMDBID: 42, Title: "Some Movie", FilePath: primaryFile, RootFolderPath: dir,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := libStore.UpsertFile(ctx, library.ItemFile{
		ItemID: item.ID, FilePath: alternateFile, IsPrimary: false,
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	sess := &mode.Session{Mode: mode.Movies}
	prober := &fakeProber{byPath: map[string]*mediainfo.Probe{
		primaryFile:   {CodecName: "h264", Width: 1920, Height: 1080, BitRate: 8000},
		alternateFile: {CodecName: "h264", Width: 1280, Height: 720, BitRate: 3000},
	}}

	got, err := ScanLibraryPHash(ctx, sess, libStore, dir, prober, matchingPHasher(primaryFile, alternateFile), 2, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected the applied alternate to be excluded from Dedup consideration, got %+v", got)
	}
}

// TestScanLibrarySeriesPHash_SkipsAppliedAlternates is the Series counterpart
// of TestScanLibraryPHash_SkipsAppliedAlternates via library_episode_files.
// An episode with a primary plus an alternate row, both files on disk, must
// produce no Dedup proposal — without the Series known-set pass the
// alternate is discovered as an orphan and phash-grouped against its own
// primary episode.
func TestScanLibrarySeriesPHash_SkipsAppliedAlternates(t *testing.T) {
	dir := t.TempDir()
	primaryFile := writeVideoFile(t, filepath.Join(dir, "Show Name", "Season 01"), "Show Name - S01E01.mkv", 100)
	alternateFile := writeVideoFile(t, filepath.Join(dir, "Show Name", "Season 01"), "Show Name - S01E01 - 720p h264.mkv", 100)

	libStore := newTestLibraryStore(t)
	ctx := context.Background()
	series, err := libStore.UpsertSeries(ctx, library.Series{TMDBID: 555, Title: "Show Name", RootFolderPath: dir})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	ep, err := libStore.UpsertEpisode(ctx, library.Episode{
		SeriesID: series.ID, SeasonNumber: 1, EpisodeNumber: 1, FilePath: primaryFile,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := libStore.UpsertEpisodeFile(ctx, library.EpisodeFile{
		EpisodeID: ep.ID, FilePath: alternateFile, IsPrimary: false,
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	sess := &mode.Session{Mode: mode.Series}
	prober := &fakeProber{byPath: map[string]*mediainfo.Probe{
		primaryFile:   {CodecName: "h264", Width: 1920, Height: 1080, BitRate: 8000},
		alternateFile: {CodecName: "h264", Width: 1280, Height: 720, BitRate: 3000},
	}}

	got, err := ScanLibrarySeriesPHash(ctx, sess, libStore, dir, prober, matchingPHasher(primaryFile, alternateFile), 2, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected the applied alternate to be excluded from Dedup consideration, got %+v", got)
	}
}

// Claude 2026-08-04: ported here (Wave 4, legacy-scan retirement per
// .omc/plans/autopilot-impl-phash-grouping.md) and adapted from the
// now-deleted dedup_library_series_test.go's
// TestScanLibrarySeries_SeasonPackOrphanMatchesExistingSingleEpisodeDuplicate.
// Reason: ScanLibrarySeriesPHash still breaks a season-pack orphan directory
// into individual files via library.ResolveEpisodeVideoFiles (same
// mechanism the legacy scan used), but groups purely by phash similarity
// now, not by (show,season,episode) key — dedup_progress_test.go's own
// season-pack fixture (TestScanLibrarySeriesPHash_SeasonPackProgressNeverExceedsTotal)
// gives all 3 files an identical hash and never asserts which files
// actually group, so it can't catch a regression where pack expansion loses
// its per-file selectivity. This test uses a distinguishing hash so only
// the matching episode groups, proving expansion + phash-selective grouping
// together, not just expansion + progress accounting.
// Troubleshooting: n/a — regression coverage only.
// Review if: never — durable regression coverage.

// TestScanLibrarySeriesPHash_SeasonPackOrphanSelectivelyGroupsMatchingEpisode
// proves a season-pack orphan directory is broken into individual files via
// ResolveEpisodeVideoFiles, and only the file whose phash matches the
// already-tracked episode groups with it — the pack's other episode (a
// dissimilar hash) stays a lone, ungrouped orphan.
func TestScanLibrarySeriesPHash_SeasonPackOrphanSelectivelyGroupsMatchingEpisode(t *testing.T) {
	dir := t.TempDir()
	trackedFile := writeVideoFile(t, filepath.Join(dir, "Show Name", "Season 01"), "Show Name - S01E01.mkv", 100)
	packDir := filepath.Join(dir, "Show.Name.Season.01.1080p.WEB-DL-GROUP")
	packEp1 := writeVideoFile(t, packDir, "Show.Name.S01E01.mkv", 100)
	packEp2 := writeVideoFile(t, packDir, "Show.Name.S01E02.mkv", 100)

	libStore := newTestLibraryStore(t)
	ctx := context.Background()
	series, err := libStore.UpsertSeries(ctx, library.Series{TMDBID: 555, Title: "Show Name", RootFolderPath: dir})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	tracked, err := libStore.UpsertEpisode(ctx, library.Episode{
		SeriesID: series.ID, SeasonNumber: 1, EpisodeNumber: 1, FilePath: trackedFile,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	sess := &mode.Session{Mode: mode.Series}
	prober := &fakeProber{byPath: map[string]*mediainfo.Probe{
		trackedFile: {CodecName: "h264", Width: 1280, Height: 720, BitRate: 3000},
		packEp1:     {CodecName: "h265", Width: 1920, Height: 1080, BitRate: 8000},
		packEp2:     {CodecName: "h265", Width: 1920, Height: 1080, BitRate: 8000},
	}}
	hasher := &fakePHasher{byPath: map[string]string{trackedFile: refHash, packEp1: refHash, packEp2: farHash}}

	got, err := ScanLibrarySeriesPHash(ctx, sess, libStore, dir, prober, hasher, 2, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Episode 1's pack file matches the tracked copy (2 candidates); episode
	// 2's pack file is perceptually dissimilar (farHash), so it's a lone new
	// orphan — not reported.
	if len(got) != 1 {
		t.Fatalf("expected exactly 1 duplicate group (episode 1 only), got %d: %+v", len(got), got)
	}
	p := got[0]
	if len(p.Candidates) != 2 {
		t.Fatalf("unexpected proposal: %+v", p)
	}
	var sawPackFile, sawTrackedFile bool
	for _, c := range p.Candidates {
		if c.Path == packEp1 {
			sawPackFile = true
		}
		if c.Path == trackedFile {
			sawTrackedFile = true
			if c.TrackedID != int(tracked.ID) {
				t.Errorf("expected the tracked candidate's TrackedID to be %d, got %d", tracked.ID, c.TrackedID)
			}
		}
	}
	if !sawPackFile || !sawTrackedFile {
		t.Fatalf("expected the season-pack's matching episode 1 file to group with the tracked episode 1, got %+v", p.Candidates)
	}
}

// TestScanLibraryPHash_PrunesStaleOrphanCacheAfterFileRemoved proves open
// decision #1's resolution (DeleteOrphanPHashesNotIn, called by every
// phash-primary scan): once an orphan file is removed from disk, the next
// scan prunes its now-stale orphan_phashes row rather than leaking it forever.
func TestScanLibraryPHash_PrunesStaleOrphanCacheAfterFileRemoved(t *testing.T) {
	dir := t.TempDir()
	staleFile := writeVideoFile(t, dir, "Stale.Movie.mkv", 100)

	libStore := newTestLibraryStore(t)
	ctx := context.Background()
	sess := &mode.Session{Mode: mode.Movies}
	prober := &fakeProber{byPath: map[string]*mediainfo.Probe{
		staleFile: {CodecName: "h264", Width: 1280, Height: 720, BitRate: 3000},
	}}
	hasher := &fakePHasher{byPath: map[string]string{staleFile: refHash}}

	if _, err := ScanLibraryPHash(ctx, sess, libStore, dir, prober, hasher, 2, nil); err != nil {
		t.Fatalf("unexpected error on first scan: %v", err)
	}
	cached, err := libStore.GetOrphanPHash(ctx, staleFile)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cached.PHash == "" {
		t.Fatal("expected the orphan's phash to be cached after the first scan")
	}

	if err := os.Remove(staleFile); err != nil {
		t.Fatalf("removing the stale file: %v", err)
	}
	if _, err := ScanLibraryPHash(ctx, sess, libStore, dir, prober, hasher, 2, nil); err != nil {
		t.Fatalf("unexpected error on second scan: %v", err)
	}

	cached, err = libStore.GetOrphanPHash(ctx, staleFile)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cached.PHash != "" {
		t.Errorf("expected the orphan_phashes row for the removed file to be pruned, got %+v", cached)
	}
}
