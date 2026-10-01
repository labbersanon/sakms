package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/labbersanon/sakms/internal/grabs"
	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mode"
)

func deleteTracked(t *testing.T, baseURL, mode string, id int64, body any) *http.Response {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(http.MethodDelete, fmt.Sprintf("%s/api/modes/%s/tracked/%d", baseURL, mode, id), rdr)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func decodeGone(t *testing.T, resp *http.Response) bool {
	t.Helper()
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status %d: %s", resp.StatusCode, b)
	}
	var out struct {
		Gone bool `json:"gone"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out.Gone
}

func TestDeleteTracked_MoviesRemovesRow(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "Movie", "movie.mkv")
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	libStore, srv := newTrackedTestServer(t)
	item := seedMovie(t, libStore, library.Item{
		Mode: mode.Movies, TMDBID: 10, Title: "Movie", FilePath: file, RootFolderPath: root,
	})
	resp := deleteTracked(t, srv.URL, "movies", item.ID, nil)
	if !decodeGone(t, resp) {
		t.Fatal("expected gone")
	}
	if _, err := libStore.Get(context.Background(), item.ID); err == nil {
		t.Fatal("item still present")
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Errorf("file should be gone: %v", err)
	}
}

func TestDeleteTracked_SeriesEntire(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "Show", "Season 01", "e1.mkv")
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	libStore, srv := newTrackedTestServer(t)
	ctx := context.Background()
	series, err := libStore.UpsertSeries(ctx, library.Series{TMDBID: 1, Title: "Show", RootFolderPath: root})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := libStore.UpsertEpisode(ctx, library.Episode{
		SeriesID: series.ID, SeasonNumber: 1, EpisodeNumber: 1, FilePath: file,
	}); err != nil {
		t.Fatal(err)
	}
	resp := deleteTracked(t, srv.URL, "series", series.ID, map[string]any{"entireSeries": true})
	if !decodeGone(t, resp) {
		t.Fatal("expected gone")
	}
	if _, err := libStore.GetSeries(ctx, series.ID); err == nil {
		t.Fatal("series still present")
	}
}

func TestDeleteTracked_SeriesSeasonsKeepsShow(t *testing.T) {
	root := t.TempDir()
	s1 := filepath.Join(root, "Show", "Season 01", "e1.mkv")
	s2 := filepath.Join(root, "Show", "Season 02", "e1.mkv")
	for _, p := range []string{s1, s2} {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	libStore, srv := newTrackedTestServer(t)
	ctx := context.Background()
	series, err := libStore.UpsertSeries(ctx, library.Series{TMDBID: 1, Title: "Show", RootFolderPath: root})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := libStore.UpsertEpisode(ctx, library.Episode{
		SeriesID: series.ID, SeasonNumber: 1, EpisodeNumber: 1, FilePath: s1,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := libStore.UpsertEpisode(ctx, library.Episode{
		SeriesID: series.ID, SeasonNumber: 2, EpisodeNumber: 1, FilePath: s2,
	}); err != nil {
		t.Fatal(err)
	}
	resp := deleteTracked(t, srv.URL, "series", series.ID, map[string]any{"seasons": []int{1}})
	if decodeGone(t, resp) {
		t.Fatal("series should remain")
	}
	if _, err := libStore.GetSeries(ctx, series.ID); err != nil {
		t.Fatalf("series should remain: %v", err)
	}
	if _, err := os.Stat(s2); err != nil {
		t.Errorf("season 2 must survive: %v", err)
	}
}

func TestDeleteTracked_SeriesEmptyBodyIs400(t *testing.T) {
	libStore, srv := newTrackedTestServer(t)
	ctx := context.Background()
	series, err := libStore.UpsertSeries(ctx, library.Series{TMDBID: 1, Title: "Show", RootFolderPath: "/tv"})
	if err != nil {
		t.Fatal(err)
	}
	resp := deleteTracked(t, srv.URL, "series", series.ID, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestDeleteTracked_NotFound(t *testing.T) {
	_, srv := newTrackedTestServer(t)
	resp := deleteTracked(t, srv.URL, "movies", 99, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestDeleteTracked_AdultRemovesScene(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "Studio", "scene.mp4")
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	libStore, srv := newTrackedTestServer(t)
	scene := seedScene(t, libStore, library.Scene{
		Box: "tpdb", SceneID: "abc", Title: "A Scene", FilePath: file, RootFolderPath: root,
	})
	resp := deleteTracked(t, srv.URL, "adult", scene.ID, nil)
	if !decodeGone(t, resp) {
		t.Fatal("expected gone")
	}
	if _, err := libStore.GetSceneByID(context.Background(), scene.ID); err == nil {
		t.Fatal("scene still present")
	}
}

func newTrackedEnv(t *testing.T) (*library.Store, *grabs.Store, *httptest.Server) {
	t.Helper()
	connStore, propStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	mux := NewMux(testHTTPClient(), connStore, nil, propStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, testFeedHealth(), rssFeedsStore, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return libStore, grabsStore, srv
}

func parkAirDateRetry(t *testing.T, grabsStore *grabs.Store, tmdbID, season, episode int) grabs.Grab {
	t.Helper()
	g, err := grabsStore.Create(context.Background(), grabs.Grab{
		Mode: mode.Series, Title: "Show", TMDBID: tmdbID,
		SeasonNumber: season, EpisodeNumber: episode, SeasonSpecified: true,
		RootFolderPath: "/tv", Status: grabs.PendingRetry,
		RetryAfter: grabs.FormatTime(time.Now().Add(time.Hour)),
	})
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func TestDeleteTracked_SeriesSeasonUnmonitorsThatSeason(t *testing.T) {
	root := t.TempDir()
	s1 := filepath.Join(root, "Show", "Season 01", "e1.mkv")
	s2 := filepath.Join(root, "Show", "Season 02", "e1.mkv")
	for _, p := range []string{s1, s2} {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	libStore, grabsStore, srv := newTrackedEnv(t)
	ctx := context.Background()
	series, err := libStore.UpsertSeries(ctx, library.Series{TMDBID: 42, Title: "Show", RootFolderPath: root})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := libStore.UpsertEpisode(ctx, library.Episode{
		SeriesID: series.ID, SeasonNumber: 1, EpisodeNumber: 1, FilePath: s1,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := libStore.UpsertEpisode(ctx, library.Episode{
		SeriesID: series.ID, SeasonNumber: 2, EpisodeNumber: 1, FilePath: s2,
	}); err != nil {
		t.Fatal(err)
	}
	parked := parkAirDateRetry(t, grabsStore, 42, 1, 1)
	kept := parkAirDateRetry(t, grabsStore, 42, 2, 1)

	resp := deleteTracked(t, srv.URL, "series", series.ID, map[string]any{"seasons": []int{1}})
	if decodeGone(t, resp) {
		t.Fatal("series should remain")
	}
	got, err := grabsStore.Get(ctx, parked.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != grabs.Failed {
		t.Fatalf("season 1 retry status %q, want failed", got.Status)
	}
	if got.RetryReason != airDateUnmonitoredReason {
		t.Fatalf("retry_reason = %q, want un-monitored", got.RetryReason)
	}
	survivor, err := grabsStore.Get(ctx, kept.ID)
	if err != nil {
		t.Fatal(err)
	}
	if survivor.Status != grabs.PendingRetry {
		t.Fatalf("season 2 retry status %q, want pending_retry", survivor.Status)
	}
}

func TestDeleteTracked_SeriesEntireUnmonitorsAllRetries(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "Show", "Season 01", "e1.mkv")
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	libStore, grabsStore, srv := newTrackedEnv(t)
	ctx := context.Background()
	series, err := libStore.UpsertSeries(ctx, library.Series{TMDBID: 99, Title: "Show", RootFolderPath: root})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := libStore.UpsertEpisode(ctx, library.Episode{
		SeriesID: series.ID, SeasonNumber: 1, EpisodeNumber: 1, FilePath: file,
	}); err != nil {
		t.Fatal(err)
	}
	parked := parkAirDateRetry(t, grabsStore, 99, 3, 1)
	watch, err := grabsStore.Create(ctx, grabs.Grab{
		Mode: mode.Series, Title: "Show", TMDBID: 99, RootFolderPath: "/tv",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := grabsStore.SetOrigin(ctx, watch.ID, grabOriginUpgradeWatch); err != nil {
		t.Fatal(err)
	}
	if err := grabsStore.UpdateStatus(ctx, watch.ID, grabs.PendingRetry); err != nil {
		t.Fatal(err)
	}

	resp := deleteTracked(t, srv.URL, "series", series.ID, map[string]any{"entireSeries": true})
	if !decodeGone(t, resp) {
		t.Fatal("expected gone")
	}
	got, err := grabsStore.Get(ctx, parked.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != grabs.Failed {
		t.Fatalf("air-date retry status %q, want failed", got.Status)
	}
	watched, err := grabsStore.Get(ctx, watch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if watched.Status != grabs.Failed {
		t.Fatalf("upgrade-watch retry status %q, want failed", watched.Status)
	}
}

func TestDeleteTracked_MoviesUnmonitorsUpgradeWatch(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "Movie", "movie.mkv")
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	libStore, grabsStore, srv := newTrackedEnv(t)
	item := seedMovie(t, libStore, library.Item{
		Mode: mode.Movies, TMDBID: 10, Title: "Movie", FilePath: file, RootFolderPath: root,
	})
	watch, err := grabsStore.Create(context.Background(), grabs.Grab{
		Mode: mode.Movies, Title: "Movie", TMDBID: 10, RootFolderPath: root,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := grabsStore.SetOrigin(context.Background(), watch.ID, grabOriginUpgradeWatch); err != nil {
		t.Fatal(err)
	}
	if err := grabsStore.UpdateStatus(context.Background(), watch.ID, grabs.PendingRetry); err != nil {
		t.Fatal(err)
	}

	resp := deleteTracked(t, srv.URL, "movies", item.ID, nil)
	if !decodeGone(t, resp) {
		t.Fatal("expected gone")
	}
	got, err := grabsStore.Get(context.Background(), watch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != grabs.Failed {
		t.Fatalf("upgrade-watch retry status %q, want failed", got.Status)
	}
}
