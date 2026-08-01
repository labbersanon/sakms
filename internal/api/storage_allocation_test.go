package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mode"
)

// storageAllocationCellFor pulls one (mode, tier) intersection out of the
// response, failing if the grid isn't dense — the endpoint's contract is that
// every combination is always present, zeroes included.
func storageAllocationCellFor(t *testing.T, got storageAllocationResponse, m, tier string) storageAllocationCell {
	t.Helper()
	for _, row := range got.Rows {
		if row.Mode != m {
			continue
		}
		for _, cell := range row.Cells {
			if cell.Tier == tier {
				return cell
			}
		}
		t.Fatalf("mode %q has no %q cell (grid is not dense): %+v", m, tier, row.Cells)
	}
	t.Fatalf("no %q row in the response: %+v", m, got.Rows)
	return storageAllocationCell{}
}

// TestStorageAllocation_ReturnsDenseGrid is the route's happy path: 200, and
// the full 3x5 envelope with real totals from a seeded store.
func TestStorageAllocation_ReturnsDenseGrid(t *testing.T) {
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	ctx := context.Background()

	if _, err := libStore.Upsert(ctx, library.Item{
		Mode: mode.Movies, TMDBID: 1, Title: "A Movie", Year: 2020,
		FilePath: "/movies/A Movie/a.mkv", RootFolderPath: "/movies",
		Size: 4_096, QualityTier: "lossless",
	}); err != nil {
		t.Fatalf("seeding movie: %v", err)
	}
	if _, err := libStore.UpsertScene(ctx, library.Scene{
		Box: "stashdb", SceneID: "s1", Title: "A Scene",
		FilePath: "/adult/a.mp4", RootFolderPath: "/adult",
		Size: 2_048, QualityTier: "medium",
	}); err != nil {
		t.Fatalf("seeding scene: %v", err)
	}

	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, nil, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, testFeedHealth(), rssFeedsStore, nil, nil, nil, nil, nil, nil))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/admin/storage-allocation")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var got storageAllocationResponse
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decoding response: %v", err)
	}

	wantTiers := []string{"low", "medium", "high", "lossless", "unknown"}
	if len(got.Tiers) != len(wantTiers) {
		t.Fatalf("expected the fixed 5-tier axis, got %+v", got.Tiers)
	}
	for i, tier := range wantTiers {
		if got.Tiers[i] != tier {
			t.Errorf("tier axis position %d: expected %q, got %q", i, tier, got.Tiers[i])
		}
	}
	wantModes := []string{"movies", "series", "adult"}
	if len(got.Rows) != len(wantModes) {
		t.Fatalf("expected 3 mode rows, got %d: %+v", len(got.Rows), got.Rows)
	}
	for i, m := range wantModes {
		if got.Rows[i].Mode != m {
			t.Errorf("row %d: expected mode %q, got %q", i, m, got.Rows[i].Mode)
		}
		if len(got.Rows[i].Cells) != len(wantTiers) {
			t.Errorf("row %q: expected 5 cells, got %d", m, len(got.Rows[i].Cells))
		}
	}

	if cell := storageAllocationCellFor(t, got, "movies", "lossless"); cell.TotalBytes != 4_096 || cell.ItemCount != 1 {
		t.Errorf("movies/lossless: expected 4096 bytes / 1 item, got %+v", cell)
	}
	if cell := storageAllocationCellFor(t, got, "adult", "medium"); cell.TotalBytes != 2_048 || cell.ItemCount != 1 {
		t.Errorf("adult/medium: expected 2048 bytes / 1 item, got %+v", cell)
	}
	// Series has nothing seeded and is still a full row of zero cells.
	if got.Rows[1].RowTotalBytes != 0 || got.Rows[1].RowItemCount != 0 {
		t.Errorf("expected an empty series row, got %+v", got.Rows[1])
	}
	if got.GrandTotalBytes != 6_144 {
		t.Errorf("expected grand total 6144, got %d", got.GrandTotalBytes)
	}
}

// TestStorageAllocationDoesNoDiskIO turns "this endpoint makes no per-request
// disk I/O" from a code-review claim into an assertion. Every seeded row points
// at a path that does not exist; if the handler ever grew an os.Stat, the sizes
// would come back 0 and this fails. The seeded sizes are deliberately odd,
// non-round numbers so the assertion can't be satisfied by a plausible default.
func TestStorageAllocationDoesNoDiskIO(t *testing.T) {
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	ctx := context.Background()

	if _, err := libStore.Upsert(ctx, library.Item{
		Mode: mode.Movies, TMDBID: 1, Title: "Gone Movie", Year: 2020,
		FilePath: "/nonexistent/movies/Gone Movie/gone.mkv", RootFolderPath: "/nonexistent/movies",
		Size: 1_234_567, QualityTier: "high",
	}); err != nil {
		t.Fatalf("seeding movie: %v", err)
	}
	series, err := libStore.UpsertSeries(ctx, library.Series{TMDBID: 10, Title: "Gone Show", Year: 2019, RootFolderPath: "/nonexistent/tv"})
	if err != nil {
		t.Fatalf("seeding series: %v", err)
	}
	if _, err := libStore.UpsertEpisode(ctx, library.Episode{
		SeriesID: series.ID, SeasonNumber: 1, EpisodeNumber: 1,
		FilePath: "/nonexistent/tv/Gone Show/s01e01.mkv", Size: 7_654_321, QualityTier: "high",
	}); err != nil {
		t.Fatalf("seeding episode: %v", err)
	}
	if _, err := libStore.UpsertScene(ctx, library.Scene{
		Box: "stashdb", SceneID: "s1", Title: "Gone Scene",
		FilePath: "/nonexistent/adult/gone.mp4", RootFolderPath: "/nonexistent/adult",
		Size: 9_876_543, QualityTier: "high",
	}); err != nil {
		t.Fatalf("seeding scene: %v", err)
	}

	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, nil, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, testFeedHealth(), rssFeedsStore, nil, nil, nil, nil, nil, nil))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/admin/storage-allocation")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var got storageAllocationResponse
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decoding response: %v", err)
	}

	for _, tc := range []struct {
		mode string
		want int64
	}{
		{"movies", 1_234_567},
		{"series", 7_654_321},
		{"adult", 9_876_543},
	} {
		cell := storageAllocationCellFor(t, got, tc.mode, "high")
		if cell.TotalBytes != tc.want {
			t.Errorf("%s/high: expected the DB-stored %d bytes for a path that doesn't exist (an os.Stat would return 0), got %d", tc.mode, tc.want, cell.TotalBytes)
		}
	}
	if got.GrandTotalBytes != 1_234_567+7_654_321+9_876_543 {
		t.Errorf("expected the grand total to come entirely from the DB, got %d", got.GrandTotalBytes)
	}
}
