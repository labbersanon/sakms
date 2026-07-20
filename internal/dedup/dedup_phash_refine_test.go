package dedup

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mediainfo"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/searchterm"
)

// seededHash builds a scheme-tagged 40-byte (5-frame) composite whose leading
// bytes are hexPrefix, zero-padded — so a test controls the exact Hamming
// distance between two candidates. "" is the all-zero reference.
func seededHash(hexPrefix string) string {
	return "phash64/5f:" + hexPrefix + strings.Repeat("0", 80-len(hexPrefix))
}

// tmdbTo42 maps each name's TMDB search term to the same movie id, so a set of
// orphans (and the tracked item) all group under one TMDB key.
func tmdbTo42(names ...string) map[string]string {
	m := map[string]string{}
	for _, n := range names {
		m[searchterm.FromName(n)] = `{"results":[{"id":42,"title":"Some Movie"}]}`
	}
	return m
}

// refHash is the all-zero reference; nearHash differs by 4 bits (0x0f), well
// within a per-frame threshold of 2 (budget 2×5 = 10); farHash sets every bit
// (320 differing), far outside it.
var (
	refHash  = seededHash("")
	nearHash = seededHash("0f")
	farHash  = seededHash(strings.Repeat("f", 80))
)

func TestScanLibrary_PHashKeepsNearIdenticalGroup(t *testing.T) {
	dir := t.TempDir()
	trackedFile := writeVideoFile(t, filepath.Join(dir, "Some Movie (2020)"), "movie.mkv", 100)
	orphanDir := "Some.Movie.2020.1080p.BluRay.x264-GROUP"
	orphanFile := writeVideoFile(t, filepath.Join(dir, orphanDir), "movie.mkv", 100)

	libStore := newTestLibraryStore(t)
	if _, err := libStore.Upsert(context.Background(), library.Item{
		Mode: mode.Movies, TMDBID: 42, Title: "Some Movie", FilePath: trackedFile, RootFolderPath: dir,
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	sess := &mode.Session{Mode: mode.Movies, TMDB: fakeTMDBSearch(t, tmdbTo42(orphanDir))}
	prober := &fakeProber{byPath: map[string]*mediainfo.Probe{
		trackedFile: {CodecName: "h264", Width: 1280, Height: 720, BitRate: 3000},
		orphanFile:  {CodecName: "h265", Width: 1920, Height: 1080, BitRate: 8000},
	}}
	hasher := &fakePHasher{byPath: map[string]string{trackedFile: refHash, orphanFile: nearHash}}

	got, err := ScanLibrary(context.Background(), sess, libStore, dir, prober, hasher, 2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || len(got[0].Candidates) != 2 {
		t.Fatalf("expected a near-identical pair to stay one 2-candidate group, got %+v", got)
	}
}

func TestScanLibrary_PHashDropsDivergentCandidate(t *testing.T) {
	dir := t.TempDir()
	trackedFile := writeVideoFile(t, filepath.Join(dir, "Some Movie (2020)"), "movie.mkv", 100)
	orphanDir := "Some.Movie.2020.1080p.BluRay.x264-GROUP"
	orphanFile := writeVideoFile(t, filepath.Join(dir, orphanDir), "movie.mkv", 100)

	libStore := newTestLibraryStore(t)
	if _, err := libStore.Upsert(context.Background(), library.Item{
		Mode: mode.Movies, TMDBID: 42, Title: "Some Movie", FilePath: trackedFile, RootFolderPath: dir,
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	sess := &mode.Session{Mode: mode.Movies, TMDB: fakeTMDBSearch(t, tmdbTo42(orphanDir))}
	prober := &fakeProber{byPath: map[string]*mediainfo.Probe{
		trackedFile: {CodecName: "h264", Width: 1280, Height: 720, BitRate: 3000},
		orphanFile:  {CodecName: "h265", Width: 1920, Height: 1080, BitRate: 8000},
	}}
	// The orphan shares the TMDB id but is perceptually far from the tracked
	// reference — it must be dropped, leaving a single survivor and thus NO
	// proposal (keep-both), proving the phash gate actually refines.
	hasher := &fakePHasher{byPath: map[string]string{trackedFile: refHash, orphanFile: farHash}}

	got, err := ScanLibrary(context.Background(), sess, libStore, dir, prober, hasher, 2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected the divergent orphan to be dropped and no proposal produced, got %+v", got)
	}
}

// TestScanLibrary_PHashAllCandidatesUncomputable is a regression test: if
// every candidate in a same-TMDB group fails to hash (e.g. ffmpeg missing or
// every file corrupt), attachPHashes drops all of them, so refineByPHash must
// handle a 0-length slice without panicking — it previously indexed
// candidates[0] unconditionally. Uncomputable is the same tolerant outcome as
// a divergent group: no proposal, keep-both.
func TestScanLibrary_PHashAllCandidatesUncomputable(t *testing.T) {
	dir := t.TempDir()
	trackedFile := writeVideoFile(t, filepath.Join(dir, "Some Movie (2020)"), "movie.mkv", 100)
	orphanDir := "Some.Movie.2020.1080p.BluRay.x264-GROUP"
	orphanFile := writeVideoFile(t, filepath.Join(dir, orphanDir), "movie.mkv", 100)

	libStore := newTestLibraryStore(t)
	if _, err := libStore.Upsert(context.Background(), library.Item{
		Mode: mode.Movies, TMDBID: 42, Title: "Some Movie", FilePath: trackedFile, RootFolderPath: dir,
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	sess := &mode.Session{Mode: mode.Movies, TMDB: fakeTMDBSearch(t, tmdbTo42(orphanDir))}
	prober := &fakeProber{byPath: map[string]*mediainfo.Probe{
		trackedFile: {CodecName: "h264", Width: 1280, Height: 720, BitRate: 3000},
		orphanFile:  {CodecName: "h265", Width: 1920, Height: 1080, BitRate: 8000},
	}}
	// Empty byPath: every Hash call returns os.ErrNotExist, so attachPHashes
	// drops the entire group down to a 0-length slice.
	hasher := &fakePHasher{}

	got, err := ScanLibrary(context.Background(), sess, libStore, dir, prober, hasher, 2)
	if err != nil {
		t.Fatalf("unexpected error (must not panic): %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected no proposal when the whole group is uncomputable, got %+v", got)
	}
}

func TestScanLibrary_PHashUsesTrackedAsReference(t *testing.T) {
	dir := t.TempDir()
	trackedFile := writeVideoFile(t, filepath.Join(dir, "Some Movie (2020)"), "movie.mkv", 100)
	nearDir := "Some.Movie.2020.1080p.BluRay.x264-GROUP"
	farDir := "Some.Movie.2020.720p.WEB.x264-OTHER"
	nearFile := writeVideoFile(t, filepath.Join(dir, nearDir), "movie.mkv", 100)
	farFile := writeVideoFile(t, filepath.Join(dir, farDir), "movie.mkv", 100)

	libStore := newTestLibraryStore(t)
	tracked, err := libStore.Upsert(context.Background(), library.Item{
		Mode: mode.Movies, TMDBID: 42, Title: "Some Movie", FilePath: trackedFile, RootFolderPath: dir,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	sess := &mode.Session{Mode: mode.Movies, TMDB: fakeTMDBSearch(t, tmdbTo42(nearDir, farDir))}
	prober := &fakeProber{byPath: map[string]*mediainfo.Probe{
		trackedFile: {CodecName: "h264", Width: 1280, Height: 720, BitRate: 3000},
		nearFile:    {CodecName: "h265", Width: 1920, Height: 1080, BitRate: 8000},
		farFile:     {CodecName: "h264", Width: 1280, Height: 720, BitRate: 2000},
	}}
	// Both orphans are measured against the TRACKED reference: the near one is
	// kept, the far one dropped. If an orphan were the reference the outcome
	// would differ.
	hasher := &fakePHasher{byPath: map[string]string{trackedFile: refHash, nearFile: nearHash, farFile: farHash}}

	got, err := ScanLibrary(context.Background(), sess, libStore, dir, prober, hasher, 2)
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

func TestScanLibrary_PHashCacheReusedWhenIdentityMatches(t *testing.T) {
	dir := t.TempDir()
	trackedFile := writeVideoFile(t, filepath.Join(dir, "Some Movie (2020)"), "movie.mkv", 100)
	orphanDir := "Some.Movie.2020.1080p.BluRay.x264-GROUP"
	orphanFile := writeVideoFile(t, filepath.Join(dir, orphanDir), "movie.mkv", 100)

	// Seed the tracked item's cache with a hash keyed to the file's CURRENT
	// identity (same helper ScanLibrary uses), so the identity check matches.
	size, mtime, err := fileIdentity(trackedFile)
	if err != nil {
		t.Fatalf("stat tracked file: %v", err)
	}
	libStore := newTestLibraryStore(t)
	if _, err := libStore.Upsert(context.Background(), library.Item{
		Mode: mode.Movies, TMDBID: 42, Title: "Some Movie", FilePath: trackedFile, RootFolderPath: dir,
		PHash: refHash, PHashFileSize: size, PHashFileMTime: mtime,
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	sess := &mode.Session{Mode: mode.Movies, TMDB: fakeTMDBSearch(t, tmdbTo42(orphanDir))}
	prober := &fakeProber{byPath: map[string]*mediainfo.Probe{
		trackedFile: {CodecName: "h264", Width: 1280, Height: 720, BitRate: 3000},
		orphanFile:  {CodecName: "h265", Width: 1920, Height: 1080, BitRate: 8000},
	}}
	// trackedFile is present in byPath only as a safety net — the cache should
	// mean it's never asked for.
	hasher := &fakePHasher{byPath: map[string]string{trackedFile: refHash, orphanFile: nearHash}}

	got, err := ScanLibrary(context.Background(), sess, libStore, dir, prober, hasher, 2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || len(got[0].Candidates) != 2 {
		t.Fatalf("expected a 2-candidate group with the cached tracked hash reused, got %+v", got)
	}
	if hasher.calls[trackedFile] != 0 {
		t.Errorf("expected the cached tracked item NOT to be re-hashed (decode-once), got %d calls", hasher.calls[trackedFile])
	}
	if hasher.calls[orphanFile] != 1 {
		t.Errorf("expected the uncached orphan to be hashed exactly once, got %d calls", hasher.calls[orphanFile])
	}
}
