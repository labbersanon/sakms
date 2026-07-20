package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/labbersanon/sakms/internal/downloader"
	"github.com/labbersanon/sakms/internal/grabs"
	"github.com/labbersanon/sakms/internal/mode"
)

// TestDownloadContentPath covers the derivation aria2's real (files-populated)
// responses drive — the production path, not the dir-only fallback the
// check-import tests happen to exercise.
func TestDownloadContentPath(t *testing.T) {
	staging := "/staging"
	cases := []struct {
		name    string
		files   []string
		dir     string
		staging string
		want    string
	}{
		{
			name:    "multi-file under a per-torrent subfolder → relocate the subfolder",
			files:   []string{"/staging/Show.S01/e01.mkv", "/staging/Show.S01/e02.mkv"},
			dir:     "/staging/Show.S01",
			staging: staging,
			want:    "/staging/Show.S01",
		},
		{
			name:    "single file dropped directly in staging → relocate the file",
			files:   []string{"/staging/movie.mkv"},
			dir:     staging,
			staging: staging,
			want:    "/staging/movie.mkv",
		},
		{
			name:    "single file in a per-torrent subfolder → relocate the subfolder",
			files:   []string{"/staging/Movie.2023/movie.mkv"},
			dir:     "/staging/Movie.2023",
			staging: staging,
			want:    "/staging/Movie.2023",
		},
		{
			name:    "no files reported → fall back to the reported dir",
			files:   nil,
			dir:     "/staging/whatever",
			staging: staging,
			want:    "/staging/whatever",
		},
		{
			name:    "trailing-slash staging still matches (single file at root)",
			files:   []string{"/staging/movie.mkv"},
			dir:     "/staging/",
			staging: "/staging/",
			want:    "/staging/movie.mkv",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := downloadContentPath(tc.files, tc.dir, tc.staging)
			if got != tc.want {
				t.Errorf("downloadContentPath(%v, %q, %q) = %q, want %q", tc.files, tc.dir, tc.staging, got, tc.want)
			}
		})
	}
}

// TestCheckImportHandler_MultiFileTorrent_RelocatesWholeFolder is the
// end-to-end guard the advisor asked for: a completed download whose aria2
// status reports individual files under a per-torrent subfolder must relocate
// the WHOLE folder (both episodes), not just files[0]. This exercises the
// production files[] branch that setCompleteDir's dir-only fixtures don't.
func TestCheckImportHandler_MultiFileTorrent_RelocatesWholeFolder(t *testing.T) {
	dir := t.TempDir()
	staging := filepath.Join(dir, "staging")
	pack := filepath.Join(staging, "Some.Show.S01.1080p.WEB-DL")
	tvRoot := filepath.Join(dir, "TV")
	for _, d := range []string{pack, tvRoot} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}
	e1 := filepath.Join(pack, "Some.Show.S01E01.mkv")
	e2 := filepath.Join(pack, "Some.Show.S01E02.mkv")
	for _, f := range []string{e1, e2} {
		if err := os.WriteFile(f, []byte("fake video"), 0o644); err != nil {
			t.Fatalf("writing file: %v", err)
		}
	}

	dl := newTestDownloader("packgid", staging)
	dl.SeedState(downloader.Download{
		GID: "packgid", Status: "complete", Dir: pack,
		Files: []string{e1, e2},
	})

	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	ctx := context.Background()

	g, err := grabsStore.Create(ctx, grabs.Grab{
		Mode: mode.Series, Title: "Some Show", TMDBID: 777, SeasonNumber: 1, SeasonSpecified: true,
		Indexer: "I", Protocol: "torrent", DownloadClient: "aria2",
		DownloadGID: "packgid", RootFolderPath: tvRoot,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore, nil, nil, dl, nil, nil))
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/api/grabs/"+strconv.FormatInt(g.ID, 10)+"/check-import", "application/json", nil)
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	// Both episodes must be recorded — the bug was that only files[0] moved,
	// leaving episode 2 orphaned in staging and untracked.
	series, err := libStore.GetSeriesByTMDBID(ctx, 777)
	if err != nil {
		t.Fatalf("expected the series recorded, got err=%v", err)
	}
	episodes, err := libStore.ListEpisodes(ctx, series.ID)
	if err != nil {
		t.Fatalf("listing episodes: %v", err)
	}
	if len(episodes) != 2 {
		t.Fatalf("expected BOTH episodes recorded (whole-folder relocate), got %d: %+v", len(episodes), episodes)
	}
	// Both relocated files must exist on disk under the TV root.
	for _, name := range []string{"Some.Show.S01E01.mkv", "Some.Show.S01E02.mkv"} {
		if _, err := os.Stat(filepath.Join(tvRoot, "Some.Show.S01.1080p.WEB-DL", name)); err != nil {
			t.Errorf("expected %s relocated under the TV root, got err=%v", name, err)
		}
	}
}
