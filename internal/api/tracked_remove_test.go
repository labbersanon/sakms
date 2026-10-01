package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"

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
