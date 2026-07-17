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

	"github.com/curtiswtaylorjr/sakms/internal/library"
	"github.com/curtiswtaylorjr/sakms/internal/mediainfo"
	"github.com/curtiswtaylorjr/sakms/internal/proposals"
)

// fakeTMDBTVSearchHandler serves TMDB's /search/tv endpoint with one canned
// result — Dedup's Series path never calls /tv/{id}/season/{n} (unlike
// Rename's), so this is the only endpoint the fake needs.
func fakeTMDBTVSearchHandler(t *testing.T, tmdbID int, name string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/search/tv" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"results": []map[string]any{
			{"id": tmdbID, "name": name},
		}})
	}
}

// TestDedupWorkflow_Series_ScanThenApply_EndToEnd mirrors
// TestDedupWorkflow_ScanThenApply_EndToEnd (Movies) but for Series' own
// episode-level dedup — no Sonarr involved anywhere.
func TestDedupWorkflow_Series_ScanThenApply_EndToEnd(t *testing.T) {
	dir := t.TempDir()
	trackedFile := writeTestVideoFile(t, filepath.Join(dir, "Some Show", "Season 01"), "Some Show - S01E01.mkv", 10)
	orphanFile := writeTestVideoFile(t, dir, "Some.Show.S01E01.1080p.BluRay.x264-GROUP.mkv", 10)

	fakeTMDB := httptest.NewServer(fakeTMDBTVSearchHandler(t, 555, "Some Show"))
	defer fakeTMDB.Close()

	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	ctx := context.Background()
	overrideFixedURL(t, "tmdb", fakeTMDB.URL)
	if err := connStore.Upsert(ctx, "tmdb", fakeTMDB.URL, "test-key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := settingsStore.Set(ctx, seriesLibraryRootFolderKey, dir); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	series, err := libStore.UpsertSeries(ctx, library.Series{TMDBID: 555, Title: "Some Show", RootFolderPath: dir})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	tracked, err := libStore.UpsertEpisode(ctx, library.Episode{
		SeriesID: series.ID, SeasonNumber: 1, EpisodeNumber: 1, FilePath: trackedFile,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	prober := &fakeDedupProber{byPath: map[string]*mediainfo.Probe{
		trackedFile: {CodecName: "h264", Width: 1280, Height: 720, BitRate: 3000},
		orphanFile:  {CodecName: "h265", Width: 1920, Height: 1080, BitRate: 8000},
	}}
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, prober, testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore, nil, nil))
	defer srv.Close()

	scanResp, err := http.Post(srv.URL+"/api/modes/series/dedup/scan", "application/json", nil)
	if err != nil {
		t.Fatalf("scan POST failed: %v", err)
	}
	defer scanResp.Body.Close()
	if scanResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 from scan, got %d", scanResp.StatusCode)
	}
	var scanned []proposals.Proposal
	json.NewDecoder(scanResp.Body).Decode(&scanned)
	if len(scanned) != 1 || len(scanned[0].Candidates) != 2 || scanned[0].SeasonNumber != 1 || scanned[0].EpisodeNumber != 1 {
		t.Fatalf("unexpected scan result: %+v", scanned)
	}

	listResp, err := http.Get(srv.URL + "/api/modes/series/dedup/proposals")
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
		t.Errorf("expected the losing tracked candidate's file to be deleted directly (no Sonarr to ask), got: %v", err)
	}
	if _, err := os.Stat(orphanFile); err != nil {
		t.Errorf("expected the winning orphan's file to remain in place, got: %v", err)
	}

	ep, err := libStore.GetEpisode(ctx, series.ID, 1, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ep.ID != tracked.ID {
		t.Errorf("expected the same episode row id to be reused, got %d (was %d)", ep.ID, tracked.ID)
	}
	if ep.FilePath != orphanFile {
		t.Errorf("expected the episode row's file path updated to the winning orphan, got %+v", ep)
	}
}

// TestDedupWorkflow_Series_SeasonPack_ScanFindsGroupedDuplicate proves the
// season-pack grouping works through the real HTTP handlers too, not just
// at the package level.
func TestDedupWorkflow_Series_SeasonPack_ScanFindsGroupedDuplicate(t *testing.T) {
	dir := t.TempDir()
	trackedFile := writeTestVideoFile(t, filepath.Join(dir, "Some Show", "Season 01"), "Some Show - S01E01.mkv", 10)
	packDir := filepath.Join(dir, "Some.Show.Season.01.1080p.WEB-DL-GROUP")
	packEp1 := writeTestVideoFile(t, packDir, "Some.Show.S01E01.mkv", 10)
	packEp2 := writeTestVideoFile(t, packDir, "Some.Show.S01E02.mkv", 10)

	fakeTMDB := httptest.NewServer(fakeTMDBTVSearchHandler(t, 555, "Some Show"))
	defer fakeTMDB.Close()

	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	ctx := context.Background()
	overrideFixedURL(t, "tmdb", fakeTMDB.URL)
	if err := connStore.Upsert(ctx, "tmdb", fakeTMDB.URL, "test-key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := settingsStore.Set(ctx, seriesLibraryRootFolderKey, dir); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	series, err := libStore.UpsertSeries(ctx, library.Series{TMDBID: 555, Title: "Some Show", RootFolderPath: dir})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := libStore.UpsertEpisode(ctx, library.Episode{
		SeriesID: series.ID, SeasonNumber: 1, EpisodeNumber: 1, FilePath: trackedFile,
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	prober := &fakeDedupProber{byPath: map[string]*mediainfo.Probe{
		trackedFile: {CodecName: "h264", Width: 1280, Height: 720, BitRate: 3000},
		packEp1:     {CodecName: "h265", Width: 1920, Height: 1080, BitRate: 8000},
		packEp2:     {CodecName: "h265", Width: 1920, Height: 1080, BitRate: 8000},
	}}
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, prober, testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore, nil, nil))
	defer srv.Close()

	scanResp, err := http.Post(srv.URL+"/api/modes/series/dedup/scan", "application/json", nil)
	if err != nil {
		t.Fatalf("scan POST failed: %v", err)
	}
	defer scanResp.Body.Close()
	if scanResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 from scan, got %d", scanResp.StatusCode)
	}
	var scanned []proposals.Proposal
	json.NewDecoder(scanResp.Body).Decode(&scanned)
	// Episode 1 (in the pack) duplicates the tracked copy; episode 2 (also
	// in the pack) is a lone new orphan, not reported.
	if len(scanned) != 1 || scanned[0].EpisodeNumber != 1 || len(scanned[0].Candidates) != 2 {
		t.Fatalf("unexpected scan result: %+v", scanned)
	}
}
