package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mode"
)

// newTrackedTestServer wires a mux over a fresh store set and returns the
// library store to seed plus the running server — the 24-argument NewMux call
// is pure boilerplate for the qualityTiers cases below, which touch no store
// but libStore.
func newTrackedTestServer(t *testing.T) (*library.Store, *httptest.Server) {
	t.Helper()
	connStore, propStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, nil, propStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, testFeedHealth(), rssFeedsStore, nil, nil, nil, nil, nil, nil, nil, nil))
	t.Cleanup(srv.Close)
	return libStore, srv
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
// title, and scene-level tags, the same {id, title, tags} shape Movies/Series
// return, keyed on a library_scenes row.
func TestListTracked_Adult_ReturnsSceneLibraryItems(t *testing.T) {
	connStore, propStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	ctx := context.Background()
	scene, err := libStore.UpsertScene(ctx, library.Scene{
		Box: "stashdb", SceneID: "s1", Title: "Some Scene",
		FilePath: "/media/Adult/Some Scene.mp4", RootFolderPath: "/media/Adult",
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
	// CreatedAt is deliberately left unpopulated for Adult (see
	// libraryTrackedItem's doc comment) — Adult has no Library grid to sort,
	// and omitting it (omitempty) keeps this response byte-identical to
	// before the field existed. Locked in here so a future "consistency"
	// edit trips this test.
	if got[0].CreatedAt != "" {
		t.Fatalf("expected createdAt to be omitted for Adult, got %q", got[0].CreatedAt)
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

// TestListTracked_Adult_QualityTiersOmitted — Adult isn't browsable in Library
// (its tag CRUD table has no tier filter), and the Dashboard's Adult cells are
// deliberately non-clickable, so there is nothing for this field to feed.
// Locked in here so a future "consistency" edit trips this test rather than
// silently changing Adult's wire shape.
func TestListTracked_Adult_QualityTiersOmitted(t *testing.T) {
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
	if len(got[0].QualityTiers) != 0 {
		t.Fatalf("expected qualityTiers to be omitted for Adult even with a stored tier, got %+v", got[0].QualityTiers)
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
