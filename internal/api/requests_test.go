package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/labbersanon/sakms/internal/apidto"
	"github.com/labbersanon/sakms/internal/dbtest"
	"github.com/labbersanon/sakms/internal/excludes"
	"github.com/labbersanon/sakms/internal/grabs"
	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/secrets"
)

// requestsTestStores builds grabs/library/excludes stores on one fresh db for
// the Requests-worklist tests — self-contained rather than the shared
// testStores tuple, since these tests need an *excludes.Store the tuple doesn't
// carry (and adding it there would churn all 51 testStores call sites).
func requestsTestStores(t *testing.T) (*grabs.Store, *library.Store, *excludes.Store) {
	t.Helper()
	sqlDB := dbtest.New(t)
	// A real secret store, not nil: these tests create grabs, and grabs.Store
	// encrypts a grab's download URL at rest.
	secretStore, err := secrets.New(make([]byte, 32))
	if err != nil {
		t.Fatalf("building secret store: %v", err)
	}
	return grabs.New(sqlDB, secretStore), library.New(sqlDB), excludes.New(sqlDB)
}

// TestRequestsHandler_AggregatesAndDedups exercises all four behaviors at once:
// In-Library rows (Movies/Series/Adult), Series MissingCount, a Pending row
// for a grab with no tracked match, and the dedup where a tracked title that is
// also actively grabbing collapses to one row with the grab status winning.
func TestRequestsHandler_AggregatesAndDedups(t *testing.T) {
	grabsStore, libStore, excludesStore := requestsTestStores(t)
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
	mux.HandleFunc("GET /api/requests", requestsHandler(grabsStore, libStore, excludesStore))
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

	if a := byTitle["Movie A"]; a.Status != "Pending" || a.GrabID != grabA.ID {
		t.Errorf("Movie A should dedup to Pending with the grab id, got %+v", a)
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
	if d := byTitle["Movie D"]; d.Status != "Pending" || d.GrabID == 0 {
		t.Errorf("Movie D should be a standalone Pending row, got %+v", d)
	}
}

// TestRequestsHandler_ImportedGrabNotDownloading confirms an already-imported
// grab does not surface as Downloading (it's represented by its In-Library row
// instead) — only in-flight statuses count.
func TestRequestsHandler_ImportedGrabNotDownloading(t *testing.T) {
	grabsStore, libStore, excludesStore := requestsTestStores(t)
	ctx := context.Background()

	g, err := grabsStore.Create(ctx, grabs.Grab{Mode: mode.Movies, Title: "Done Movie", TMDBID: 700})
	if err != nil {
		t.Fatalf("create grab: %v", err)
	}
	if err := grabsStore.UpdateStatus(ctx, g.ID, grabs.Imported); err != nil {
		t.Fatalf("update status: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/requests", requestsHandler(grabsStore, libStore, excludesStore))
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

// TestRequestsHandler_PendingRetrySurfacesHonestly covers BOTH edit points the
// pending_retry status needed. isActiveGrab is the gate deciding whether a grab
// reaches the worklist at all — miss it and the row vanishes from /api/requests
// entirely — and the status mapping must not report it as "Downloading", since
// a pending-retry grab is not downloading anything: it found no qualifying
// release and is parked for a re-search.
func TestRequestsHandler_PendingRetrySurfacesHonestly(t *testing.T) {
	grabsStore, libStore, excludesStore := requestsTestStores(t)
	ctx := context.Background()

	// A standalone pending-retry request (nothing tracked yet).
	parked, err := grabsStore.Create(ctx, grabs.Grab{
		Mode: mode.Movies, Title: "No Match Movie", TMDBID: 900,
		Status: grabs.PendingRetry, RetryAfter: grabs.FormatTime(time.Now().Add(24 * time.Hour)),
		RetryReason: "no candidate cleared the quality floor",
	})
	if err != nil {
		t.Fatalf("create parked grab: %v", err)
	}
	// A tracked title that is ALSO parked — the dedup branch, which must reach
	// the same label as the standalone branch.
	if _, err := libStore.Upsert(ctx, library.Item{Mode: mode.Movies, TMDBID: 901, Title: "Upgrade Me", FilePath: "/m/u.mkv", RootFolderPath: "/m"}); err != nil {
		t.Fatalf("upsert tracked movie: %v", err)
	}
	tracked, err := grabsStore.Create(ctx, grabs.Grab{
		Mode: mode.Movies, Title: "Upgrade Me", TMDBID: 901,
		Status: grabs.PendingRetry, RetryAfter: grabs.FormatTime(time.Now().Add(24 * time.Hour)),
	})
	if err != nil {
		t.Fatalf("create tracked parked grab: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/requests", requestsHandler(grabsStore, libStore, excludesStore))
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

	byTitle := map[string]apidto.RequestStatusItem{}
	for _, it := range out.Items {
		byTitle[it.Title] = it
	}
	if len(out.Items) != 2 {
		t.Fatalf("expected 2 rows, got %d: %+v", len(out.Items), out.Items)
	}
	if got := byTitle["No Match Movie"]; got.Status != "Pending Retry" || got.GrabID != parked.ID {
		t.Errorf("a standalone pending-retry grab should surface as Pending Retry with its id, got %+v", got)
	}
	if got := byTitle["No Match Movie"]; got.RetryAfter != parked.RetryAfter || got.RetryReason != parked.RetryReason {
		t.Errorf("a standalone pending-retry row must carry the grab's RetryAfter/RetryReason (FE-5 needs these to explain the state), got %+v", got)
	}
	if got := byTitle["Upgrade Me"]; got.Status != "Pending Retry" || got.GrabID != tracked.ID {
		t.Errorf("the dedup branch must reach the same label, got %+v", got)
	}
	if got := byTitle["Upgrade Me"]; got.RetryAfter != tracked.RetryAfter {
		t.Errorf("the dedup (flip-existing-row) branch must also carry RetryAfter through, got %+v", got)
	}
}

// TestRequestsHandler_ExcludedTitlesSuppressed proves an excluded title is
// actually skipped by the live Requests aggregation — for BOTH an In-Library row
// (keyed by TMDB id) and a standalone Downloading grab row (keyed by title for an
// Adult scene with no TMDB id) — while a non-excluded sibling still surfaces.
func TestRequestsHandler_ExcludedTitlesSuppressed(t *testing.T) {
	grabsStore, libStore, excludesStore := requestsTestStores(t)
	ctx := context.Background()

	// Two tracked movies; one will be excluded by TMDB id, the other kept.
	if _, err := libStore.Upsert(ctx, library.Item{Mode: mode.Movies, TMDBID: 100, Title: "Excluded Movie", FilePath: "/m/x.mkv", RootFolderPath: "/m"}); err != nil {
		t.Fatalf("upsert excluded movie: %v", err)
	}
	if _, err := libStore.Upsert(ctx, library.Item{Mode: mode.Movies, TMDBID: 200, Title: "Kept Movie", FilePath: "/m/k.mkv", RootFolderPath: "/m"}); err != nil {
		t.Fatalf("upsert kept movie: %v", err)
	}
	// An Adult grab (no TMDB id) that will be excluded by title.
	if _, err := grabsStore.Create(ctx, grabs.Grab{Mode: mode.Adult, Title: "Excluded Scene"}); err != nil {
		t.Fatalf("create adult grab: %v", err)
	}

	// Exclude the movie (by id) and the scene (by title).
	if err := excludesStore.Add(ctx, "movies", 100, "Excluded Movie"); err != nil {
		t.Fatalf("exclude movie: %v", err)
	}
	if err := excludesStore.Add(ctx, "adult", 0, "Excluded Scene"); err != nil {
		t.Fatalf("exclude scene: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/requests", requestsHandler(grabsStore, libStore, excludesStore))
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

	byTitle := map[string]apidto.RequestStatusItem{}
	for _, it := range out.Items {
		byTitle[it.Title] = it
	}
	if _, ok := byTitle["Excluded Movie"]; ok {
		t.Errorf("excluded movie (by tmdb id) should be suppressed, got %+v", out.Items)
	}
	if _, ok := byTitle["Excluded Scene"]; ok {
		t.Errorf("excluded scene (by title) should be suppressed, got %+v", out.Items)
	}
	if _, ok := byTitle["Kept Movie"]; !ok {
		t.Errorf("non-excluded movie should still surface, got %+v", out.Items)
	}
	if len(out.Items) != 1 {
		t.Fatalf("expected exactly 1 surviving row (Kept Movie), got %d: %+v", len(out.Items), out.Items)
	}
}

// TestExcludeTitle_SingleEndpoint proves POST /api/requests/exclude records a
// valid title (204) and persists it, and rejects an item with no usable identity
// (400) without persisting anything.
func TestExcludeTitle_SingleEndpoint(t *testing.T) {
	_, _, excludesStore := requestsTestStores(t)
	ctx := context.Background()

	mux := NewRequestsMux(grabs.New(nil, nil), library.New(nil), excludesStore)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// Valid → 204.
	body, _ := json.Marshal(apidto.ExcludeTitleRequest{Mode: "movies", TMDBID: 7, Title: "Solo"})
	resp, err := http.Post(srv.URL+"/api/requests/exclude", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("valid exclude should be 204, got %d", resp.StatusCode)
	}

	// Invalid (no mode, no id, no title) → 400.
	bad, _ := json.Marshal(apidto.ExcludeTitleRequest{})
	resp2, err := http.Post(srv.URL+"/api/requests/exclude", "application/json", bytes.NewReader(bad))
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid exclude should be 400, got %d", resp2.StatusCode)
	}

	keys, err := excludesStore.Keys(ctx)
	if err != nil {
		t.Fatalf("keys: %v", err)
	}
	if len(keys) != 1 || !keys[excludes.Key("movies", 7, "Solo")] {
		t.Errorf("only the valid exclusion should persist, got %+v", keys)
	}
}

// TestExcludeTitlesBatch_SkipAndContinue proves the bulk exclude endpoint adds
// multiple items and that one invalid item (no mode, no id, no title) is skipped
// with an error while the valid siblings still succeed and persist.
func TestExcludeTitlesBatch_SkipAndContinue(t *testing.T) {
	_, _, excludesStore := requestsTestStores(t)
	ctx := context.Background()

	mux := NewRequestsMux(grabs.New(nil, nil), library.New(nil), excludesStore)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	body, _ := json.Marshal(apidto.ExcludeTitlesBatchRequest{Items: []apidto.ExcludeTitleRequest{
		{Mode: "movies", TMDBID: 1, Title: "Alpha"},
		{Mode: "", TMDBID: 0, Title: ""}, // invalid — no identity
		{Mode: "adult", Title: "Gamma"},
	}})
	resp, err := http.Post(srv.URL+"/api/requests/exclude-batch", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var out apidto.ExcludeTitlesBatchResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Results) != 3 {
		t.Fatalf("expected 3 per-item results, got %d: %+v", len(out.Results), out.Results)
	}
	if !out.Results[0].OK || !out.Results[2].OK {
		t.Errorf("valid items 0 and 2 should succeed, got %+v", out.Results)
	}
	if out.Results[1].OK || out.Results[1].Error == "" {
		t.Errorf("invalid item 1 should be skipped with an error, got %+v", out.Results[1])
	}

	// The two valid exclusions must have actually persisted.
	keys, err := excludesStore.Keys(ctx)
	if err != nil {
		t.Fatalf("keys: %v", err)
	}
	if !keys[excludes.Key("movies", 1, "Alpha")] || !keys[excludes.Key("adult", 0, "Gamma")] {
		t.Errorf("both valid exclusions should persist, got keys %+v", keys)
	}
	if len(keys) != 2 {
		t.Errorf("only the 2 valid exclusions should persist, got %d", len(keys))
	}
}
