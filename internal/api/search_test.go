package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"

	"github.com/labbersanon/sakms/internal/apidto"
	"github.com/labbersanon/sakms/internal/grabs"
	"github.com/labbersanon/sakms/internal/mode"
)

func fakeProwlarr(t *testing.T, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestCategoriesForSearch_AllThreeModes is a regression test for a real bug:
// categoriesForSearch had no mode.Adult case at all and fell through to the
// Movies default (2000) — so the manual Search screen silently searched
// Adult under the wrong Newznab category the whole time. Found while
// investigating a separate "Adult posters/downloads broken" report.
func TestCategoriesForSearch_AllThreeModes(t *testing.T) {
	cases := []struct {
		mode mode.Mode
		want []int
	}{
		{mode.Movies, []int{2000}},
		{mode.Series, []int{5000}},
		{mode.Adult, []int{adultAutoGrabCategory}},
	}
	for _, tc := range cases {
		got := categoriesForSearch(tc.mode)
		if len(got) != len(tc.want) || got[0] != tc.want[0] {
			t.Errorf("categoriesForSearch(%s) = %v, want %v", tc.mode, got, tc.want)
		}
	}
}

func TestSearchHandler_ScoresAndSortsResults(t *testing.T) {
	fake := fakeProwlarr(t, `[
		{"guid":"1","title":"Some.Movie.2023.480p.HDTV.x264-GROUP","indexer":"I","protocol":"torrent","size":1,"seeders":1,"downloadUrl":"http://x/1","publishDate":"2023-01-01"},
		{"guid":"2","title":"Some.Movie.2023.1080p.WEB-DL.x265-GROUP","indexer":"I","protocol":"torrent","size":2,"seeders":2,"downloadUrl":"http://x/2","publishDate":"2023-01-02"}
	]`)

	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	ctx := context.Background()
	if err := connStore.Upsert(ctx, "radarr", "http://radarr.local", "key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := connStore.Upsert(ctx, "prowlarr", fake.URL, "key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, nil, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, testFeedHealth(), rssFeedsStore, nil, nil, nil, nil, nil, nil, nil, nil))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/modes/movies/search?q=Some+Movie")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var results []apidto.SearchReleaseResult
	if err := json.NewDecoder(resp.Body).Decode(&results); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if results[0].GUID != "2" {
		t.Errorf("expected the 1080p WEB-DL release scored first, got %+v", results[0])
	}
}

// TestSearchHandler_DedupsCrossPostsKeepsHighestSeeder proves the manual Search
// screen now dedups (it had none before): two releases with the same title but
// different non-empty download URLs and different seeders — the confirmed
// root-cause cross-post case — collapse to exactly one result, the higher-seeder
// one. Run in a NON-Adult mode (Movies) to cover the spec's "applies universally"
// constraint beyond Adult.
func TestSearchHandler_DedupsCrossPostsKeepsHighestSeeder(t *testing.T) {
	fake := fakeProwlarr(t, `[
		{"guid":"1","title":"Some.Movie.2023.1080p.WEB-DL.x265-GROUP","indexer":"I","protocol":"torrent","size":1,"seeders":3,"downloadUrl":"http://x/1","publishDate":"2023-01-01"},
		{"guid":"2","title":"Some.Movie.2023.1080p.WEB-DL.x265-GROUP","indexer":"J","protocol":"torrent","size":1,"seeders":9,"downloadUrl":"http://y/2","publishDate":"2023-01-01"}
	]`)

	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	if err := connStore.Upsert(context.Background(), "prowlarr", fake.URL, "key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, nil, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, testFeedHealth(), rssFeedsStore, nil, nil, nil, nil, nil, nil, nil, nil))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/modes/movies/search?q=Some+Movie")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var results []apidto.SearchReleaseResult
	if err := json.NewDecoder(resp.Body).Decode(&results); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected the cross-post duplicates collapsed to 1 result, got %d (%+v)", len(results), results)
	}
	if results[0].Seeders != 9 || results[0].GUID != "2" {
		t.Errorf("expected the higher-seeder (9) cross-post to survive, got %+v", results[0])
	}
}

// TestSearchHandler_DistinctQualityVariantsBothReturned guards against
// over-collapsing on the Search path: two releases with genuinely different
// resolution tokens (1080p vs 2160p) are distinct pickable options and must both
// be returned and scored.
func TestSearchHandler_DistinctQualityVariantsBothReturned(t *testing.T) {
	fake := fakeProwlarr(t, `[
		{"guid":"1","title":"Some.Movie.2023.1080p.WEB-DL.x265-GROUP","indexer":"I","protocol":"torrent","size":1,"seeders":3,"downloadUrl":"http://x/1","publishDate":"2023-01-01"},
		{"guid":"2","title":"Some.Movie.2023.2160p.WEB-DL.x265-GROUP","indexer":"J","protocol":"torrent","size":2,"seeders":3,"downloadUrl":"http://y/2","publishDate":"2023-01-01"}
	]`)

	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	if err := connStore.Upsert(context.Background(), "prowlarr", fake.URL, "key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, nil, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, testFeedHealth(), rssFeedsStore, nil, nil, nil, nil, nil, nil, nil, nil))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/modes/movies/search?q=Some+Movie")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	var results []apidto.SearchReleaseResult
	if err := json.NewDecoder(resp.Body).Decode(&results); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected both distinct-quality variants returned, got %d (%+v)", len(results), results)
	}
}

// TestSearchHandler_FiresExactlyOneProwlarrSearch is the release-picker's core
// invariant (Movies/Series two-step): one card click ⇒ exactly ONE Prowlarr
// search, never more (no per-scroll/per-render re-query).
func TestSearchHandler_FiresExactlyOneProwlarrSearch(t *testing.T) {
	var prowlarrCalls int32
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&prowlarrCalls, 1)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[{"guid":"1","title":"Some.Movie.2023.1080p.WEB-DL.x265-GROUP","indexer":"I","protocol":"torrent","size":1,"seeders":1,"downloadUrl":"http://x/1","publishDate":"2023-01-01"}]`))
	}))
	defer fake.Close()

	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	if err := connStore.Upsert(context.Background(), "prowlarr", fake.URL, "key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, nil, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, testFeedHealth(), rssFeedsStore, nil, nil, nil, nil, nil, nil, nil, nil))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/modes/movies/search?q=Some+Movie")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if got := atomic.LoadInt32(&prowlarrCalls); got != 1 {
		t.Fatalf("expected EXACTLY ONE Prowlarr.Search per search call, got %d", got)
	}
}

func TestSearchHandler_RequiresQuery(t *testing.T) {
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, nil, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, testFeedHealth(), rssFeedsStore, nil, nil, nil, nil, nil, nil, nil, nil))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/modes/movies/search")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 without a q param, got %d", resp.StatusCode)
	}
}

func TestSearchHandler_ProwlarrNotConfigured(t *testing.T) {
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	if err := connStore.Upsert(context.Background(), "radarr", "http://radarr.local", "key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, nil, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, testFeedHealth(), rssFeedsStore, nil, nil, nil, nil, nil, nil, nil, nil))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/modes/movies/search?q=x")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 when prowlarr isn't configured, got %d", resp.StatusCode)
	}
}

func TestGrabHandler_Torrent_SendsToAria2AndRecordsGrab(t *testing.T) {
	dl := newTestDownloader("gid-abc", t.TempDir())

	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)

	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, nil, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, testFeedHealth(), rssFeedsStore, nil, nil, dl, nil, nil, nil, nil, nil))
	defer srv.Close()

	magnet := "magnet:?xt=urn:btih:ABCDEF1234567890abcdef1234567890abcdef12"
	body, _ := json.Marshal(grabRequest{
		Title: "Some Movie", TMDBID: 42, Protocol: "torrent",
		DownloadURL: magnet, RootFolderPath: "/movies",
	})
	resp, err := http.Post(srv.URL+"/api/modes/movies/search/grab", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var g grabs.Grab
	if err := json.NewDecoder(resp.Body).Decode(&g); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if g.DownloadClient != "anacrolix" || g.DownloadGID != "gid-abc" || g.Status != grabs.Queued {
		t.Errorf("unexpected grab: %+v", g)
	}
}

// TestGrabHandler_SeasonSpecified_RoundTrips proves seasonSpecified survives
// the POST body -> grabsStore.Create round trip — this flag is what lets
// checkImportHandler tell a deliberate Season 0 (Specials) grab apart from a
// plain series-wide grab with no season picked at all (see search_series_test.go).
func TestGrabHandler_SeasonSpecified_RoundTrips(t *testing.T) {
	dl := newTestDownloader("gid-x", t.TempDir())

	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)

	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, nil, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, testFeedHealth(), rssFeedsStore, nil, nil, dl, nil, nil, nil, nil, nil))
	defer srv.Close()

	body, _ := json.Marshal(grabRequest{
		Title: "Some Show Special", TMDBID: 555, SeasonNumber: 0, EpisodeNumber: 0, SeasonSpecified: true,
		Protocol: "torrent", DownloadURL: "magnet:?xt=urn:btih:ABCDEF1234567890abcdef1234567890abcdef12", RootFolderPath: "/tv",
	})
	resp, err := http.Post(srv.URL+"/api/modes/series/search/grab", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var g grabs.Grab
	if err := json.NewDecoder(resp.Body).Decode(&g); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if !g.SeasonSpecified {
		t.Errorf("expected seasonSpecified to round-trip true, got %+v", g)
	}
}

// TestGrabHandler_Usenet_NotSupported confirms a usenet-protocol grab is
// rejected with a clear 400 — the aria2 backend has no usenet/NNTP capability,
// so usenet releases can't be grabbed (the honest not-supported path, not a
// silent failure). See dispatchToDownloadClient's usenet branch.
func TestGrabHandler_Usenet_NotSupported(t *testing.T) {
	dl := newTestDownloader("gid-x", t.TempDir())

	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)

	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, nil, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, testFeedHealth(), rssFeedsStore, nil, nil, dl, nil, nil, nil, nil, nil))
	defer srv.Close()

	body, _ := json.Marshal(grabRequest{
		Title: "Some Show S01E01", TVDBID: 7, Protocol: "usenet",
		DownloadURL: "http://example/download.nzb", RootFolderPath: "/tv",
	})
	resp, err := http.Post(srv.URL+"/api/modes/series/search/grab", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 (usenet not supported by aria2), got %d", resp.StatusCode)
	}
}

func TestGrabHandler_UnrecognizedProtocol(t *testing.T) {
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	if err := connStore.Upsert(context.Background(), "radarr", "http://radarr.local", "key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, nil, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, testFeedHealth(), rssFeedsStore, nil, nil, nil, nil, nil, nil, nil, nil))
	defer srv.Close()

	body, _ := json.Marshal(grabRequest{Title: "X", Protocol: "carrier-pigeon", DownloadURL: "http://x", RootFolderPath: "/movies"})
	resp, err := http.Post(srv.URL+"/api/modes/movies/search/grab", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for an unrecognized protocol, got %d", resp.StatusCode)
	}
}

func TestListGrabsHandler_ScopedByMode(t *testing.T) {
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	ctx := context.Background()
	if _, err := grabsStore.Create(ctx, grabs.Grab{Mode: mode.Movies, Title: "A Movie", Indexer: "I", Protocol: "torrent", DownloadClient: "aria2", RootFolderPath: "/movies"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := grabsStore.Create(ctx, grabs.Grab{Mode: mode.Series, Title: "A Show", Indexer: "I", Protocol: "usenet", DownloadClient: "aria2", RootFolderPath: "/tv"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, nil, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, testFeedHealth(), rssFeedsStore, nil, nil, nil, nil, nil, nil, nil, nil))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/modes/movies/grabs")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	var list []grabs.Grab
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if len(list) != 1 || list[0].Title != "A Movie" {
		t.Errorf("expected only the movies grab, got %+v", list)
	}
}

// TestCheckImportHandler_TorrentCompleted_PerformsImport exercises the
// full completed-download -> relocate -> record-in-library loop against a
// real on-disk directory (standing in for the torrent engine's actual staging
// directory) — no Radarr involved anymore, the same rigor as Dedup's
// end-to-end test.
//
// Renamed from ..._QBittorrentCompleted_... on 2026-08-10: the old name was the
// only place a reader learned which client the fixture modelled, and it
// modelled one that no longer exists.
func TestCheckImportHandler_TorrentCompleted_PerformsImport(t *testing.T) {
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

	dl := newTestDownloader("abc123", t.TempDir())
	seedComplete(dl, "abc123", downloadDir)

	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	ctx := context.Background()

	g, err := grabsStore.Create(ctx, grabs.Grab{
		Mode: mode.Movies, Title: "Some Movie", TMDBID: 42,
		Indexer: "I", Protocol: "torrent", DownloadClient: "aria2",
		DownloadGID: "abc123", RootFolderPath: moviesRoot,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Capture-at-write reads the SAME per-mode tier setting Search and
	// auto-grab already read (autoGrabTier), so seed it and assert it lands on
	// the imported row below.
	if err := settingsStore.Set(ctx, qualityTierKey(mode.Movies), "lossless"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, nil, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, testFeedHealth(), rssFeedsStore, nil, nil, dl, nil, nil, nil, nil, nil))
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/api/grabs/"+strconv.FormatInt(g.ID, 10)+"/check-import", "application/json", nil)
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
	if updated.Status != grabs.Imported {
		t.Errorf("expected status Imported, got %q", updated.Status)
	}
	// Relocate moves the whole contentPath directory (preserving its
	// basename) into the root folder, the same generic behavior it already
	// has for a directory-shaped Rename source — so the file lands at
	// <root>/<download-dir-name>/movie.mkv, not directly at <root>/movie.mkv.
	wantPath := filepath.Join(moviesRoot, filepath.Base(downloadDir), "movie.mkv")
	if _, err := os.Stat(wantPath); err != nil {
		t.Errorf("expected the file to have been relocated into the root folder: %v", err)
	}
	item, err := libStore.GetByTMDBID(ctx, mode.Movies, 42)
	if err != nil {
		t.Fatalf("expected the movie to be recorded in the library, got err=%v", err)
	}
	if item.Title != "Some Movie" || item.FilePath != wantPath {
		t.Errorf("unexpected library item: %+v", item)
	}
	// Capture-at-write on the grab-import path. Size must be the resolved
	// VIDEO FILE's bytes: Relocate moved a whole directory here, and
	// library.FileSize reports 0 for a directory — a nonzero value is what
	// proves the size was stat'd at videoPath rather than movedPath.
	if want := int64(len("fake video")); item.Size != want {
		t.Errorf("expected Size %d (the resolved video file's real bytes, not the wrapping directory), got %d", want, item.Size)
	}
	if item.QualityTier != "lossless" {
		t.Errorf("expected QualityTier %q (the configured movies_quality_tier), got %q", "lossless", item.QualityTier)
	}
}

// TestCheckImportHandler_MoviesCompleted_NotifiesJellyfin is Slice 5 end to
// end: a completed grab-import's Relocate lands the file, and
// sess.NotifyPlayers fires exactly one Created PathChange for the resolved
// video file — NOT movedPath itself, since Relocate here moves the whole
// downloadDir (a directory), the same "actual file, not the wrapping
// directory" discipline as rename.go's row 1.
func TestCheckImportHandler_MoviesCompleted_NotifiesJellyfin(t *testing.T) {
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

	dl := newTestDownloader("abc123", t.TempDir())
	seedComplete(dl, "abc123", downloadDir)

	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore, scStore := testStoresWithRegistry(t)
	ctx := context.Background()
	jf := newFakeJellyfin(0)
	seedJellyfinPlayer(t, scStore, jf.Server(t).URL, "jf-key", "movies", "series")

	g, err := grabsStore.Create(ctx, grabs.Grab{
		Mode: mode.Movies, Title: "Some Movie", TMDBID: 42,
		Indexer: "I", Protocol: "torrent", DownloadClient: "aria2",
		DownloadGID: "abc123", RootFolderPath: moviesRoot,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, scStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, testFeedHealth(), rssFeedsStore, nil, nil, dl, nil, nil, nil, nil, nil))
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/api/grabs/"+strconv.FormatInt(g.ID, 10)+"/check-import", "application/json", nil)
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	item, err := libStore.GetByTMDBID(ctx, mode.Movies, 42)
	if err != nil {
		t.Fatalf("expected the movie to be recorded in the library, got err=%v", err)
	}

	if jf.CallCount() != 1 {
		t.Fatalf("expected exactly one notify call to Jellyfin, got %d", jf.CallCount())
	}
	batch := jf.Batches()[0]
	if len(batch) != 1 || batch[0].Path != item.FilePath || batch[0].UpdateType != "Created" {
		t.Errorf("expected a single Created PathChange for the resolved video file %q, got %+v", item.FilePath, batch)
	}
}

// TestCheckImportHandler_RelocateFails_NoNotify proves the plan's explicit
// "if Relocate errors, emit nothing" contract: a failed Relocate (source
// content path vanished) must produce zero notify calls.
func TestCheckImportHandler_RelocateFails_NoNotify(t *testing.T) {
	dir := t.TempDir()
	missingDownloadDir := filepath.Join(dir, "downloads", "gone")
	moviesRoot := filepath.Join(dir, "Movies")
	if err := os.MkdirAll(moviesRoot, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// missingDownloadDir is deliberately never created, so rename.Relocate's
	// os.Rename fails (source doesn't exist).

	dl := newTestDownloader("abc123", t.TempDir())
	seedComplete(dl, "abc123", missingDownloadDir)

	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore, scStore := testStoresWithRegistry(t)
	ctx := context.Background()
	jf := newFakeJellyfin(0)
	seedJellyfinPlayer(t, scStore, jf.Server(t).URL, "jf-key", "movies", "series")

	g, err := grabsStore.Create(ctx, grabs.Grab{
		Mode: mode.Movies, Title: "Some Movie", TMDBID: 42,
		Indexer: "I", Protocol: "torrent", DownloadClient: "aria2",
		DownloadGID: "abc123", RootFolderPath: moviesRoot,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, scStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, testFeedHealth(), rssFeedsStore, nil, nil, dl, nil, nil, nil, nil, nil))
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/api/grabs/"+strconv.FormatInt(g.ID, 10)+"/check-import", "application/json", nil)
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		t.Fatalf("expected a non-200 response surfacing the Relocate failure, got %d", resp.StatusCode)
	}

	if jf.CallCount() != 0 {
		t.Errorf("expected zero notify calls when Relocate fails, got %d: %+v", jf.CallCount(), jf.Batches())
	}
}

// TestCheckImportHandler_JellyfinBestEffort_ImportStillSucceeds is
// Guardrail #1's Slice 5 counterpart: a downstream Jellyfin 500 must never
// fail the grab-import — the file already moved and the library record
// already committed by the time notify runs.
func TestCheckImportHandler_JellyfinBestEffort_ImportStillSucceeds(t *testing.T) {
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

	dl := newTestDownloader("abc123", t.TempDir())
	seedComplete(dl, "abc123", downloadDir)

	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore, scStore := testStoresWithRegistry(t)
	ctx := context.Background()
	jf := newFakeJellyfin(http.StatusInternalServerError)
	seedJellyfinPlayer(t, scStore, jf.Server(t).URL, "jf-key", "movies", "series")

	g, err := grabsStore.Create(ctx, grabs.Grab{
		Mode: mode.Movies, Title: "Some Movie", TMDBID: 42,
		Indexer: "I", Protocol: "torrent", DownloadClient: "aria2",
		DownloadGID: "abc123", RootFolderPath: moviesRoot,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, scStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, testFeedHealth(), rssFeedsStore, nil, nil, dl, nil, nil, nil, nil, nil))
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/api/grabs/"+strconv.FormatInt(g.ID, 10)+"/check-import", "application/json", nil)
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 despite the Jellyfin 500, got %d", resp.StatusCode)
	}
	var updated grabs.Grab
	if err := json.NewDecoder(resp.Body).Decode(&updated); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if updated.Status != grabs.Imported {
		t.Errorf("expected status Imported despite the Jellyfin 500, got %q", updated.Status)
	}
	if jf.CallCount() != 1 {
		t.Errorf("expected the notify attempt to still have been made (and failed), got %d calls", jf.CallCount())
	}
}

// TestCheckImportHandler_AdultCompleted_NotifiesStash proves grab-import
// reaches Adult too (via the mode.Adult branch), and that it notifies Stash
// (not Jellyfin — hardcoded scoping via mode.Build) with movedPath directly.
// Since Whisparr was eliminated (Stage 4) an Adult grab has no scene identity
// at import time, so nothing is UpsertScene'd here — the file is relocated and
// left for the next Rename scan to identify (see the handler's mode.Adult
// branch); Stash's RescanPaths scans the directory tree fine.
func TestCheckImportHandler_AdultCompleted_NotifiesStash(t *testing.T) {
	dir := t.TempDir()
	downloadDir := filepath.Join(dir, "downloads", "Some.Scene.mp4")
	adultRoot := filepath.Join(dir, "Adult")
	if err := os.MkdirAll(filepath.Dir(downloadDir), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.MkdirAll(adultRoot, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(downloadDir, []byte("fake video"), 0o644); err != nil {
		t.Fatalf("writing file: %v", err)
	}

	dl := newTestDownloader("abc123", t.TempDir())
	seedComplete(dl, "abc123", downloadDir)

	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	ctx := context.Background()
	stash := newFakeStash(0)
	if err := connStore.Upsert(ctx, "stash", stash.Server(t).URL, "stash-key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	g, err := grabsStore.Create(ctx, grabs.Grab{
		Mode: mode.Adult, Title: "Some Scene",
		Indexer: "I", Protocol: "torrent", DownloadClient: "aria2",
		DownloadGID: "abc123", RootFolderPath: adultRoot,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, nil, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, testFeedHealth(), rssFeedsStore, nil, nil, dl, nil, nil, nil, nil, nil))
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/api/grabs/"+strconv.FormatInt(g.ID, 10)+"/check-import", "application/json", nil)
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
	if updated.Status != grabs.Imported {
		t.Errorf("expected status Imported, got %q", updated.Status)
	}

	wantPath := filepath.Join(adultRoot, "Some.Scene.mp4")
	scanCalls := stash.ScanCalls()
	if len(scanCalls) != 1 {
		t.Fatalf("expected exactly 1 metadataScan call, got %d: %+v", len(scanCalls), scanCalls)
	}
	scanPaths, _ := scanCalls[0]["paths"].([]any)
	if len(scanPaths) != 1 || scanPaths[0] != wantPath {
		t.Errorf("expected scan of [%q], got %+v", wantPath, scanCalls[0]["paths"])
	}
	if scanCalls[0]["scanGeneratePhashes"] != false {
		t.Errorf("expected phash-free scan (proving RescanPaths not ScanPaths was used), got %v", scanCalls[0]["scanGeneratePhashes"])
	}
	if len(stash.CleanCalls()) != 0 {
		t.Errorf("expected zero metadataClean calls for a Created-only grab-import, got %+v", stash.CleanCalls())
	}
}

func TestCheckImportHandler_StillDownloading_JustUpdatesStatus(t *testing.T) {
	dl := newTestDownloader("abc123", t.TempDir())
	seedActive(dl, "abc123")

	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	ctx := context.Background()
	g, err := grabsStore.Create(ctx, grabs.Grab{
		Mode: mode.Movies, Title: "Some Movie", Indexer: "I", Protocol: "torrent",
		DownloadClient: "aria2", DownloadGID: "abc123", RootFolderPath: "/movies",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, nil, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, testFeedHealth(), rssFeedsStore, nil, nil, dl, nil, nil, nil, nil, nil))
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/api/grabs/"+strconv.FormatInt(g.ID, 10)+"/check-import", "application/json", nil)
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	defer resp.Body.Close()
	var updated grabs.Grab
	json.NewDecoder(resp.Body).Decode(&updated)
	if updated.Status != grabs.Downloading {
		t.Errorf("expected status Downloading, got %q", updated.Status)
	}
}
