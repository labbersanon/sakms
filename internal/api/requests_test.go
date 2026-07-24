package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labbersanon/sakms/internal/apidto"
	"github.com/labbersanon/sakms/internal/grabs"
	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mode"
)

// TestRequestsHandler_AggregatesAndDedups exercises all four behaviors at once:
// In-Library rows (Movies/Series/Adult), Series MissingCount, a Downloading row
// for a grab with no tracked match, and the dedup where a tracked title that is
// also actively grabbing collapses to one row with the grab status winning.
func TestRequestsHandler_AggregatesAndDedups(t *testing.T) {
	_, _, _, _, grabsStore, libStore, _, _, _, _, _ := testStores(t)
	ctx := context.Background()

	// Movie A — tracked AND actively grabbing (dedup → Downloading).
	if _, err := libStore.Upsert(ctx, library.Item{Mode: mode.Movies, TMDBID: 100, Title: "Movie A", FilePath: "/m/a.mkv", RootFolderPath: "/m"}); err != nil {
		t.Fatalf("upsert movie: %v", err)
	}
	// Movie E — tracked only (stays In Library).
	if _, err := libStore.Upsert(ctx, library.Item{Mode: mode.Movies, TMDBID: 500, Title: "Movie E", FilePath: "/m/e.mkv", RootFolderPath: "/m"}); err != nil {
		t.Fatalf("upsert movie E: %v", err)
	}
	// Series B — one present episode, one missing (MissingCount == 1).
	series, err := libStore.UpsertSeries(ctx, library.Series{TMDBID: 200, Title: "Show B", RootFolderPath: "/tv"})
	if err != nil {
		t.Fatalf("upsert series: %v", err)
	}
	if _, err := libStore.UpsertEpisode(ctx, library.Episode{SeriesID: series.ID, SeasonNumber: 1, EpisodeNumber: 1, FilePath: "/tv/b/s01e01.mkv"}); err != nil {
		t.Fatalf("upsert ep1: %v", err)
	}
	if _, err := libStore.UpsertEpisode(ctx, library.Episode{SeriesID: series.ID, SeasonNumber: 1, EpisodeNumber: 2, FilePath: ""}); err != nil {
		t.Fatalf("upsert ep2: %v", err)
	}
	// Scene C — tracked adult scene (In Library, no TMDB id).
	if _, err := libStore.UpsertScene(ctx, library.Scene{Box: "stashdb", SceneID: "s1", Title: "Scene C", FilePath: "/a/c.mkv", RootFolderPath: "/a"}); err != nil {
		t.Fatalf("upsert scene: %v", err)
	}

	// Grab matching Movie A (dedup) + a standalone grab for an untracked title.
	grabA, err := grabsStore.Create(ctx, grabs.Grab{Mode: mode.Movies, Title: "Movie A", TMDBID: 100})
	if err != nil {
		t.Fatalf("create grab A: %v", err)
	}
	if _, err := grabsStore.Create(ctx, grabs.Grab{Mode: mode.Movies, Title: "Movie D", TMDBID: 400}); err != nil {
		t.Fatalf("create grab D: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/requests", requestsHandler(grabsStore, libStore))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/requests")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var out apidto.RequestStatusResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}

	byTitle := map[string]apidto.RequestStatusItem{}
	for _, it := range out.Items {
		byTitle[it.Title] = it
	}
	// 5 distinct rows: A (deduped), E, B, C, D.
	if len(out.Items) != 5 {
		t.Fatalf("expected 5 rows, got %d: %+v", len(out.Items), out.Items)
	}

	if a := byTitle["Movie A"]; a.Status != "Downloading" || a.GrabID != grabA.ID {
		t.Errorf("Movie A should dedup to Downloading with the grab id, got %+v", a)
	}
	if e := byTitle["Movie E"]; e.Status != "In Library" || e.GrabID != 0 {
		t.Errorf("Movie E should stay In Library, got %+v", e)
	}
	if b := byTitle["Show B"]; b.Status != "In Library" || b.MissingCount != 1 {
		t.Errorf("Show B should be In Library with MissingCount=1, got %+v", b)
	}
	if c := byTitle["Scene C"]; c.Mode != "adult" || c.Status != "In Library" {
		t.Errorf("Scene C should be an In Library adult row, got %+v", c)
	}
	if d := byTitle["Movie D"]; d.Status != "Downloading" || d.GrabID == 0 {
		t.Errorf("Movie D should be a standalone Downloading row, got %+v", d)
	}
}

// TestRequestsHandler_ImportedGrabNotDownloading confirms an already-imported
// grab does not surface as Downloading (it's represented by its In-Library row
// instead) — only in-flight statuses count.
func TestRequestsHandler_ImportedGrabNotDownloading(t *testing.T) {
	_, _, _, _, grabsStore, libStore, _, _, _, _, _ := testStores(t)
	ctx := context.Background()

	g, err := grabsStore.Create(ctx, grabs.Grab{Mode: mode.Movies, Title: "Done Movie", TMDBID: 700})
	if err != nil {
		t.Fatalf("create grab: %v", err)
	}
	if err := grabsStore.UpdateStatus(ctx, g.ID, grabs.Imported); err != nil {
		t.Fatalf("update status: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/requests", requestsHandler(grabsStore, libStore))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/requests")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	var out apidto.RequestStatusResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Items) != 0 {
		t.Errorf("an imported grab with no tracked item should surface nothing, got %+v", out.Items)
	}
}
