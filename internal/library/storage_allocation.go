package library

import (
	"context"
	"fmt"
)

// StorageAllocationTiers is the fixed tier axis every StorageAllocationRow is
// densified against: the four real quality.Tier values plus the Unknown
// display bucket. Mirrors internal/api's discoverAvailabilityTiers plus
// "unknown". A stored quality_tier outside this set (there is no writer that
// produces one today) is simply not represented in the grid — the axis is
// fixed on purpose so the frontend never has to discover columns at runtime.
var StorageAllocationTiers = []string{"low", "medium", "high", "lossless", "unknown"}

// StorageAllocationModes is the fixed mode axis, in render order.
var StorageAllocationModes = []string{"movies", "series", "adult"}

// StorageAllocationCell is one (mode, tier) intersection of the grid. A cell
// with no matching rows is still emitted, with zero values.
type StorageAllocationCell struct {
	Tier       string
	TotalBytes int64
	ItemCount  int
}

// StorageAllocationRow is one mode's full tier axis. Cells is always
// len(StorageAllocationTiers), in that exact order.
//
// RowItemCount is the sum of the row's cell counts. For Series that can
// legitimately exceed the number of distinct series: a series whose episodes
// span two tiers is counted once in each of those two cells, which is what
// makes every individual cell's count reconcile with what clicking through to
// its drill-down actually lists. RowTotalBytes never double-counts, since a
// given file belongs to exactly one tier group.
type StorageAllocationRow struct {
	Mode          string
	Cells         []StorageAllocationCell
	RowTotalBytes int64
	RowItemCount  int
}

// StorageAllocation is the dense mode x tier grid backing the Dashboard's
// Storage Allocation table. Rows is always len(StorageAllocationModes) in that
// order, and Tiers is StorageAllocationTiers.
type StorageAllocation struct {
	Rows            []StorageAllocationRow
	Tiers           []string
	GrandTotalBytes int64
}

// storageAllocationQuery aggregates tracked bytes and item counts by mode and
// quality tier in ONE round trip with ZERO disk I/O — everything it reports is
// already in the DB (size/quality_tier were captured at write time by migration
// 0055's capture-forward sites, or by the boot-time backfill). Never add an
// os.Stat to this path; see storageAllocationHandler's doc comment.
//
// Three rules here are easy to get wrong and are load-bearing:
//
//  1. Series bytes are deduplicated per file_path via GROUP BY + MAX(...), NOT
//     SELECT DISTINCT. A single physical file can back several library_episodes
//     rows (a S01E01-E02 double episode, or a season pack broken into slots), so
//     a naive SUM(size) double-counts. DISTINCT would work only if two rows
//     sharing a file_path always shared size AND quality_tier — an invariant
//     that holds for UpsertEpisodes (the atomic multi-row writer) but NOT for
//     UpsertEpisode (the single-row writer internal/dedup uses), which can leave
//     two same-path rows momentarily divergent and would then emit two DISTINCT
//     rows for one file. GROUP BY file_path + MAX(...) needs no such invariant
//     and costs the same.
//
//  2. Series' item_count counts DISTINCT series_id, not distinct file_path.
//     /api/tracked returns one row per SERIES, not per episode file, so counting
//     files would print a cell count that doesn't match what clicking the cell
//     lists. Bytes still sum per file (rule 1); only the count is per series.
//
//  3. The empty-string tier is folded to the literal 'unknown' for DISPLAY
//     only. The two stored sentinels stay distinct: an EMPTY quality_tier
//     means "never processed", while the literal 'unknown' means "processed,
//     and the inference chain concluded Unknown". Keeping them apart is what
//     keeps BackfillSizeAndTier's own uncaptured-row guard idempotent (see
//     TierUnknown's doc for the full table). Never rewrite the stored value.
//
// library_items.mode = 'movies' is filtered explicitly rather than grouped:
// that table carries a legacy mode column from when Adult was expected to share
// it, but Adult has its own library_scenes table now. Rows with an empty
// file_path are excluded everywhere — for library_episodes those are the deliberate
// "TMDB knows about this episode, it isn't on disk" rows.
const storageAllocationQuery = `
SELECT 'movies' AS mode,
       CASE WHEN quality_tier = '' THEN 'unknown' ELSE quality_tier END AS tier,
       COALESCE(SUM(size), 0) AS total_bytes,
       COUNT(*)               AS item_count
  FROM library_items
 WHERE mode = 'movies' AND file_path != ''
 GROUP BY tier
UNION ALL
SELECT 'series',
       tier,
       COALESCE(SUM(file_bytes), 0),
       COUNT(DISTINCT series_id)
  FROM (SELECT series_id, file_path,
               MAX(size) AS file_bytes,
               CASE WHEN MAX(quality_tier) = '' THEN 'unknown' ELSE MAX(quality_tier) END AS tier
          FROM library_episodes
         WHERE file_path != ''
         GROUP BY series_id, file_path) AS episode_files
 GROUP BY tier
UNION ALL
SELECT 'adult',
       CASE WHEN quality_tier = '' THEN 'unknown' ELSE quality_tier END,
       COALESCE(SUM(size), 0),
       COUNT(*)
  FROM library_scenes
 WHERE file_path != ''
 GROUP BY 2`

// StorageAllocation returns the tracked library's byte totals and item counts
// split by mode and quality tier, as a DENSE grid: every (mode, tier)
// combination is present, missing ones as zero cells, so no caller has to
// invent them. Densification happens here rather than in the handler so the
// grid invariant is testable at the layer that owns it.
func (s *Store) StorageAllocation(ctx context.Context) (StorageAllocation, error) {
	rows, err := s.db.QueryContext(ctx, storageAllocationQuery)
	if err != nil {
		return StorageAllocation{}, fmt.Errorf("querying storage allocation: %w", err)
	}
	defer rows.Close()

	// sparse[mode][tier] holds only the combinations the DB actually returned.
	sparse := make(map[string]map[string]StorageAllocationCell, len(StorageAllocationModes))
	for rows.Next() {
		var m, tier string
		var totalBytes int64
		var itemCount int
		if err := rows.Scan(&m, &tier, &totalBytes, &itemCount); err != nil {
			return StorageAllocation{}, fmt.Errorf("scanning storage allocation row: %w", err)
		}
		if sparse[m] == nil {
			sparse[m] = make(map[string]StorageAllocationCell, len(StorageAllocationTiers))
		}
		sparse[m][tier] = StorageAllocationCell{Tier: tier, TotalBytes: totalBytes, ItemCount: itemCount}
	}
	if err := rows.Err(); err != nil {
		return StorageAllocation{}, fmt.Errorf("iterating storage allocation rows: %w", err)
	}

	out := StorageAllocation{
		Rows:  make([]StorageAllocationRow, 0, len(StorageAllocationModes)),
		Tiers: StorageAllocationTiers,
	}
	for _, m := range StorageAllocationModes {
		row := StorageAllocationRow{Mode: m, Cells: make([]StorageAllocationCell, 0, len(StorageAllocationTiers))}
		for _, tier := range StorageAllocationTiers {
			cell, ok := sparse[m][tier]
			if !ok {
				cell = StorageAllocationCell{Tier: tier}
			}
			row.Cells = append(row.Cells, cell)
			row.RowTotalBytes += cell.TotalBytes
			row.RowItemCount += cell.ItemCount
		}
		out.GrandTotalBytes += row.RowTotalBytes
		out.Rows = append(out.Rows, row)
	}
	return out, nil
}
