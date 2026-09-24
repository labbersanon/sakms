package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mode"
)

func putTrackedIdentity(t *testing.T, baseURL, mode string, id int64, body any) *http.Response {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPut, fmt.Sprintf("%s/api/modes/%s/tracked/%d/identity", baseURL, mode, id), bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestPutTrackedIdentity_SeriesUpdatesSameRow(t *testing.T) {
	libStore, srv := newTrackedTestServer(t)
	ctx := context.Background()
	series, err := libStore.UpsertSeries(ctx, library.Series{TMDBID: 1, Title: "Wrong Show", Year: 2000, RootFolderPath: "/tv"})
	if err != nil {
		t.Fatal(err)
	}
	resp := putTrackedIdentity(t, srv.URL, "series", series.ID, map[string]any{
		"tmdbId": 1396, "title": "Breaking Bad", "year": 2008,
	})
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status %d", resp.StatusCode)
	}
	got, err := libStore.GetSeries(ctx, series.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.TMDBID != 1396 || got.Title != "Breaking Bad" || got.Year != 2008 {
		t.Fatalf("got %+v", got)
	}
}

func TestPutTrackedIdentity_SeriesConflict(t *testing.T) {
	libStore, srv := newTrackedTestServer(t)
	ctx := context.Background()
	if _, err := libStore.UpsertSeries(ctx, library.Series{TMDBID: 1, Title: "A", RootFolderPath: "/tv"}); err != nil {
		t.Fatal(err)
	}
	b, err := libStore.UpsertSeries(ctx, library.Series{TMDBID: 2, Title: "B", RootFolderPath: "/tv"})
	if err != nil {
		t.Fatal(err)
	}
	resp := putTrackedIdentity(t, srv.URL, "series", b.ID, map[string]any{
		"tmdbId": 1, "title": "A",
	})
	resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestPutTrackedIdentity_MoviesUpdatesSameRow(t *testing.T) {
	libStore, srv := newTrackedTestServer(t)
	item := seedMovie(t, libStore, library.Item{
		Mode: mode.Movies, TMDBID: 10, Title: "Wrong", Year: 1999, RootFolderPath: "/movies",
	})
	resp := putTrackedIdentity(t, srv.URL, "movies", item.ID, map[string]any{
		"tmdbId": 27205, "title": "Inception", "year": 2010,
	})
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status %d", resp.StatusCode)
	}
	got, err := libStore.Get(context.Background(), item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.TMDBID != 27205 || got.Title != "Inception" || got.Year != 2010 {
		t.Fatalf("got %+v", got)
	}
}

func TestPutTrackedIdentity_AdultUpdatesSameRow(t *testing.T) {
	libStore, srv := newTrackedTestServer(t)
	scene := seedScene(t, libStore, library.Scene{
		Box: "local", SceneID: "old", Title: "Wrong", Studio: "A",
		RootFolderPath: "/adult",
	})
	resp := putTrackedIdentity(t, srv.URL, "adult", scene.ID, map[string]any{
		"title": "Right", "box": "stashdb", "sceneId": "catalog-uuid",
		"studio": "Studio", "date": "2023-06-15",
	})
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status %d", resp.StatusCode)
	}
	got, err := libStore.GetSceneByID(context.Background(), scene.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Box != "stashdb" || got.SceneID != "catalog-uuid" || got.Title != "Right" {
		t.Fatalf("got %+v", got)
	}
}
