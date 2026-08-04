package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/labbersanon/sakms/internal/downloader"
	"github.com/labbersanon/sakms/internal/grabs"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/rename"
)

// importCopyDirName mirrors the downloader package's unexported importDirName.
// The api layer only ever learns the per-gid root through Manager.ImportRoot,
// so this literal exists solely to assert paths in tests.
const importCopyDirName = ".import"

// T-3.4 — handed the per-gid import root, downloadContentPath keeps its
// single-file vs multi-file verdict: identical outcomes to the seeding-off
// case, just rooted at .import/<gid>/ instead of the staging dir.
func TestDownloadContentPath_PerGIDImportRoot(t *testing.T) {
	root := "/staging/.import/abc123"
	cases := []struct {
		name  string
		files []string
		dir   string
		want  string
	}{
		{
			name:  "single file directly in the import root → relocate the file",
			files: []string{root + "/movie.mkv"},
			dir:   root,
			want:  root + "/movie.mkv",
		},
		{
			name:  "multi-file under a mirrored subfolder → relocate the subfolder",
			files: []string{root + "/Pack/e01.mkv", root + "/Pack/e02.mkv"},
			dir:   root + "/Pack",
			want:  root + "/Pack",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := downloadContentPath(tc.files, tc.dir, root); got != tc.want {
				t.Errorf("downloadContentPath(%v, %q, %q) = %q, want %q", tc.files, tc.dir, root, got, tc.want)
			}
		})
	}

	// The bug this exists to prevent: comparing against the GLOBAL staging dir
	// while handed import-copy paths turns a single-file torrent into a
	// directory relocate.
	if got := downloadContentPath([]string{root + "/movie.mkv"}, root, "/staging"); got != root {
		t.Fatalf("sanity: comparing against the global staging dir should return the directory, got %q", got)
	}
}

// T-3.2 — spec Constraint #2: the import flow is byte-for-byte unaffected by
// the inversion. Relocating the import COPY produces the same library path
// relocating the original would, and leaves the seeding original in place.
func TestRelocate_ImportCopy_MatchesSeedingOffOutcome(t *testing.T) {
	base := t.TempDir()
	staging := filepath.Join(base, "staging")
	copyPath := filepath.Join(staging, importCopyDirName, "gid1", "Some.Movie.2023.mkv")
	originalPath := filepath.Join(staging, "Some.Movie.2023.mkv")
	seedingOffPath := filepath.Join(base, "staging-off", "Some.Movie.2023.mkv")
	for _, p := range []string{copyPath, originalPath, seedingOffPath} {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(p, []byte("fake video"), 0o644); err != nil {
			t.Fatalf("writing %s: %v", p, err)
		}
	}

	withSeeding := filepath.Join(base, "libA")
	withoutSeeding := filepath.Join(base, "libB")
	for _, d := range []string{withSeeding, withoutSeeding} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}

	movedCopy, err := rename.Relocate(copyPath, withSeeding)
	if err != nil {
		t.Fatalf("relocating the import copy: %v", err)
	}
	movedOriginal, err := rename.Relocate(seedingOffPath, withoutSeeding)
	if err != nil {
		t.Fatalf("relocating with seeding off: %v", err)
	}

	relCopy, _ := filepath.Rel(withSeeding, movedCopy)
	relOriginal, _ := filepath.Rel(withoutSeeding, movedOriginal)
	if relCopy != relOriginal {
		t.Errorf("library path differs: %q with seeding vs %q without", relCopy, relOriginal)
	}
	if _, err := os.Stat(originalPath); err != nil {
		t.Errorf("the seeding original must survive the import: %v", err)
	}
}

// seedingGrabFixture stages a multi-file torrent as the seeding architecture
// leaves it after a completed download: the ORIGINALS in place under staging
// (still being served), and a per-gid import COPY the importer consumes.
type seedingGrabFixture struct {
	staging    string
	tvRoot     string
	gid        string
	importRoot string
	originals  []string
	copies     []string
	dl         *downloader.Manager
}

func newSeedingGrabFixture(t *testing.T) seedingGrabFixture {
	t.Helper()
	dir := t.TempDir()
	staging := filepath.Join(dir, "staging")
	tvRoot := filepath.Join(dir, "TV")
	gid := "seedgid"
	pack := "Some.Show.S01.1080p.WEB-DL"
	importRoot := filepath.Join(staging, importCopyDirName, gid)

	var originals, copies []string
	for _, name := range []string{"Some.Show.S01E01.mkv", "Some.Show.S01E02.mkv"} {
		originals = append(originals, filepath.Join(staging, pack, name))
		copies = append(copies, filepath.Join(importRoot, pack, name))
	}
	if err := os.MkdirAll(tvRoot, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	for _, p := range append(append([]string{}, originals...), copies...) {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(p, []byte("fake video"), 0o644); err != nil {
			t.Fatalf("writing %s: %v", p, err)
		}
	}

	dl := newTestDownloader(gid, staging)
	// The entry reports the ORIGINALS, exactly as the Manager does — the
	// import copy is reachable only through ImportPaths/ImportRoot. Seeding
	// the entry with the copies instead would let the UNFIXED handler pass.
	dl.SeedState(downloader.Download{
		GID: gid, Status: "complete", Dir: filepath.Join(staging, pack), Files: originals,
	})
	dl.SeedImportCopy(gid, importRoot, copies)

	return seedingGrabFixture{
		staging: staging, tvRoot: tvRoot, gid: gid, importRoot: importRoot,
		originals: originals, copies: copies, dl: dl,
	}
}

// T-3.9 PRIMARY — a grab whose automatic import FAILED while seeding is
// active. The row is left non-Imported, so the pre-existing already-Imported
// guard does not fire, and the operator's manual "check import" runs the
// relocate. Without §3.4.1's fix it relocates the ORIGINALS out from under the
// running seed; every other seeding test passes regardless.
func TestCheckImport_FailedAutoImport_ConsumesTheCopyAndLeavesTheSeedIntact(t *testing.T) {
	f := newSeedingGrabFixture(t)
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	ctx := context.Background()

	g, err := grabsStore.Create(ctx, grabs.Grab{
		Mode: mode.Series, Title: "Some Show", TMDBID: 778, SeasonNumber: 1, SeasonSpecified: true,
		Indexer: "I", Protocol: "torrent", DownloadClient: "aria2",
		DownloadGID: f.gid, RootFolderPath: f.tvRoot,
	})
	if err != nil {
		t.Fatalf("creating grab: %v", err)
	}

	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, nil, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, testFeedHealth(), rssFeedsStore, nil, nil, f.dl, nil, nil, nil, nil))
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/api/grabs/"+strconv.FormatInt(g.ID, 10)+"/check-import", "application/json", nil)
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	// (1) every ORIGINAL is still exactly where the torrent client wrote it.
	for _, p := range f.originals {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("the seeding original %s was moved out from under the torrent client: %v", p, err)
		}
	}

	// (2) exactly one copy of the content landed in the library.
	entries, err := os.ReadDir(f.tvRoot)
	if err != nil {
		t.Fatalf("reading the TV root: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected exactly one relocated pack under the TV root, got %d: %v", len(entries), entries)
	}
	imported, err := os.ReadDir(filepath.Join(f.tvRoot, entries[0].Name()))
	if err != nil {
		t.Fatalf("reading the relocated pack: %v", err)
	}
	if len(imported) != 2 {
		t.Errorf("expected both episodes relocated exactly once, got %d: %v", len(imported), imported)
	}

	updated, err := grabsStore.Get(ctx, g.ID)
	if err != nil {
		t.Fatalf("re-reading the grab: %v", err)
	}
	// (3) the path the import actually consumed was the per-gid import copy.
	if !strings.HasPrefix(updated.DownloadStagingPath, f.importRoot) {
		t.Errorf("the import consumed %q, want a path under the per-gid import root %q", updated.DownloadStagingPath, f.importRoot)
	}
	// (4) the grab transitions to Imported.
	if updated.Status != grabs.Imported {
		t.Errorf("grab status = %q, want %q", updated.Status, grabs.Imported)
	}
	// (5) the SEED itself was NOT torn down — the entry survives and its
	// ORIGINALS (asserted in (1) above) are untouched. NOT that upload is
	// still allowed, which is a different claim and is what T-6.2
	// (internal/downloader) asserts against Seeding().
	if d, _ := f.dl.FindByGID(f.gid); d == nil {
		t.Error("the download entry was dropped — the seed was torn down by a manual check-import")
	}
	// (6) but the now-empty per-gid IMPORT COPY (distinct from the seed
	// itself) must be reclaimed on success, the same cleanup the automatic
	// path (DownloadCompleteImporter) already does — a manual check-import
	// success must not leave an empty .import/<gid>/ tree behind.
	if got := f.dl.ImportPaths(f.gid); len(got) != 0 {
		t.Errorf("ImportPaths = %v, want nil — check-import must clear the consumed import copy on success", got)
	}
	if _, err := os.Stat(f.importRoot); !os.IsNotExist(err) {
		t.Errorf("import-copy dir %s still exists after a successful check-import, err=%v", f.importRoot, err)
	}
}

// T-3.9 SECONDARY — an already-Imported grab short-circuits. This exercises
// the PRE-EXISTING guard at the top of checkImportHandler, not §3.4.1's fix;
// it is a regression guard only.
func TestCheckImport_AlreadyImportedGrab_ShortCircuitsAndLeavesFilesAlone(t *testing.T) {
	f := newSeedingGrabFixture(t)
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	ctx := context.Background()

	g, err := grabsStore.Create(ctx, grabs.Grab{
		Mode: mode.Series, Title: "Some Show", TMDBID: 779, SeasonNumber: 1, SeasonSpecified: true,
		Indexer: "I", Protocol: "torrent", DownloadClient: "aria2",
		DownloadGID: f.gid, RootFolderPath: f.tvRoot,
	})
	if err != nil {
		t.Fatalf("creating grab: %v", err)
	}
	if err := grabsStore.UpdateStatus(ctx, g.ID, grabs.Imported); err != nil {
		t.Fatalf("marking imported: %v", err)
	}

	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, nil, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, testFeedHealth(), rssFeedsStore, nil, nil, f.dl, nil, nil, nil, nil))
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/api/grabs/"+strconv.FormatInt(g.ID, 10)+"/check-import", "application/json", nil)
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusConflict {
		t.Fatalf("expected the already-imported short-circuit (200) or the seeding refusal (409), got %d", resp.StatusCode)
	}

	for _, p := range f.originals {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("the seeding original %s must not be touched by a repeat check-import: %v", p, err)
		}
	}
	entries, err := os.ReadDir(f.tvRoot)
	if err != nil {
		t.Fatalf("reading the TV root: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("a short-circuited check-import must not relocate anything, got %v", entries)
	}
}

// The copy is recorded but already consumed by a prior import: re-running
// would relocate the seeding originals, so the handler refuses instead.
func TestCheckImport_ImportCopyAlreadyConsumed_Refuses409(t *testing.T) {
	f := newSeedingGrabFixture(t)
	if err := os.RemoveAll(f.importRoot); err != nil {
		t.Fatalf("removing the consumed import copy: %v", err)
	}
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	ctx := context.Background()

	g, err := grabsStore.Create(ctx, grabs.Grab{
		Mode: mode.Series, Title: "Some Show", TMDBID: 780, SeasonNumber: 1, SeasonSpecified: true,
		Indexer: "I", Protocol: "torrent", DownloadClient: "aria2",
		DownloadGID: f.gid, RootFolderPath: f.tvRoot,
	})
	if err != nil {
		t.Fatalf("creating grab: %v", err)
	}

	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, nil, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, testFeedHealth(), rssFeedsStore, nil, nil, f.dl, nil, nil, nil, nil))
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/api/grabs/"+strconv.FormatInt(g.ID, 10)+"/check-import", "application/json", nil)
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("expected 409, got %d", resp.StatusCode)
	}
	for _, p := range f.originals {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("the seeding original %s must survive the refusal: %v", p, err)
		}
	}
}
