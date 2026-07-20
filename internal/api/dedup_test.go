package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mediainfo"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/proposals"
)

func writeTestVideoFile(t *testing.T, dir, name string, size int) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, make([]byte, size), 0o644); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
	return path
}

// fakeDedupProber maps a video file path to a canned mediainfo.Probe result
// — Dedup always ffprobes real files directly (see internal/dedup's doc
// comment), so end-to-end tests fake that one boundary rather than needing
// a real ffprobe binary and real encoded video content.
type fakeDedupProber struct {
	byPath map[string]*mediainfo.Probe
}

func (f *fakeDedupProber) Probe(ctx context.Context, path string) (*mediainfo.Probe, error) {
	p, ok := f.byPath[path]
	if !ok {
		return nil, os.ErrNotExist
	}
	return p, nil
}

// TestDedupWorkflow_ScanThenApply_EndToEnd exercises the full Dedup loop for
// Movies against SAK's real HTTP handlers, a real migrated SQLite database,
// a fake TMDB, and real on-disk files — no Radarr involved anymore.
func TestDedupWorkflow_ScanThenApply_EndToEnd(t *testing.T) {
	dir := t.TempDir()
	trackedDir := filepath.Join(dir, "Some Movie (2020)")
	orphanDir := filepath.Join(dir, "Some.Movie.2020.1080p.BluRay.x264-GROUP")
	trackedFile := writeTestVideoFile(t, trackedDir, "movie.mkv", 10)
	orphanFile := writeTestVideoFile(t, orphanDir, "movie.mkv", 10)

	fakeTMDB := httptest.NewServer(fakeTMDBSearchHandler(t, 42, "Some Movie"))
	defer fakeTMDB.Close()

	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	ctx := context.Background()
	overrideFixedURL(t, "tmdb", fakeTMDB.URL)
	if err := connStore.Upsert(ctx, "tmdb", fakeTMDB.URL, "test-key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := settingsStore.Set(ctx, moviesLibraryRootFolderKey, dir); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	tracked, err := libStore.Upsert(ctx, library.Item{
		Mode: mode.Movies, TMDBID: 42, Title: "Some Movie", FilePath: trackedFile, RootFolderPath: dir,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	prober := &fakeDedupProber{byPath: map[string]*mediainfo.Probe{
		trackedFile: {CodecName: "h264", Width: 1280, Height: 720, BitRate: 3000},
		orphanFile:  {CodecName: "h265", Width: 1920, Height: 1080, BitRate: 8000},
	}}
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, prober, testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore, nil, nil, nil, nil))
	defer srv.Close()

	scanResp, err := http.Post(srv.URL+"/api/modes/movies/dedup/scan", "application/json", nil)
	if err != nil {
		t.Fatalf("scan POST failed: %v", err)
	}
	defer scanResp.Body.Close()
	if scanResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 from scan, got %d", scanResp.StatusCode)
	}
	var scanned []proposals.Proposal
	json.NewDecoder(scanResp.Body).Decode(&scanned)
	if len(scanned) != 1 || len(scanned[0].Candidates) != 2 {
		t.Fatalf("unexpected scan result: %+v", scanned)
	}

	listResp, err := http.Get(srv.URL + "/api/modes/movies/dedup/proposals")
	if err != nil {
		t.Fatalf("list proposals failed: %v", err)
	}
	defer listResp.Body.Close()
	var listed []proposals.Proposal
	json.NewDecoder(listResp.Body).Decode(&listed)
	if len(listed) != 1 || listed[0].ID != scanned[0].ID {
		t.Fatalf("expected the dedup queue to reflect what scan staged, got %+v", listed)
	}

	// Apply with no body: auto-resolve by quality (the precomputed winner —
	// the higher-resolution orphan, which isn't tracked yet).
	applyResp, err := http.Post(
		srv.URL+"/api/proposals/"+strconv.FormatInt(scanned[0].ID, 10)+"/apply", "application/json", nil)
	if err != nil {
		t.Fatalf("apply POST failed: %v", err)
	}
	defer applyResp.Body.Close()
	if applyResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(applyResp.Body)
		t.Fatalf("expected 200 from apply, got %d: %s", applyResp.StatusCode, body)
	}
	var applied proposals.Proposal
	json.NewDecoder(applyResp.Body).Decode(&applied)
	if applied.Status != proposals.Applied {
		t.Fatalf("expected the proposal to come back Applied, got %+v", applied)
	}
	if _, err := os.Stat(trackedFile); !os.IsNotExist(err) {
		t.Errorf("expected the losing tracked candidate's file to be deleted directly (no *arr app to ask anymore), got: %v", err)
	}
	if _, err := libStore.Get(ctx, tracked.ID); err != library.ErrNotFound {
		t.Errorf("expected the losing tracked candidate's library record to be deleted, got err=%v", err)
	}
	if _, err := os.Stat(orphanFile); err != nil {
		t.Errorf("expected the winning orphan's file to remain in place, got: %v", err)
	}
}
