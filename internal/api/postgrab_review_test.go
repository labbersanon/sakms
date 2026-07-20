package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/labbersanon/sakms/internal/downloader"
	"github.com/labbersanon/sakms/internal/grabs"
	"github.com/labbersanon/sakms/internal/mediainfo"
	"github.com/labbersanon/sakms/internal/mode"
)

// fixedProber is a dedup.Prober that returns the same probed duration for any
// path — the post-grab review's imported file lands at a runtime-computed
// (naming-preset) path we can't pre-key a map on, and all we need to exercise
// is the runtime-mismatch flag, not per-path behavior.
type fixedProber struct{ durationSeconds float64 }

func (f fixedProber) Probe(ctx context.Context, path string) (*mediainfo.Probe, error) {
	return &mediainfo.Probe{Duration: f.durationSeconds}, nil
}

// fakeCompletedDownloader returns a download Manager backed by a fake aria2
// that reports the download "abc123" as complete, staged at contentPath — the
// setup a check-import test needs for an already-finished grab.
func fakeCompletedDownloader(t *testing.T, contentPath string) *downloader.Manager {
	t.Helper()
	dl := newTestDownloader("abc123", t.TempDir())
	seedComplete(dl, "abc123", contentPath)
	return dl
}

// TestPostGrabReview_Movies_RuntimeMismatchFlags proves the post-grab mislabel
// check flags a Movies grab whose imported file's actual runtime is wildly
// inconsistent with TMDB's listed movie runtime. (No prior test exercised
// postGrabRuntimeReview's flag path for any mode — this establishes the Movies
// baseline the Series wiring mirrors.)
func TestPostGrabReview_Movies_RuntimeMismatchFlags(t *testing.T) {
	dir := t.TempDir()
	downloadDir := filepath.Join(dir, "downloads", "Some.Movie.2023.1080p.WEB-DL.x264-GROUP")
	moviesRoot := filepath.Join(dir, "Movies")
	if err := os.MkdirAll(downloadDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.MkdirAll(moviesRoot, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(downloadDir, "movie.mkv"), []byte("fake video"), 0o644); err != nil {
		t.Fatalf("writing file: %v", err)
	}

	dl := fakeCompletedDownloader(t, downloadDir)
	tmdbSrv := fakeTMDBMovieRuntime(t, 100) // TMDB says 100 min = 6000 s

	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	ctx := context.Background()
	overrideFixedURL(t, "tmdb", tmdbSrv.URL)
	if err := connStore.Upsert(ctx, "tmdb", tmdbSrv.URL, "key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	g, err := grabsStore.Create(ctx, grabs.Grab{
		Mode: mode.Movies, Title: "Some Movie", TMDBID: 42,
		Indexer: "I", Protocol: "torrent", DownloadClient: "aria2",
		DownloadGID: "abc123", RootFolderPath: moviesRoot,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Imported file runs 20 min against a 100-min listing → ratio 0.2, well
	// outside the [0.70, 1.30] band → flagged.
	prober := fixedProber{durationSeconds: 20 * 60}
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, prober, testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore, nil, nil, dl, nil, nil))
	defer srv.Close()

	updated := postCheckImport(t, srv.URL, g.ID)
	if updated.Status != grabs.Imported {
		t.Fatalf("expected status Imported, got %q", updated.Status)
	}
	if !updated.FlaggedForReview {
		t.Fatalf("expected the Movies grab to be flagged for review on a runtime mismatch, got flag=%v reason=%q", updated.FlaggedForReview, updated.FlagReason)
	}
}

// TestPostGrabReview_Series_SingleEpisode_RuntimeMismatchFlags is the payoff of
// this task: a single-episode Series grab whose imported file's runtime
// mismatches the picked episode's TMDB runtime is now flagged for review, the
// same safety net Movies already got — closing the drift where the check
// skipped Series on a doc comment claiming per-episode runtime couldn't be
// fetched (seriesEpisodeRuntimeSeconds fetches exactly that).
func TestPostGrabReview_Series_SingleEpisode_RuntimeMismatchFlags(t *testing.T) {
	dir := t.TempDir()
	downloadDir := filepath.Join(dir, "downloads", "Some.Show.S01E01.1080p.WEB-DL.x264-GROUP")
	tvRoot := filepath.Join(dir, "TV")
	if err := os.MkdirAll(downloadDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.MkdirAll(tvRoot, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(downloadDir, "Some.Show.S01E01.mkv"), []byte("fake video"), 0o644); err != nil {
		t.Fatalf("writing file: %v", err)
	}

	dl := fakeCompletedDownloader(t, downloadDir)
	tmdbSrv := fakeTMDBSeriesRuntime(t, 1, 58) // season 1 episode 1, 58 min = 3480 s

	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	ctx := context.Background()
	overrideFixedURL(t, "tmdb", tmdbSrv.URL)
	if err := connStore.Upsert(ctx, "tmdb", tmdbSrv.URL, "key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	g, err := grabsStore.Create(ctx, grabs.Grab{
		Mode: mode.Series, Title: "Some Show", TMDBID: 555, SeasonNumber: 1, EpisodeNumber: 1, SeasonSpecified: true,
		Indexer: "I", Protocol: "torrent", DownloadClient: "aria2",
		DownloadGID: "abc123", RootFolderPath: tvRoot,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Imported file runs 10 min against a 58-min episode → ratio 0.17 → flagged.
	prober := fixedProber{durationSeconds: 10 * 60}
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, prober, testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore, nil, nil, dl, nil, nil))
	defer srv.Close()

	updated := postCheckImport(t, srv.URL, g.ID)
	if updated.Status != grabs.Imported {
		t.Fatalf("expected status Imported, got %q", updated.Status)
	}
	if !updated.FlaggedForReview {
		t.Fatalf("expected the single-episode Series grab to be flagged for review on a runtime mismatch, got flag=%v reason=%q", updated.FlaggedForReview, updated.FlagReason)
	}
}

// TestPostGrabReview_Series_SeasonPack_Skips proves a whole-season grab
// (EpisodeNumber == 0) is NEVER flagged, even when the imported file's runtime
// would mismatch a single episode's: a season pack has no single per-file
// runtime to check against, so seriesEpisodeRuntimeSeconds returns 0 and the
// review skips — consistent with the pre-grab scorer's own "unknown-bitrate"
// treatment of packs, never a false mismatch. The single relocated file keeps
// len(changes)==1, so it is specifically the EpisodeNumber==0 gate under test.
func TestPostGrabReview_Series_SeasonPack_Skips(t *testing.T) {
	dir := t.TempDir()
	downloadDir := filepath.Join(dir, "downloads", "Some.Show.S01.1080p.WEB-DL.x264-GROUP")
	tvRoot := filepath.Join(dir, "TV")
	if err := os.MkdirAll(downloadDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.MkdirAll(tvRoot, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// A single episode file inside a season-pack-shaped grab: it parses to
	// S01E01 on its own, so exactly one episode is recorded (len(changes)==1),
	// isolating the EpisodeNumber==0 skip from the multi-file len gate.
	if err := os.WriteFile(filepath.Join(downloadDir, "Some.Show.S01E01.mkv"), []byte("fake video"), 0o644); err != nil {
		t.Fatalf("writing file: %v", err)
	}

	dl := fakeCompletedDownloader(t, downloadDir)
	tmdbSrv := fakeTMDBSeriesRuntime(t, 1, 58) // would be 3480 s if a single episode were checked

	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	ctx := context.Background()
	overrideFixedURL(t, "tmdb", tmdbSrv.URL)
	if err := connStore.Upsert(ctx, "tmdb", tmdbSrv.URL, "key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// EpisodeNumber 0 → whole-season grab.
	g, err := grabsStore.Create(ctx, grabs.Grab{
		Mode: mode.Series, Title: "Some Show", TMDBID: 555, SeasonNumber: 1, EpisodeNumber: 0, SeasonSpecified: true,
		Indexer: "I", Protocol: "torrent", DownloadClient: "aria2",
		DownloadGID: "abc123", RootFolderPath: tvRoot,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// A duration that WOULD flag if a single-episode runtime were (wrongly)
	// applied — proving the skip is the EpisodeNumber gate, not a lucky match.
	prober := fixedProber{durationSeconds: 10 * 60}
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, prober, testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore, nil, nil, dl, nil, nil))
	defer srv.Close()

	updated := postCheckImport(t, srv.URL, g.ID)
	if updated.Status != grabs.Imported {
		t.Fatalf("expected status Imported, got %q", updated.Status)
	}
	if updated.FlaggedForReview {
		t.Fatalf("a season-pack grab (EpisodeNumber 0) must never be flagged on a single-episode runtime mismatch, got reason=%q", updated.FlagReason)
	}
}

// postCheckImport POSTs a grab's check-import and decodes the updated grab.
func postCheckImport(t *testing.T, baseURL string, grabID int64) grabs.Grab {
	t.Helper()
	resp, err := http.Post(baseURL+"/api/grabs/"+strconv.FormatInt(grabID, 10)+"/check-import", "application/json", nil)
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var updated grabs.Grab
	if err := json.NewDecoder(resp.Body).Decode(&updated); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	return updated
}
