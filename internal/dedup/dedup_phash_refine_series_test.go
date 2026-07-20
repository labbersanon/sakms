package dedup

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mediainfo"
	"github.com/labbersanon/sakms/internal/mode"
)

// These mirror the Movies refinement tests (dedup_phash_refine_test.go) for the
// Series path, reusing the package-level refHash/nearHash/farHash seeds and the
// same fake TMDB/prober/hasher helpers. The grouping key differs
// ((show, season, episode) vs a bare TMDB id) but the refinement mechanics —
// attachPHashesSeries → refineByPHash → keep-both below 2 survivors — are the
// same, so the assertions parallel the Movies ones. The sixth test is
// Series-specific: it proves the refinement runs correctly on the flat
// post-season-pack-split candidate list.

func TestScanLibrarySeries_PHashKeepsNearIdenticalGroup(t *testing.T) {
	dir := t.TempDir()
	trackedFile := writeVideoFile(t, filepath.Join(dir, "Show Name", "Season 01"), "Show Name - S01E01.mkv", 100)
	orphanFile := writeVideoFile(t, dir, "Show.Name.S01E01.1080p.BluRay.x264-GROUP.mkv", 100)

	libStore := newTestLibraryStore(t)
	ctx := context.Background()
	series, err := libStore.UpsertSeries(ctx, library.Series{TMDBID: 555, Title: "Show Name", RootFolderPath: dir})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := libStore.UpsertEpisode(ctx, library.Episode{
		SeriesID: series.ID, SeasonNumber: 1, EpisodeNumber: 1, FilePath: trackedFile,
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	sess := &mode.Session{Mode: mode.Series, TMDB: fakeTMDBSeriesSearch(t, map[string]string{
		"Show Name": `{"results":[{"id":555,"name":"Show Name"}]}`,
	})}
	prober := &fakeProber{byPath: map[string]*mediainfo.Probe{
		trackedFile: {CodecName: "h264", Width: 1280, Height: 720, BitRate: 3000},
		orphanFile:  {CodecName: "h265", Width: 1920, Height: 1080, BitRate: 8000},
	}}
	hasher := &fakePHasher{byPath: map[string]string{trackedFile: refHash, orphanFile: nearHash}}

	got, err := ScanLibrarySeries(ctx, sess, libStore, dir, prober, hasher, 2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || len(got[0].Candidates) != 2 {
		t.Fatalf("expected a near-identical pair to stay one 2-candidate group, got %+v", got)
	}
}

func TestScanLibrarySeries_PHashDropsDivergentCandidate(t *testing.T) {
	dir := t.TempDir()
	trackedFile := writeVideoFile(t, filepath.Join(dir, "Show Name", "Season 01"), "Show Name - S01E01.mkv", 100)
	orphanFile := writeVideoFile(t, dir, "Show.Name.S01E01.1080p.BluRay.x264-GROUP.mkv", 100)

	libStore := newTestLibraryStore(t)
	ctx := context.Background()
	series, err := libStore.UpsertSeries(ctx, library.Series{TMDBID: 555, Title: "Show Name", RootFolderPath: dir})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := libStore.UpsertEpisode(ctx, library.Episode{
		SeriesID: series.ID, SeasonNumber: 1, EpisodeNumber: 1, FilePath: trackedFile,
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	sess := &mode.Session{Mode: mode.Series, TMDB: fakeTMDBSeriesSearch(t, map[string]string{
		"Show Name": `{"results":[{"id":555,"name":"Show Name"}]}`,
	})}
	prober := &fakeProber{byPath: map[string]*mediainfo.Probe{
		trackedFile: {CodecName: "h264", Width: 1280, Height: 720, BitRate: 3000},
		orphanFile:  {CodecName: "h265", Width: 1920, Height: 1080, BitRate: 8000},
	}}
	// The orphan shares the episode key but is perceptually far from the tracked
	// reference — it must be dropped, leaving a single survivor and thus NO
	// proposal (keep-both), proving the phash gate actually refines.
	hasher := &fakePHasher{byPath: map[string]string{trackedFile: refHash, orphanFile: farHash}}

	got, err := ScanLibrarySeries(ctx, sess, libStore, dir, prober, hasher, 2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected the divergent orphan to be dropped and no proposal produced, got %+v", got)
	}
}

func TestScanLibrarySeries_PHashUsesTrackedAsReference(t *testing.T) {
	dir := t.TempDir()
	trackedFile := writeVideoFile(t, filepath.Join(dir, "Show Name", "Season 01"), "Show Name - S01E01.mkv", 100)
	nearFile := writeVideoFile(t, dir, "Show.Name.S01E01.1080p.BluRay.x264-GROUP.mkv", 100)
	farFile := writeVideoFile(t, dir, "Show.Name.S01E01.720p.WEB.x264-OTHER.mkv", 100)

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

	sess := &mode.Session{Mode: mode.Series, TMDB: fakeTMDBSeriesSearch(t, map[string]string{
		"Show Name": `{"results":[{"id":555,"name":"Show Name"}]}`,
	})}
	prober := &fakeProber{byPath: map[string]*mediainfo.Probe{
		trackedFile: {CodecName: "h264", Width: 1280, Height: 720, BitRate: 3000},
		nearFile:    {CodecName: "h265", Width: 1920, Height: 1080, BitRate: 8000},
		farFile:     {CodecName: "h264", Width: 1280, Height: 720, BitRate: 2000},
	}}
	// Both orphans are measured against the TRACKED reference: the near one is
	// kept, the far one dropped. If an orphan were the reference the outcome
	// would differ.
	hasher := &fakePHasher{byPath: map[string]string{trackedFile: refHash, nearFile: nearHash, farFile: farHash}}

	got, err := ScanLibrarySeries(ctx, sess, libStore, dir, prober, hasher, 2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || len(got[0].Candidates) != 2 {
		t.Fatalf("expected the tracked reference plus the near orphan, got %+v", got)
	}
	var sawTracked bool
	for _, c := range got[0].Candidates {
		if c.Path == farFile {
			t.Errorf("expected the divergent far orphan to be dropped, but it survived: %+v", c)
		}
		if c.TrackedID == int(tracked.ID) {
			sawTracked = true
		}
	}
	if !sawTracked {
		t.Error("expected the tracked reference to never be dropped, but it's absent from the group")
	}
}

func TestScanLibrarySeries_PHashCacheReusedWhenIdentityMatches(t *testing.T) {
	dir := t.TempDir()
	trackedFile := writeVideoFile(t, filepath.Join(dir, "Show Name", "Season 01"), "Show Name - S01E01.mkv", 100)
	orphanFile := writeVideoFile(t, dir, "Show.Name.S01E01.1080p.BluRay.x264-GROUP.mkv", 100)

	// Seed the tracked episode's cache with a hash keyed to the file's CURRENT
	// identity (same helper ScanLibrarySeries uses), so the identity check matches.
	size, mtime, err := fileIdentity(trackedFile)
	if err != nil {
		t.Fatalf("stat tracked file: %v", err)
	}
	libStore := newTestLibraryStore(t)
	ctx := context.Background()
	series, err := libStore.UpsertSeries(ctx, library.Series{TMDBID: 555, Title: "Show Name", RootFolderPath: dir})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := libStore.UpsertEpisode(ctx, library.Episode{
		SeriesID: series.ID, SeasonNumber: 1, EpisodeNumber: 1, FilePath: trackedFile,
		PHash: refHash, PHashFileSize: size, PHashFileMTime: mtime,
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	sess := &mode.Session{Mode: mode.Series, TMDB: fakeTMDBSeriesSearch(t, map[string]string{
		"Show Name": `{"results":[{"id":555,"name":"Show Name"}]}`,
	})}
	prober := &fakeProber{byPath: map[string]*mediainfo.Probe{
		trackedFile: {CodecName: "h264", Width: 1280, Height: 720, BitRate: 3000},
		orphanFile:  {CodecName: "h265", Width: 1920, Height: 1080, BitRate: 8000},
	}}
	// trackedFile is present in byPath only as a safety net — the cache should
	// mean it's never asked for.
	hasher := &fakePHasher{byPath: map[string]string{trackedFile: refHash, orphanFile: nearHash}}

	got, err := ScanLibrarySeries(ctx, sess, libStore, dir, prober, hasher, 2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || len(got[0].Candidates) != 2 {
		t.Fatalf("expected a 2-candidate group with the cached tracked hash reused, got %+v", got)
	}
	if hasher.calls[trackedFile] != 0 {
		t.Errorf("expected the cached tracked episode NOT to be re-hashed (decode-once), got %d calls", hasher.calls[trackedFile])
	}
	if hasher.calls[orphanFile] != 1 {
		t.Errorf("expected the uncached orphan to be hashed exactly once, got %d calls", hasher.calls[orphanFile])
	}
}

// TestScanLibrarySeries_PHashAllCandidatesUncomputable is the panic regression:
// if every candidate in a same-episode group fails to hash, attachPHashesSeries
// drops all of them, so refineByPHash must handle a 0-length slice without
// panicking (it previously indexed candidates[0] unconditionally). Uncomputable
// is the same tolerant outcome as a divergent group: no proposal, keep-both.
func TestScanLibrarySeries_PHashAllCandidatesUncomputable(t *testing.T) {
	dir := t.TempDir()
	trackedFile := writeVideoFile(t, filepath.Join(dir, "Show Name", "Season 01"), "Show Name - S01E01.mkv", 100)
	orphanFile := writeVideoFile(t, dir, "Show.Name.S01E01.1080p.BluRay.x264-GROUP.mkv", 100)

	libStore := newTestLibraryStore(t)
	ctx := context.Background()
	series, err := libStore.UpsertSeries(ctx, library.Series{TMDBID: 555, Title: "Show Name", RootFolderPath: dir})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := libStore.UpsertEpisode(ctx, library.Episode{
		SeriesID: series.ID, SeasonNumber: 1, EpisodeNumber: 1, FilePath: trackedFile,
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	sess := &mode.Session{Mode: mode.Series, TMDB: fakeTMDBSeriesSearch(t, map[string]string{
		"Show Name": `{"results":[{"id":555,"name":"Show Name"}]}`,
	})}
	prober := &fakeProber{byPath: map[string]*mediainfo.Probe{
		trackedFile: {CodecName: "h264", Width: 1280, Height: 720, BitRate: 3000},
		orphanFile:  {CodecName: "h265", Width: 1920, Height: 1080, BitRate: 8000},
	}}
	// Empty byPath: every Hash call returns os.ErrNotExist, so attachPHashesSeries
	// drops the entire group down to a 0-length slice.
	hasher := &fakePHasher{}

	got, err := ScanLibrarySeries(ctx, sess, libStore, dir, prober, hasher, 2)
	if err != nil {
		t.Fatalf("unexpected error (must not panic): %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected no proposal when the whole group is uncomputable, got %+v", got)
	}
}

// TestScanLibrarySeries_PHashSeasonPackRefinesWithLooseDuplicate is the
// Series-specific acceptance case: a season pack is flattened into per-episode
// files upstream of grouping, so its S01E01 file and a loose S01E01 duplicate
// arrive as a flat two-candidate list — structurally identical to any other
// same-key orphan pair. Both are near-hashed, so refinement keeps them together
// as one 2-candidate proposal; the pack's lone S01E02 is a single new orphan
// and is not reported.
func TestScanLibrarySeries_PHashSeasonPackRefinesWithLooseDuplicate(t *testing.T) {
	dir := t.TempDir()
	packDir := filepath.Join(dir, "Show.Name.Season.01.1080p.WEB-DL-GROUP")
	packEp1 := writeVideoFile(t, packDir, "Show.Name.S01E01.mkv", 100)
	packEp2 := writeVideoFile(t, packDir, "Show.Name.S01E02.mkv", 100)
	looseEp1 := writeVideoFile(t, dir, "Show.Name.S01E01.720p.WEB.x264-OTHER.mkv", 100)

	libStore := newTestLibraryStore(t)
	ctx := context.Background()

	sess := &mode.Session{Mode: mode.Series, TMDB: fakeTMDBSeriesSearch(t, map[string]string{
		"Show Name": `{"results":[{"id":555,"name":"Show Name"}]}`,
	})}
	prober := &fakeProber{byPath: map[string]*mediainfo.Probe{
		packEp1:  {CodecName: "h265", Width: 1920, Height: 1080, BitRate: 8000},
		packEp2:  {CodecName: "h265", Width: 1920, Height: 1080, BitRate: 8000},
		looseEp1: {CodecName: "h264", Width: 1280, Height: 720, BitRate: 3000},
	}}
	// The pack's E01 and the loose E01 are perceptually identical (distance 0),
	// so refinement keeps both.
	hasher := &fakePHasher{byPath: map[string]string{packEp1: refHash, looseEp1: refHash}}

	got, err := ScanLibrarySeries(ctx, sess, libStore, dir, prober, hasher, 2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected exactly 1 duplicate group (episode 1 only), got %d: %+v", len(got), got)
	}
	p := got[0]
	if p.SeasonNumber != 1 || p.EpisodeNumber != 1 || len(p.Candidates) != 2 {
		t.Fatalf("unexpected proposal: %+v", p)
	}
	var sawPack, sawLoose bool
	for _, c := range p.Candidates {
		if c.Path == packEp1 {
			sawPack = true
		}
		if c.Path == looseEp1 {
			sawLoose = true
		}
	}
	if !sawPack || !sawLoose {
		t.Fatalf("expected the pack's E01 and the loose E01 to refine into one group, got %+v", p.Candidates)
	}
}
