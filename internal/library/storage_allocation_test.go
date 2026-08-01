package library

import (
	"context"
	"fmt"
	"testing"

	"github.com/labbersanon/sakms/internal/mode"
)

// cellFor pulls one (mode, tier) intersection out of the dense grid, failing
// the test if the grid isn't dense — every assertion below relies on the cell
// existing whether or not any row backs it.
func cellFor(t *testing.T, alloc StorageAllocation, m, tier string) StorageAllocationCell {
	t.Helper()
	for _, row := range alloc.Rows {
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
	t.Fatalf("no %q row in the grid: %+v", m, alloc.Rows)
	return StorageAllocationCell{}
}

// TestStorageAllocationGroupsByModeAndTier is the base case: real rows in three
// modes and three tiers, and the result is ALWAYS a full 3x5 grid — the
// combinations nothing backs are emitted as zero cells rather than omitted, so
// the frontend never has to invent a missing cell.
func TestStorageAllocationGroupsByModeAndTier(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if _, err := s.Upsert(ctx, Item{
		Mode: mode.Movies, TMDBID: 1, Title: "A Movie", Year: 2020,
		FilePath: "/movies/A Movie/a.mkv", RootFolderPath: "/movies",
		Size: 1_000, QualityTier: "high",
	}); err != nil {
		t.Fatalf("seeding movie: %v", err)
	}
	if _, err := s.Upsert(ctx, Item{
		Mode: mode.Movies, TMDBID: 2, Title: "Another Movie", Year: 2021,
		FilePath: "/movies/Another Movie/b.mkv", RootFolderPath: "/movies",
		Size: 500, QualityTier: "high",
	}); err != nil {
		t.Fatalf("seeding movie: %v", err)
	}

	series, err := s.UpsertSeries(ctx, Series{TMDBID: 10, Title: "A Show", Year: 2019, RootFolderPath: "/tv"})
	if err != nil {
		t.Fatalf("seeding series: %v", err)
	}
	if _, err := s.UpsertEpisode(ctx, Episode{
		SeriesID: series.ID, SeasonNumber: 1, EpisodeNumber: 1, Title: "Ep1",
		FilePath: "/tv/A Show/s01e01.mkv", Size: 2_000, QualityTier: "medium",
	}); err != nil {
		t.Fatalf("seeding episode: %v", err)
	}

	if _, err := s.UpsertScene(ctx, Scene{
		Box: "stashdb", SceneID: "s1", Title: "A Scene",
		FilePath: "/adult/a.mp4", RootFolderPath: "/adult",
		Size: 3_000, QualityTier: "lossless",
	}); err != nil {
		t.Fatalf("seeding scene: %v", err)
	}

	alloc, err := s.StorageAllocation(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// The grid shape itself is part of the contract.
	if len(alloc.Rows) != 3 {
		t.Fatalf("expected 3 mode rows, got %d: %+v", len(alloc.Rows), alloc.Rows)
	}
	for i, wantMode := range StorageAllocationModes {
		if alloc.Rows[i].Mode != wantMode {
			t.Errorf("row %d: expected mode %q, got %q", i, wantMode, alloc.Rows[i].Mode)
		}
		if len(alloc.Rows[i].Cells) != len(StorageAllocationTiers) {
			t.Fatalf("row %q: expected %d cells, got %d", wantMode, len(StorageAllocationTiers), len(alloc.Rows[i].Cells))
		}
		for j, wantTier := range StorageAllocationTiers {
			if alloc.Rows[i].Cells[j].Tier != wantTier {
				t.Errorf("row %q cell %d: expected tier %q, got %q", wantMode, j, wantTier, alloc.Rows[i].Cells[j].Tier)
			}
		}
	}

	if got := cellFor(t, alloc, "movies", "high"); got.TotalBytes != 1_500 || got.ItemCount != 2 {
		t.Errorf("movies/high: expected 1500 bytes / 2 items, got %+v", got)
	}
	if got := cellFor(t, alloc, "series", "medium"); got.TotalBytes != 2_000 || got.ItemCount != 1 {
		t.Errorf("series/medium: expected 2000 bytes / 1 item, got %+v", got)
	}
	if got := cellFor(t, alloc, "adult", "lossless"); got.TotalBytes != 3_000 || got.ItemCount != 1 {
		t.Errorf("adult/lossless: expected 3000 bytes / 1 item, got %+v", got)
	}
	// A combination nothing backs is present and zero, not absent.
	if got := cellFor(t, alloc, "movies", "low"); got.TotalBytes != 0 || got.ItemCount != 0 {
		t.Errorf("movies/low: expected an empty zero cell, got %+v", got)
	}
	if got := cellFor(t, alloc, "adult", "unknown"); got.TotalBytes != 0 || got.ItemCount != 0 {
		t.Errorf("adult/unknown: expected an empty zero cell, got %+v", got)
	}

	if alloc.Rows[0].RowTotalBytes != 1_500 || alloc.Rows[0].RowItemCount != 2 {
		t.Errorf("movies row totals: expected 1500/2, got %d/%d", alloc.Rows[0].RowTotalBytes, alloc.Rows[0].RowItemCount)
	}
	if alloc.GrandTotalBytes != 6_500 {
		t.Errorf("expected grand total 6500, got %d", alloc.GrandTotalBytes)
	}
	if len(alloc.Tiers) != 5 {
		t.Errorf("expected the fixed 5-tier axis, got %+v", alloc.Tiers)
	}
}

// TestStorageAllocationDeduplicatesMultiEpisodeFiles pins the rule that makes
// Series bytes honest: one physical file backing two episode rows (a S01E01-E02
// double episode, or a season pack broken into slots) contributes its bytes
// ONCE, and the series counts as ONE item — not 2 GB across 2 items.
func TestStorageAllocationDeduplicatesMultiEpisodeFiles(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	series, err := s.UpsertSeries(ctx, Series{TMDBID: 10, Title: "A Show", Year: 2019, RootFolderPath: "/tv"})
	if err != nil {
		t.Fatalf("seeding series: %v", err)
	}
	const oneGB = int64(1 << 30)
	const sharedPath = "/tv/A Show/A Show S01E01-E02.mkv"
	for _, epNum := range []int{1, 2} {
		if _, err := s.UpsertEpisode(ctx, Episode{
			SeriesID: series.ID, SeasonNumber: 1, EpisodeNumber: epNum,
			FilePath: sharedPath, Size: oneGB, QualityTier: "high",
		}); err != nil {
			t.Fatalf("seeding episode %d: %v", epNum, err)
		}
	}

	alloc, err := s.StorageAllocation(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := cellFor(t, alloc, "series", "high")
	if got.TotalBytes != oneGB {
		t.Errorf("expected the shared file's bytes counted once (%d), got %d", oneGB, got.TotalBytes)
	}
	if got.ItemCount != 1 {
		t.Errorf("expected 1 item (one series), got %d", got.ItemCount)
	}
}

// TestStorageAllocationDeduplicatesMultiEpisodeFilesWithDivergentRows is the
// case a SELECT DISTINCT-based dedup could not have caught. UpsertEpisode
// (singular) is a single-row writer — internal/dedup uses it — so two rows
// sharing a file_path can legitimately be momentarily divergent in size and
// quality_tier. DISTINCT would then emit two rows for one physical file and
// double-count it silently. GROUP BY file_path + MAX(...) needs no such
// invariant: it collapses them to one deterministic row regardless.
func TestStorageAllocationDeduplicatesMultiEpisodeFilesWithDivergentRows(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	series, err := s.UpsertSeries(ctx, Series{TMDBID: 10, Title: "A Show", Year: 2019, RootFolderPath: "/tv"})
	if err != nil {
		t.Fatalf("seeding series: %v", err)
	}
	const sharedPath = "/tv/A Show/A Show S01E01-E02.mkv"
	// Two DIFFERENT episode slots, one physical file, divergent size/tier.
	if _, err := s.UpsertEpisode(ctx, Episode{
		SeriesID: series.ID, SeasonNumber: 1, EpisodeNumber: 1,
		FilePath: sharedPath, Size: 1_000, QualityTier: "high",
	}); err != nil {
		t.Fatalf("seeding episode 1: %v", err)
	}
	if _, err := s.UpsertEpisode(ctx, Episode{
		SeriesID: series.ID, SeasonNumber: 1, EpisodeNumber: 2,
		FilePath: sharedPath, Size: 2_000, QualityTier: "medium",
	}); err != nil {
		t.Fatalf("seeding episode 2: %v", err)
	}

	alloc, err := s.StorageAllocation(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// MAX(size) = 2000 and MAX(quality_tier) = 'medium' ('m' > 'h'): one cell,
	// one item, deterministically — never one row in each of two tier cells.
	got := cellFor(t, alloc, "series", "medium")
	if got.TotalBytes != 2_000 || got.ItemCount != 1 {
		t.Errorf("series/medium: expected MAX(size)=2000 / 1 item, got %+v", got)
	}
	if other := cellFor(t, alloc, "series", "high"); other.TotalBytes != 0 || other.ItemCount != 0 {
		t.Errorf("series/high: the divergent row must not also land here, got %+v", other)
	}
	if total := alloc.Rows[1].RowTotalBytes; total != 2_000 {
		t.Errorf("expected the series row to total 2000 (one file, counted once), got %d", total)
	}
}

// TestStorageAllocationSeriesItemCountCountsSeriesNotFiles pins the count rule
// that makes the cell reconcile with its own drill-down: /api/tracked returns
// one row per SERIES, not per episode file, so a 10-episode series must read as
// 1 item — a cell saying 10 would link to a list showing 1.
func TestStorageAllocationSeriesItemCountCountsSeriesNotFiles(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	series, err := s.UpsertSeries(ctx, Series{TMDBID: 10, Title: "A Show", Year: 2019, RootFolderPath: "/tv"})
	if err != nil {
		t.Fatalf("seeding series: %v", err)
	}
	for ep := 1; ep <= 10; ep++ {
		if _, err := s.UpsertEpisode(ctx, Episode{
			SeriesID: series.ID, SeasonNumber: 1, EpisodeNumber: ep,
			FilePath: fmt.Sprintf("/tv/A Show/s01e%02d.mkv", ep),
			Size:     100, QualityTier: "high",
		}); err != nil {
			t.Fatalf("seeding episode %d: %v", ep, err)
		}
	}

	alloc, err := s.StorageAllocation(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := cellFor(t, alloc, "series", "high")
	if got.ItemCount != 1 {
		t.Errorf("expected itemCount 1 (one series), got %d", got.ItemCount)
	}
	// Bytes still sum per file — only the COUNT is per series.
	if got.TotalBytes != 1_000 {
		t.Errorf("expected 10 files x 100 bytes = 1000, got %d", got.TotalBytes)
	}
}

// TestStorageAllocationExcludesMissingEpisodes proves the non-empty-file_path
// filter: library_episodes deliberately holds rows for episodes TMDB knows
// about that are not on disk. Without the filter they'd show up as hundreds of
// zero-byte Unknown "items" in the Series row.
func TestStorageAllocationExcludesMissingEpisodes(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	series, err := s.UpsertSeries(ctx, Series{TMDBID: 10, Title: "A Show", Year: 2019, RootFolderPath: "/tv"})
	if err != nil {
		t.Fatalf("seeding series: %v", err)
	}
	missing := make([]Episode, 0, 200)
	for ep := 1; ep <= 200; ep++ {
		missing = append(missing, Episode{
			SeriesID: series.ID, SeasonNumber: 2, EpisodeNumber: ep, Title: "Not on disk",
		})
	}
	if _, err := s.UpsertEpisodes(ctx, missing); err != nil {
		t.Fatalf("seeding missing episodes: %v", err)
	}

	alloc, err := s.StorageAllocation(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, cell := range alloc.Rows[1].Cells {
		if cell.TotalBytes != 0 || cell.ItemCount != 0 {
			t.Errorf("missing-episode rows must be invisible, but series/%s reports %+v", cell.Tier, cell)
		}
	}
	if alloc.Rows[1].RowItemCount != 0 || alloc.GrandTotalBytes != 0 {
		t.Errorf("expected an entirely empty grid, got rowItemCount=%d grandTotal=%d", alloc.Rows[1].RowItemCount, alloc.GrandTotalBytes)
	}
}

// TestStorageAllocationFoldsEmptyTierIntoUnknown proves the empty-string tier
// -> "unknown" fold is DISPLAY-ONLY. The stored value must stay the
// empty-string sentinel so BackfillSizeAndTier's uncaptured-row guard stays
// idempotent — folding in the SQL's CASE rather than rewriting the row is what
// preserves the distinction between the two sentinels:
//
//	quality_tier = ''        -> never processed
//	quality_tier = 'unknown' -> processed, chain concluded Unknown
func TestStorageAllocationFoldsEmptyTierIntoUnknown(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	item, err := s.Upsert(ctx, Item{
		Mode: mode.Movies, TMDBID: 1, Title: "Unbackfilled", Year: 2020,
		FilePath: "/movies/Unbackfilled/a.mkv", RootFolderPath: "/movies",
		Size: 777, // QualityTier deliberately left "" — not yet backfilled.
	})
	if err != nil {
		t.Fatalf("seeding movie: %v", err)
	}

	alloc, err := s.StorageAllocation(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := cellFor(t, alloc, "movies", "unknown")
	if got.TotalBytes != 777 || got.ItemCount != 1 {
		t.Errorf("expected the '' -tier row folded into movies/unknown, got %+v", got)
	}

	// The stored value is untouched.
	stored, err := s.Get(ctx, item.ID)
	if err != nil {
		t.Fatalf("re-reading the item: %v", err)
	}
	if stored.QualityTier != "" {
		t.Errorf("the fold must be display-only: expected the stored quality_tier to stay \"\", got %q", stored.QualityTier)
	}
}
