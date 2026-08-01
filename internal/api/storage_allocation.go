package api

import (
	"encoding/json"
	"net/http"

	"github.com/labbersanon/sakms/internal/library"
)

// storageAllocationCell mirrors apidto.StorageAllocationCell — one (mode, tier)
// intersection of the Dashboard's Storage Allocation grid.
type storageAllocationCell struct {
	Tier       string `json:"tier"`
	TotalBytes int64  `json:"totalBytes"`
	ItemCount  int    `json:"itemCount"`
}

// storageAllocationRow mirrors apidto.StorageAllocationRow — one mode's full
// tier axis, Cells always len(alloc.Tiers).
type storageAllocationRow struct {
	Mode          string                  `json:"mode"`
	Cells         []storageAllocationCell `json:"cells"`
	RowTotalBytes int64                   `json:"rowTotalBytes"`
	RowItemCount  int                     `json:"rowItemCount"`
}

// storageAllocationResponse mirrors apidto.StorageAllocation.
type storageAllocationResponse struct {
	Rows            []storageAllocationRow `json:"rows"`
	Tiers           []string               `json:"tiers"`
	GrandTotalBytes int64                  `json:"grandTotalBytes"`
}

// storageAllocationHandler backs GET /api/admin/storage-allocation — the
// tracked library's size and item count split by mode and quality tier.
//
// It takes libStore and NOTHING ELSE — no *http.Client, no connections.Store,
// no settings.Store. That is deliberate and structural, not incidental: with no
// client and no connection store in scope, this handler CANNOT make an external
// call, and the whole grid comes from one grouped SQL statement over the
// size/quality_tier columns captured at write time (migration 0055). There is
// no os.Stat anywhere on this path and there must never be one — a Dashboard
// section that stats every tracked file on every page load is exactly the
// per-request disk I/O this endpoint's shape rules out. If a future change
// needs a number this query can't produce, capture it at write time; do not
// widen this signature.
//
// The handler is a thin JSON writer: densifying the grid (fixed 3 mode rows x
// 5 tier columns, missing combinations materialized as zero cells) happens in
// library.Store.StorageAllocation, so the grid invariant is owned and tested at
// the layer that produces it.
func storageAllocationHandler(libStore *library.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		alloc, err := libStore.StorageAllocation(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		out := storageAllocationResponse{
			Rows:            make([]storageAllocationRow, 0, len(alloc.Rows)),
			Tiers:           alloc.Tiers,
			GrandTotalBytes: alloc.GrandTotalBytes,
		}
		for _, row := range alloc.Rows {
			cells := make([]storageAllocationCell, 0, len(row.Cells))
			for _, cell := range row.Cells {
				cells = append(cells, storageAllocationCell{
					Tier: cell.Tier, TotalBytes: cell.TotalBytes, ItemCount: cell.ItemCount,
				})
			}
			out.Rows = append(out.Rows, storageAllocationRow{
				Mode:          row.Mode,
				Cells:         cells,
				RowTotalBytes: row.RowTotalBytes,
				RowItemCount:  row.RowItemCount,
			})
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(out)
	}
}
