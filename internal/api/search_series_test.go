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

	"github.com/labbersanon/sakms/internal/grabs"
	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mode"
)

// TestCheckImportHandler_Series_SingleEpisode_PerformsImport mirrors
// TestCheckImportHandler_QBittorrentCompleted_PerformsImport but for a
// single-episode Series grab — no Sonarr involved anywhere.
func TestCheckImportHandler_Series_SingleEpisode_PerformsImport(t *testing.T) {
	dir := t.TempDir()
	downloadDir := filepath.Join(dir, "downloads", "Some.Show.S01E01.1080p.WEB-DL.x264-GROUP")
	tvRoot := filepath.Join(dir, "TV")
	if err := os.MkdirAll(downloadDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.MkdirAll(tvRoot, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(downloadDir, "episode.mkv"), []byte("fake video"), 0o644); err != nil {
		t.Fatalf("writing file: %v", err)
	}

	dl := newTestDownloader("abc123", t.TempDir())
	seedComplete(dl, "abc123", downloadDir)

	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	ctx := context.Background()

	g, err := grabsStore.Create(ctx, grabs.Grab{
		Mode: mode.Series, Title: "Some Show", TMDBID: 555, SeasonNumber: 1, EpisodeNumber: 1, SeasonSpecified: true,
		Indexer: "I", Protocol: "torrent", DownloadClient: "aria2",
		DownloadGID: "abc123", RootFolderPath: tvRoot,
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
	var updated grabs.Grab
	if err := json.NewDecoder(resp.Body).Decode(&updated); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if updated.Status != grabs.Imported {
		t.Errorf("expected status Imported, got %q", updated.Status)
	}

	series, err := libStore.GetSeriesByTMDBID(ctx, 555)
	if err != nil {
		t.Fatalf("expected the series to be recorded, got err=%v", err)
	}
	episodes, err := libStore.ListEpisodes(ctx, series.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(episodes) != 1 || episodes[0].SeasonNumber != 1 || episodes[0].EpisodeNumber != 1 || episodes[0].FilePath == "" {
		t.Fatalf("unexpected episodes: %+v", episodes)
	}
}

// TestCheckImportHandler_Series_LogicalSplit_RecordsBothEpisodes proves the
// confirmed pre-fix bug is actually fixed: a directly-grabbed logical-
// episode-split file ("S01E01-E02") used to record only episode 1 (the
// first match ParseEpisodeFilename's FindStringSubmatch ever returned) —
// episode 2 was silently dropped forever. This asserts BOTH episode rows
// exist, pointing at the SAME relocated file.
func TestCheckImportHandler_Series_LogicalSplit_RecordsBothEpisodes(t *testing.T) {
	dir := t.TempDir()
	downloadDir := filepath.Join(dir, "downloads", "Some.Show.S01E01-E02.1080p.WEB-DL.x264-GROUP")
	tvRoot := filepath.Join(dir, "TV")
	if err := os.MkdirAll(downloadDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.MkdirAll(tvRoot, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(downloadDir, "Some.Show.S01E01-E02.mkv"), []byte("fake video"), 0o644); err != nil {
		t.Fatalf("writing file: %v", err)
	}

	dl := newTestDownloader("abc123", t.TempDir())
	seedComplete(dl, "abc123", downloadDir)

	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	ctx := context.Background()

	g, err := grabsStore.Create(ctx, grabs.Grab{
		Mode: mode.Series, Title: "Some Show", TMDBID: 555, SeasonNumber: 1, EpisodeNumber: 1, SeasonSpecified: true,
		Indexer: "I", Protocol: "torrent", DownloadClient: "aria2",
		DownloadGID: "abc123", RootFolderPath: tvRoot,
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
	var updated grabs.Grab
	if err := json.NewDecoder(resp.Body).Decode(&updated); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if updated.Status != grabs.Imported {
		t.Errorf("expected status Imported, got %q", updated.Status)
	}

	series, err := libStore.GetSeriesByTMDBID(ctx, 555)
	if err != nil {
		t.Fatalf("expected the series to be recorded, got err=%v", err)
	}
	episodes, err := libStore.ListEpisodes(ctx, series.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(episodes) != 2 {
		t.Fatalf("expected both bundled episodes to be recorded, got %d: %+v", len(episodes), episodes)
	}
	if episodes[0].SeasonNumber != 1 || episodes[0].EpisodeNumber != 1 || episodes[0].FilePath == "" {
		t.Errorf("unexpected episode 1: %+v", episodes[0])
	}
	if episodes[1].SeasonNumber != 1 || episodes[1].EpisodeNumber != 2 || episodes[1].FilePath == "" {
		t.Errorf("unexpected episode 2: %+v", episodes[1])
	}
	if episodes[0].FilePath != episodes[1].FilePath {
		t.Errorf("expected both episodes to point at the SAME relocated file, got %q vs %q", episodes[0].FilePath, episodes[1].FilePath)
	}
}

// TestCheckImportHandler_Series_SeasonPack_PerformsImport proves a
// season-pack grab (a directory containing multiple episode files) records
// one episode row per file, not just one for the whole pack.
func TestCheckImportHandler_Series_SeasonPack_PerformsImport(t *testing.T) {
	dir := t.TempDir()
	downloadDir := filepath.Join(dir, "downloads", "Some.Show.S01.1080p.WEB-DL.x264-GROUP")
	tvRoot := filepath.Join(dir, "TV")
	if err := os.MkdirAll(downloadDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.MkdirAll(tvRoot, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	for _, name := range []string{"Some.Show.S01E01.mkv", "Some.Show.S01E02.mkv"} {
		if err := os.WriteFile(filepath.Join(downloadDir, name), []byte("fake video"), 0o644); err != nil {
			t.Fatalf("writing %s: %v", name, err)
		}
	}

	dl := newTestDownloader("def456", t.TempDir())
	seedComplete(dl, "def456", downloadDir)

	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	ctx := context.Background()

	// Season-pack grab: SeasonNumber set, EpisodeNumber left 0.
	g, err := grabsStore.Create(ctx, grabs.Grab{
		Mode: mode.Series, Title: "Some Show", TMDBID: 555, SeasonNumber: 1,
		Indexer: "I", Protocol: "torrent", DownloadClient: "aria2",
		DownloadGID: "def456", RootFolderPath: tvRoot,
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
	var updated grabs.Grab
	if err := json.NewDecoder(resp.Body).Decode(&updated); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if updated.Status != grabs.Imported {
		t.Errorf("expected status Imported, got %q", updated.Status)
	}

	series, err := libStore.GetSeriesByTMDBID(ctx, 555)
	if err != nil {
		t.Fatalf("expected the series to be recorded, got err=%v", err)
	}
	episodes, err := libStore.ListEpisodes(ctx, series.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(episodes) != 2 {
		t.Fatalf("expected one episode row per file in the season pack, got %+v", episodes)
	}
	byEpisode := map[int]library.Episode{}
	for _, ep := range episodes {
		byEpisode[ep.EpisodeNumber] = ep
	}
	if byEpisode[1].FilePath == "" || byEpisode[2].FilePath == "" {
		t.Fatalf("expected both episode files resolved, got %+v", episodes)
	}
}

// TestCheckImportHandler_Series_SeasonSpecifiedZero_RecordsSpecialsEpisode
// proves a deliberate Season 0 (Specials) grab whose single resolved file's
// name doesn't parse via ParseEpisodeFilename still gets recorded — the
// SeasonSpecified flag distinguishes it from "no season picked at all"
// (see the next test), which SeasonNumber==0 alone can never do.
func TestCheckImportHandler_Series_SeasonSpecifiedZero_RecordsSpecialsEpisode(t *testing.T) {
	dir := t.TempDir()
	downloadDir := filepath.Join(dir, "downloads", "Some.Show.Special.1080p.WEB-DL.x264-GROUP")
	tvRoot := filepath.Join(dir, "TV")
	if err := os.MkdirAll(downloadDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.MkdirAll(tvRoot, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(downloadDir, "special.mkv"), []byte("fake video"), 0o644); err != nil {
		t.Fatalf("writing file: %v", err)
	}

	dl := newTestDownloader("special1", t.TempDir())
	seedComplete(dl, "special1", downloadDir)

	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	ctx := context.Background()

	// SeasonNumber/EpisodeNumber left at 0/0 — genuine Season 0 (Specials),
	// episode unspecified within it — but SeasonSpecified is true, since the
	// user deliberately picked season 0.
	g, err := grabsStore.Create(ctx, grabs.Grab{
		Mode: mode.Series, Title: "Some Show", TMDBID: 555, SeasonSpecified: true,
		Indexer: "I", Protocol: "torrent", DownloadClient: "aria2",
		DownloadGID: "special1", RootFolderPath: tvRoot,
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

	series, err := libStore.GetSeriesByTMDBID(ctx, 555)
	if err != nil {
		t.Fatalf("expected the series to be recorded, got err=%v", err)
	}
	episodes, err := libStore.ListEpisodes(ctx, series.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(episodes) != 1 || episodes[0].SeasonNumber != 0 || episodes[0].EpisodeNumber != 0 || episodes[0].FilePath == "" {
		t.Fatalf("expected a season-0 episode recorded, got %+v", episodes)
	}
}

// TestCheckImportHandler_Series_SeasonNotSpecified_UnparseableFilename_SkipsRatherThanMisfiling
// is the regression guard for the bug the naive "just delete the ==0 check"
// fix would have introduced: a plain series-wide grab (no season ever
// picked) whose single resolved file's name doesn't parse must NOT be
// misfiled as a Season 0/Specials episode — it should simply not be
// recorded, leaving the file on disk for a human to sort out via Rename.
func TestCheckImportHandler_Series_SeasonNotSpecified_UnparseableFilename_SkipsRatherThanMisfiling(t *testing.T) {
	dir := t.TempDir()
	downloadDir := filepath.Join(dir, "downloads", "Some.Show.Complete.1080p.WEB-DL.x264-GROUP")
	tvRoot := filepath.Join(dir, "TV")
	if err := os.MkdirAll(downloadDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.MkdirAll(tvRoot, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(downloadDir, "video.mkv"), []byte("fake video"), 0o644); err != nil {
		t.Fatalf("writing file: %v", err)
	}

	dl := newTestDownloader("noseasons", t.TempDir())
	seedComplete(dl, "noseasons", downloadDir)

	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	ctx := context.Background()

	// A plain series-wide grab: no season ever picked, SeasonSpecified false.
	g, err := grabsStore.Create(ctx, grabs.Grab{
		Mode: mode.Series, Title: "Some Show", TMDBID: 555,
		Indexer: "I", Protocol: "torrent", DownloadClient: "aria2",
		DownloadGID: "noseasons", RootFolderPath: tvRoot,
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

	series, err := libStore.GetSeriesByTMDBID(ctx, 555)
	if err != nil {
		t.Fatalf("expected the series to be recorded, got err=%v", err)
	}
	episodes, err := libStore.ListEpisodes(ctx, series.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(episodes) != 0 {
		t.Fatalf("expected no episode recorded for an unparseable file with no season specified, got %+v", episodes)
	}
}
