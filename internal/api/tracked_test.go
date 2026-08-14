package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mode"
)

// newTrackedMux wires a mux over a fresh store set and returns the library store
// to seed plus the BARE handler — bare so the locked-Adult case can wrap it in
// withLockedAdult, which an already-started server cannot be. The 24-argument
// NewMux call is pure boilerplate for the cases below, which touch no store but
// libStore.
func newTrackedMux(t *testing.T) (*library.Store, *http.ServeMux) {
	t.Helper()
	connStore, propStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	mux := NewMux(testHTTPClient(), connStore, nil, propStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, testFeedHealth(), rssFeedsStore, nil, nil, nil, nil, nil, nil, nil, nil)
	return libStore, mux
}

// newTrackedTestServer is newTrackedMux already serving, for every case that
// needs no middleware between the client and the mux.
func newTrackedTestServer(t *testing.T) (*library.Store, *httptest.Server) {
	t.Helper()
	libStore, mux := newTrackedMux(t)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return libStore, srv
}

// seedScene upserts one scene and returns it with its assigned row id.
func seedScene(t *testing.T, libStore *library.Store, scene library.Scene) library.Scene {
	t.Helper()
	out, err := libStore.UpsertScene(context.Background(), scene)
	if err != nil {
		t.Fatalf("seeding scene: %v", err)
	}
	return out
}

// getTrackedVideo GETs the Adult tracked-video route for one scene id and
// returns the drained response — the body matters to the locked case (it must
// carry no file bytes), the status and headers to the rest.
func getTrackedVideo(t *testing.T, srv *httptest.Server, sceneID int64) (*http.Response, []byte) {
	t.Helper()
	resp, err := http.Get(fmt.Sprintf("%s/api/modes/adult/tracked/%d/video", srv.URL, sceneID))
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading body: %v", err)
	}
	return resp, body
}

// getTrackedItems GETs /api/modes/{m}/tracked and decodes it, failing on
// anything but a 200.
func getTrackedItems(t *testing.T, srv *httptest.Server, m string) []libraryTrackedItem {
	t.Helper()
	resp, err := http.Get(srv.URL + "/api/modes/" + m + "/tracked")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var got []libraryTrackedItem
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	return got
}

// TestListTracked_Adult_ReturnsSceneLibraryItems proves Adult is served
// straight from libStore now (Whisparr eliminated, Stage 4) — scene id,
// title, scene-level tags, Library sort timestamp, and quality tier, keyed on
// a library_scenes row.
func TestListTracked_Adult_ReturnsSceneLibraryItems(t *testing.T) {
	connStore, propStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	ctx := context.Background()
	scene, err := libStore.UpsertScene(ctx, library.Scene{
		Box: "stashdb", SceneID: "s1", Title: "Some Scene",
		Studio: "Studio A", Date: "2024-01-02",
		FilePath: "/media/Adult/Some Scene.mp4", RootFolderPath: "/media/Adult",
		QualityTier: "high",
		PosterURL:   "https://1.1.1.1/scene.jpg",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := libStore.AddSceneTag(ctx, scene.ID, "favorite"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, nil, propStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, testFeedHealth(), rssFeedsStore, nil, nil, nil, nil, nil, nil, nil, nil))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/modes/adult/tracked")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var got []libraryTrackedItem
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if len(got) != 1 || got[0].Title != "Some Scene" || len(got[0].Tags) != 1 || got[0].Tags[0] != "favorite" {
		t.Fatalf("unexpected response: %+v", got)
	}
	if got[0].CreatedAt == "" {
		t.Fatalf("expected createdAt for Adult Library sorting, got %+v", got[0])
	}
	if !reflect.DeepEqual(got[0].QualityTiers, []string{"high"}) {
		t.Fatalf("expected Adult qualityTiers [high], got %+v", got[0].QualityTiers)
	}
	if got[0].VideoURL != fmt.Sprintf("/api/modes/adult/tracked/%d/video", scene.ID) {
		t.Fatalf("expected adult tracked video URL for poster fallback, got %q", got[0].VideoURL)
	}
	if got[0].PosterURL != "https://1.1.1.1/scene.jpg" {
		t.Fatalf("expected catalog poster URL, got %q", got[0].PosterURL)
	}
	if got[0].Box != "stashdb" || got[0].SceneID != "s1" {
		t.Fatalf("expected stored box/sceneId stashdb/s1, got %q/%q", got[0].Box, got[0].SceneID)
	}
	if got[0].Studio != "Studio A" || got[0].Date != "2024-01-02" {
		t.Fatalf("expected stored studio/date, got %q/%q", got[0].Studio, got[0].Date)
	}
}

func TestListTracked_Adult_AspectQuery(t *testing.T) {
	libStore, srv := newTrackedTestServer(t)
	ctx := context.Background()
	if _, err := libStore.UpsertScene(ctx, library.Scene{
		Box: "stashdb", SceneID: "h", Title: "Horiz", RootFolderPath: "/adult",
		PosterAspectClass: library.PosterAspectHorizontal,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := libStore.UpsertScene(ctx, library.Scene{
		Box: "stashdb", SceneID: "v", Title: "Vert", RootFolderPath: "/adult",
		PosterAspectClass: library.PosterAspectVertical,
	}); err != nil {
		t.Fatal(err)
	}

	all := getTrackedItems(t, srv, "adult")
	if len(all) != 2 {
		t.Fatalf("omit aspect: got %d, want 2", len(all))
	}

	resp, err := http.Get(srv.URL + "/api/modes/adult/tracked?aspect=vertical")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("vertical: status %d", resp.StatusCode)
	}
	var vert []libraryTrackedItem
	if err := json.NewDecoder(resp.Body).Decode(&vert); err != nil {
		t.Fatal(err)
	}
	if len(vert) != 1 || vert[0].Title != "Vert" {
		t.Fatalf("vertical filter = %+v", vert)
	}

	bad, err := http.Get(srv.URL + "/api/modes/adult/tracked?aspect=garbage")
	if err != nil {
		t.Fatal(err)
	}
	defer bad.Body.Close()
	if bad.StatusCode != http.StatusBadRequest {
		t.Fatalf("garbage aspect: status %d, want 400", bad.StatusCode)
	}
}

// TestListTracked_Adult_EmptyWhenNoScenes proves Adult needs no *arr
// connection at all now — an empty library returns an empty list with 200,
// not a 400 (the old Whisparr-missing behavior).
func TestListTracked_Adult_EmptyWhenNoScenes(t *testing.T) {
	connStore, propStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, nil, propStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, testFeedHealth(), rssFeedsStore, nil, nil, nil, nil, nil, nil, nil, nil))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/modes/adult/tracked")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 with no connection and an empty library, got %d", resp.StatusCode)
	}
	var got []libraryTrackedItem
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected an empty list, got %+v", got)
	}
}

func TestListTracked_Adult_OmitsVideoURLWhenSceneHasNoFile(t *testing.T) {
	libStore, srv := newTrackedTestServer(t)
	seedScene(t, libStore, library.Scene{
		Box: "stashdb", SceneID: "s1", Title: "No File", RootFolderPath: "/media/Adult",
	})

	got := getTrackedItems(t, srv, "adult")
	if len(got) != 1 {
		t.Fatalf("expected 1 scene, got %+v", got)
	}
	if got[0].VideoURL != "" {
		t.Fatalf("expected videoUrl omitted when FilePath is empty, got %q", got[0].VideoURL)
	}
}

func TestTrackedVideoHandler_AdultServesFile(t *testing.T) {
	libStore, srv := newTrackedTestServer(t)
	path := filepath.Join(t.TempDir(), "scene.mp4")
	if err := os.WriteFile(path, []byte("video-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	scene := seedScene(t, libStore, library.Scene{
		Box: "stashdb", SceneID: "s1", Title: "Some Scene",
		FilePath: path, RootFolderPath: filepath.Dir(path),
	})

	resp, body := getTrackedVideo(t, srv, scene.ID)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%q", resp.StatusCode, body)
	}
	if got := resp.Header.Get("Cache-Control"); got != "private, max-age=3600" {
		t.Fatalf("Cache-Control = %q, want private max-age", got)
	}
}

func TestTrackedVideoHandler_AdultRejectsDirectory(t *testing.T) {
	libStore, srv := newTrackedTestServer(t)
	dir := t.TempDir()
	scene := seedScene(t, libStore, library.Scene{
		Box: "stashdb", SceneID: "s1", Title: "Some Scene",
		FilePath: dir, RootFolderPath: dir,
	})

	resp, _ := getTrackedVideo(t, srv, scene.ID)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 for directory path, got %d", resp.StatusCode)
	}
}

func TestTrackedVideoHandler_AdultRejectsEmptyPath(t *testing.T) {
	libStore, srv := newTrackedTestServer(t)
	scene := seedScene(t, libStore, library.Scene{
		Box: "stashdb", SceneID: "s1", Title: "Some Scene", RootFolderPath: "/adult",
	})

	resp, _ := getTrackedVideo(t, srv, scene.ID)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 for empty path, got %d", resp.StatusCode)
	}
}

// TestTrackedVideoHandler_AdultLockedRefusesBeforeAnyBytes pins the ordering:
// the Adult section-lock check runs before the file is opened, so a locked
// request answers 403 without a single byte of the scene reaching the client. A
// status-only assertion would pass against a handler that streams first and
// reports the lock afterwards, hence the marker.
func TestTrackedVideoHandler_AdultLockedRefusesBeforeAnyBytes(t *testing.T) {
	const marker = "ADULT-TRACKED-VIDEO-BYTES-DO-NOT-SERVE"
	libStore, mux := newTrackedMux(t)
	path := filepath.Join(t.TempDir(), "scene.mp4")
	if err := os.WriteFile(path, []byte(marker), 0o644); err != nil {
		t.Fatal(err)
	}
	scene := seedScene(t, libStore, library.Scene{
		Box: "stashdb", SceneID: "s1", Title: "Some Scene",
		FilePath: path, RootFolderPath: filepath.Dir(path),
	})

	srv := httptest.NewServer(withLockedAdult(mux))
	defer srv.Close()

	resp, body := getTrackedVideo(t, srv, scene.ID)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 while Adult is locked, got %d body=%q", resp.StatusCode, body)
	}
	if strings.Contains(string(body), marker) {
		t.Fatalf("locked Adult video response leaked marker bytes: %q", body)
	}
}

// TestListTracked_Movies_ReturnsLibraryItemsWithLabelTags proves Movies
// never touches Radarr at all — it's served straight from libStore, with
// Tags as label strings (not numeric Servarr tag ids).
func TestListTracked_Movies_ReturnsLibraryItemsWithLabelTags(t *testing.T) {
	connStore, propStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	ctx := context.Background()
	item, err := libStore.Upsert(ctx, library.Item{Mode: mode.Movies, TMDBID: 453, Title: "A Beautiful Mind", Year: 2001, RootFolderPath: "/movies"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := libStore.AddTag(ctx, item.ID, "favorite"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, nil, propStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, testFeedHealth(), rssFeedsStore, nil, nil, nil, nil, nil, nil, nil, nil))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/modes/movies/tracked")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 with zero Radarr connection configured, got %d", resp.StatusCode)
	}
	var got []libraryTrackedItem
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if len(got) != 1 || got[0].Title != "A Beautiful Mind" || len(got[0].Tags) != 1 || got[0].Tags[0] != "favorite" {
		t.Fatalf("unexpected response: %+v", got)
	}
	// Movies carry TMDBID/Year through so Discover's existing-library row can
	// render a real poster card (lazy poster + availability + auto-grab all
	// key off tmdbId; year is display).
	if got[0].TMDBID != 453 || got[0].Year != 2001 {
		t.Fatalf("expected tmdbId 453 / year 2001 on the tracked item, got %+v", got[0])
	}
	// CreatedAt is populated for Movies (Library's added-date sort relies on
	// this being non-empty).
	if got[0].CreatedAt == "" {
		t.Fatalf("expected createdAt to be populated for Movies, got %+v", got[0])
	}
}

// TestListTracked_Series_ReturnsLibrarySeriesWithLabelTags proves Series
// never touches Sonarr at all now — it's served straight from libStore,
// same shape as Movies.
func TestListTracked_Series_ReturnsLibrarySeriesWithLabelTags(t *testing.T) {
	connStore, propStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	ctx := context.Background()
	series, err := libStore.UpsertSeries(ctx, library.Series{TMDBID: 555, Title: "Some Show", Year: 2019, RootFolderPath: "/tv"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := libStore.AddSeriesTag(ctx, series.ID, "favorite"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, nil, propStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, testFeedHealth(), rssFeedsStore, nil, nil, nil, nil, nil, nil, nil, nil))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/modes/series/tracked")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 with zero Sonarr connection configured, got %d", resp.StatusCode)
	}
	var got []libraryTrackedItem
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if len(got) != 1 || got[0].Title != "Some Show" || len(got[0].Tags) != 1 || got[0].Tags[0] != "favorite" {
		t.Fatalf("unexpected response: %+v", got)
	}
	if got[0].TMDBID != 555 || got[0].Year != 2019 {
		t.Fatalf("expected tmdbId 555 / year 2019 on the tracked series, got %+v", got[0])
	}
	// CreatedAt is populated for Series too (Library's added-date sort
	// relies on this being non-empty).
	if got[0].CreatedAt == "" {
		t.Fatalf("expected createdAt to be populated for Series, got %+v", got[0])
	}
}

// TestListTracked_Movies_QualityTierIsASingleElementSlice — a movie has one
// file, so one tier.
func TestListTracked_Movies_QualityTierIsASingleElementSlice(t *testing.T) {
	libStore, srv := newTrackedTestServer(t)
	ctx := context.Background()
	if _, err := libStore.Upsert(ctx, library.Item{
		Mode: mode.Movies, TMDBID: 453, Title: "A Beautiful Mind", Year: 2001,
		FilePath: "/movies/A Beautiful Mind/a.mkv", RootFolderPath: "/movies", QualityTier: "high",
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got := getTrackedItems(t, srv, "movies")
	if len(got) != 1 {
		t.Fatalf("expected 1 item, got %+v", got)
	}
	if len(got[0].QualityTiers) != 1 || got[0].QualityTiers[0] != "high" {
		t.Fatalf("expected qualityTiers [high], got %+v", got[0].QualityTiers)
	}
}

// TestListTracked_Movies_EmptyQualityTierFoldsToUnknown is the drill-down-side
// half of the Unknown-cell fix: the Dashboard folds a never-captured "" into
// the same visible, CLICKABLE Unknown cell as a genuinely-inferred "unknown",
// so this field must fold too. Omitting it (the obvious alternative) would
// leave the frontend's `qualityTiers.includes(tier)` predicate unable to match
// these rows, and a real nonzero cell would drill down to an empty list.
func TestListTracked_Movies_EmptyQualityTierFoldsToUnknown(t *testing.T) {
	libStore, srv := newTrackedTestServer(t)
	ctx := context.Background()
	// QualityTier deliberately left "" — a row the boot-time backfill hasn't
	// reached yet.
	item, err := libStore.Upsert(ctx, library.Item{
		Mode: mode.Movies, TMDBID: 453, Title: "Unbackfilled", Year: 2001,
		FilePath: "/movies/Unbackfilled/a.mkv", RootFolderPath: "/movies",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got := getTrackedItems(t, srv, "movies")
	if len(got) != 1 {
		t.Fatalf("expected 1 item, got %+v", got)
	}
	if len(got[0].QualityTiers) != 1 || got[0].QualityTiers[0] != "unknown" {
		t.Fatalf("expected qualityTiers [unknown] (folded, NOT omitted), got %+v", got[0].QualityTiers)
	}
	// The fold is display-only — the stored sentinel is untouched, which is
	// what keeps BackfillSizeAndTier's `WHERE quality_tier = ''` idempotent.
	stored, err := libStore.Get(ctx, item.ID)
	if err != nil {
		t.Fatalf("re-reading the item: %v", err)
	}
	if stored.QualityTier != "" {
		t.Fatalf("expected the stored quality_tier to stay \"\", got %q", stored.QualityTier)
	}
}

// TestListTracked_Series_QualityTiersAreTheDistinctEpisodeTiers — a series
// genuinely has more than one tier (episodes are grabbed at different times
// under different settings), so the field is a deduped, sorted set rather than
// a fabricated single majority value.
func TestListTracked_Series_QualityTiersAreTheDistinctEpisodeTiers(t *testing.T) {
	libStore, srv := newTrackedTestServer(t)
	ctx := context.Background()
	series, err := libStore.UpsertSeries(ctx, library.Series{TMDBID: 555, Title: "Some Show", Year: 2019, RootFolderPath: "/tv"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, ep := range []struct {
		num  int
		tier string
	}{{1, "lossless"}, {2, "high"}, {3, "high"}} {
		if _, err := libStore.UpsertEpisode(ctx, library.Episode{
			SeriesID: series.ID, SeasonNumber: 1, EpisodeNumber: ep.num,
			FilePath: fmt.Sprintf("/tv/Some Show/s01e%02d.mkv", ep.num), QualityTier: ep.tier,
		}); err != nil {
			t.Fatalf("seeding episode %d: %v", ep.num, err)
		}
	}

	got := getTrackedItems(t, srv, "series")
	if len(got) != 1 {
		t.Fatalf("expected 1 series, got %+v", got)
	}
	// Deduped ("high" appears twice on disk) and sorted.
	want := []string{"high", "lossless"}
	if !reflect.DeepEqual(got[0].QualityTiers, want) {
		t.Fatalf("expected qualityTiers %v (deduped, sorted), got %+v", want, got[0].QualityTiers)
	}
}

// TestListTracked_Series_UnbackfilledEpisodeFoldsToUnknown is the Series half
// of the Unknown-cell fix — same reasoning as the Movies case above, applied to
// EpisodeTiersBySeries' query, which deliberately carries no filter excluding
// the empty-string (never-processed) tier sentinel.
func TestListTracked_Series_UnbackfilledEpisodeFoldsToUnknown(t *testing.T) {
	libStore, srv := newTrackedTestServer(t)
	ctx := context.Background()
	series, err := libStore.UpsertSeries(ctx, library.Series{TMDBID: 555, Title: "Some Show", Year: 2019, RootFolderPath: "/tv"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// One backfilled episode, one not.
	if _, err := libStore.UpsertEpisode(ctx, library.Episode{
		SeriesID: series.ID, SeasonNumber: 1, EpisodeNumber: 1,
		FilePath: "/tv/Some Show/s01e01.mkv", QualityTier: "high",
	}); err != nil {
		t.Fatalf("seeding episode: %v", err)
	}
	if _, err := libStore.UpsertEpisode(ctx, library.Episode{
		SeriesID: series.ID, SeasonNumber: 1, EpisodeNumber: 2,
		FilePath: "/tv/Some Show/s01e02.mkv",
	}); err != nil {
		t.Fatalf("seeding episode: %v", err)
	}

	got := getTrackedItems(t, srv, "series")
	if len(got) != 1 {
		t.Fatalf("expected 1 series, got %+v", got)
	}
	want := []string{"high", "unknown"}
	if !reflect.DeepEqual(got[0].QualityTiers, want) {
		t.Fatalf("expected qualityTiers %v (the unbackfilled episode folded to unknown, not dropped), got %+v", want, got[0].QualityTiers)
	}
}

func TestListTracked_Adult_QualityTiersPresent(t *testing.T) {
	libStore, srv := newTrackedTestServer(t)
	ctx := context.Background()
	if _, err := libStore.UpsertScene(ctx, library.Scene{
		Box: "stashdb", SceneID: "s1", Title: "Some Scene",
		FilePath: "/media/Adult/Some Scene.mp4", RootFolderPath: "/media/Adult",
		QualityTier: "high",
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got := getTrackedItems(t, srv, "adult")
	if len(got) != 1 {
		t.Fatalf("expected 1 scene, got %+v", got)
	}
	if !reflect.DeepEqual(got[0].QualityTiers, []string{"high"}) {
		t.Fatalf("expected Adult qualityTiers [high], got %+v", got[0].QualityTiers)
	}
}

// TestListTracked_CreatedAt_IsLexicographicallySortable proves the property
// Library.tsx's "Newest first" sort relies on: two items upserted in
// insertion order have createdAt values whose plain string (lexicographic)
// comparison matches that insertion order. created_at is written by
// strftime('%Y-%m-%dT%H:%M:%fZ', 'now') (internal/library/library.go), a
// fixed-width, zero-padded ISO-8601-with-milliseconds format, which is
// lexicographically sortable by construction. Tolerant of a same-millisecond
// tie (<=, not <) rather than sleeping between inserts — sleeping would make
// this test slower and flakier under a loaded CI runner for no real gain,
// since a tie is the correctly-handled case (§3.2: ties are acceptable, item
// order among them is unspecified), not a failure.
func TestListTracked_CreatedAt_IsLexicographicallySortable(t *testing.T) {
	connStore, propStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	ctx := context.Background()
	first, err := libStore.Upsert(ctx, library.Item{Mode: mode.Movies, TMDBID: 100, Title: "First", Year: 2000, RootFolderPath: "/movies"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	second, err := libStore.Upsert(ctx, library.Item{Mode: mode.Movies, TMDBID: 200, Title: "Second", Year: 2001, RootFolderPath: "/movies"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, nil, propStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, testFeedHealth(), rssFeedsStore, nil, nil, nil, nil, nil, nil, nil, nil))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/modes/movies/tracked")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	var got []libraryTrackedItem
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	byID := map[int64]string{}
	for _, item := range got {
		byID[item.ID] = item.CreatedAt
	}
	firstCreatedAt, secondCreatedAt := byID[first.ID], byID[second.ID]
	if firstCreatedAt == "" || secondCreatedAt == "" {
		t.Fatalf("expected both items to have createdAt populated, got first=%q second=%q", firstCreatedAt, secondCreatedAt)
	}
	if firstCreatedAt > secondCreatedAt {
		t.Fatalf("expected first-inserted item's createdAt (%q) to sort <= second-inserted item's createdAt (%q)", firstCreatedAt, secondCreatedAt)
	}
}

func seedMovie(t *testing.T, libStore *library.Store, item library.Item) library.Item {
	t.Helper()
	out, err := libStore.Upsert(context.Background(), item)
	if err != nil {
		t.Fatalf("seeding movie: %v", err)
	}
	return out
}

func getModeTrackedVideo(t *testing.T, srv *httptest.Server, m string, id int64, query string) (*http.Response, []byte) {
	t.Helper()
	resp, err := http.Get(fmt.Sprintf("%s/api/modes/%s/tracked/%d/video%s", srv.URL, m, id, query))
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading body: %v", err)
	}
	return resp, body
}

func TestListTracked_Movies_SetsVideoURLWhenPlayable(t *testing.T) {
	libStore, srv := newTrackedTestServer(t)
	ctx := context.Background()
	item := seedMovie(t, libStore, library.Item{
		Mode: mode.Movies, TMDBID: 27205, Title: "Inception", Year: 2010,
		FilePath: "/movies/Inception/Inception.mp4", RootFolderPath: "/movies",
	})
	files, err := libStore.ListFiles(ctx, item.ID)
	if err != nil || len(files) != 1 {
		t.Fatalf("expected 1 synced primary file, got %v err=%v", files, err)
	}

	got := getTrackedItems(t, srv, "movies")
	if len(got) != 1 {
		t.Fatalf("expected 1 item, got %+v", got)
	}
	wantItem := fmt.Sprintf("/api/modes/movies/tracked/%d/video", item.ID)
	if got[0].VideoURL != wantItem {
		t.Fatalf("item videoUrl = %q, want %q", got[0].VideoURL, wantItem)
	}
	wantFile := fmt.Sprintf("/api/modes/movies/tracked/%d/video?fileId=%d", item.ID, files[0].ID)
	if len(got[0].Files) != 1 || got[0].Files[0].VideoURL != wantFile {
		t.Fatalf("file videoUrl = %+v, want %q", got[0].Files, wantFile)
	}
}

func TestListTracked_Movies_OmitsVideoURLForMkv(t *testing.T) {
	libStore, srv := newTrackedTestServer(t)
	seedMovie(t, libStore, library.Item{
		Mode: mode.Movies, TMDBID: 27205, Title: "Inception", Year: 2010,
		FilePath: "/movies/Inception/Inception.mkv", RootFolderPath: "/movies",
	})

	got := getTrackedItems(t, srv, "movies")
	if len(got) != 1 {
		t.Fatalf("expected 1 item, got %+v", got)
	}
	if got[0].VideoURL != "" {
		t.Fatalf("expected item videoUrl omitted for mkv, got %q", got[0].VideoURL)
	}
	if len(got[0].Files) != 1 || got[0].Files[0].VideoURL != "" {
		t.Fatalf("expected file videoUrl omitted for mkv, got %+v", got[0].Files)
	}
}

func TestTrackedVideoHandler_MoviesServesPrimaryFile(t *testing.T) {
	libStore, srv := newTrackedTestServer(t)
	path := filepath.Join(t.TempDir(), "movie.mp4")
	if err := os.WriteFile(path, []byte("movie-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	item := seedMovie(t, libStore, library.Item{
		Mode: mode.Movies, TMDBID: 1, Title: "Some Movie", Year: 2020,
		FilePath: path, RootFolderPath: filepath.Dir(path),
	})

	resp, body := getModeTrackedVideo(t, srv, "movies", item.ID, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%q", resp.StatusCode, body)
	}
	if string(body) != "movie-bytes" {
		t.Fatalf("body = %q, want movie-bytes", body)
	}
	if got := resp.Header.Get("Cache-Control"); got != "private, max-age=3600" {
		t.Fatalf("Cache-Control = %q, want private max-age", got)
	}
}

func TestTrackedVideoHandler_MoviesServesFileId(t *testing.T) {
	libStore, srv := newTrackedTestServer(t)
	dir := t.TempDir()
	primary := filepath.Join(dir, "primary.mp4")
	alt := filepath.Join(dir, "alt.mp4")
	if err := os.WriteFile(primary, []byte("primary-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(alt, []byte("alt-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	item := seedMovie(t, libStore, library.Item{
		Mode: mode.Movies, TMDBID: 1, Title: "Some Movie", Year: 2020,
		FilePath: primary, RootFolderPath: dir,
	})
	altFile, err := libStore.UpsertFile(context.Background(), library.ItemFile{
		ItemID: item.ID, FilePath: alt, IsPrimary: false,
	})
	if err != nil {
		t.Fatal(err)
	}

	resp, body := getModeTrackedVideo(t, srv, "movies", item.ID, fmt.Sprintf("?fileId=%d", altFile.ID))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%q", resp.StatusCode, body)
	}
	if string(body) != "alt-bytes" {
		t.Fatalf("body = %q, want alt-bytes", body)
	}
}

func TestTrackedVideoHandler_MoviesFileIdMustBelongToItem(t *testing.T) {
	libStore, srv := newTrackedTestServer(t)
	dir := t.TempDir()
	aPath := filepath.Join(dir, "a.mp4")
	bPath := filepath.Join(dir, "b.mp4")
	if err := os.WriteFile(aPath, []byte("aaa"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bPath, []byte("bbb"), 0o644); err != nil {
		t.Fatal(err)
	}
	a := seedMovie(t, libStore, library.Item{
		Mode: mode.Movies, TMDBID: 1, Title: "A", Year: 2020,
		FilePath: aPath, RootFolderPath: dir,
	})
	b := seedMovie(t, libStore, library.Item{
		Mode: mode.Movies, TMDBID: 2, Title: "B", Year: 2021,
		FilePath: bPath, RootFolderPath: dir,
	})
	bFiles, err := libStore.ListFiles(context.Background(), b.ID)
	if err != nil || len(bFiles) != 1 {
		t.Fatalf("b files: %v err=%v", bFiles, err)
	}

	resp, body := getModeTrackedVideo(t, srv, "movies", a.ID, fmt.Sprintf("?fileId=%d", bFiles[0].ID))
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 for sibling fileId, got %d body=%q", resp.StatusCode, body)
	}
	if strings.Contains(string(body), "bbb") {
		t.Fatalf("sibling file bytes leaked: %q", body)
	}
}

func TestTrackedVideoHandler_MoviesRejectsDirectory(t *testing.T) {
	libStore, srv := newTrackedTestServer(t)
	dir := t.TempDir()
	item := seedMovie(t, libStore, library.Item{
		Mode: mode.Movies, TMDBID: 1, Title: "Some Movie", Year: 2020,
		FilePath: dir, RootFolderPath: dir,
	})

	resp, _ := getModeTrackedVideo(t, srv, "movies", item.ID, "")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 for directory path, got %d", resp.StatusCode)
	}
}

func TestTrackedVideoHandler_MoviesRejectsEmptyPath(t *testing.T) {
	libStore, srv := newTrackedTestServer(t)
	item := seedMovie(t, libStore, library.Item{
		Mode: mode.Movies, TMDBID: 1, Title: "Some Movie", Year: 2020,
		RootFolderPath: "/movies",
	})

	resp, _ := getModeTrackedVideo(t, srv, "movies", item.ID, "")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 for empty path, got %d", resp.StatusCode)
	}
}

func TestTrackedVideoHandler_MoviesInvalidFileId(t *testing.T) {
	libStore, srv := newTrackedTestServer(t)
	item := seedMovie(t, libStore, library.Item{
		Mode: mode.Movies, TMDBID: 1, Title: "Some Movie", Year: 2020,
		RootFolderPath: "/movies",
	})

	resp, _ := getModeTrackedVideo(t, srv, "movies", item.ID, "?fileId=abc")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for non-numeric fileId, got %d", resp.StatusCode)
	}
}

func TestTrackedVideoHandler_SeriesStillUnsupported(t *testing.T) {
	_, srv := newTrackedTestServer(t)
	resp, _ := getModeTrackedVideo(t, srv, "series", 1, "")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for series tracked video, got %d", resp.StatusCode)
	}
}

func TestTrackedVideoHandler_MoviesServesWhileAdultLocked(t *testing.T) {
	const marker = "MOVIE-TRACKED-VIDEO-BYTES"
	libStore, mux := newTrackedMux(t)
	path := filepath.Join(t.TempDir(), "movie.mp4")
	if err := os.WriteFile(path, []byte(marker), 0o644); err != nil {
		t.Fatal(err)
	}
	item := seedMovie(t, libStore, library.Item{
		Mode: mode.Movies, TMDBID: 1, Title: "Some Movie", Year: 2020,
		FilePath: path, RootFolderPath: filepath.Dir(path),
	})

	srv := httptest.NewServer(withLockedAdult(mux))
	defer srv.Close()

	resp, body := getModeTrackedVideo(t, srv, "movies", item.ID, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 while Adult is locked, got %d body=%q", resp.StatusCode, body)
	}
	if string(body) != marker {
		t.Fatalf("body = %q, want marker", body)
	}
}
