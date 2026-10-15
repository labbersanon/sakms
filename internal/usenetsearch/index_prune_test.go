package usenetsearch

import (
	"context"
	"fmt"
	"testing"

	"github.com/labbersanon/sakms/internal/dbtest"
)

// TestPruneOldest_StopsAtBudgetFloor proves soft size prune keeps recent rows
// instead of wiping the table once over budget (the pg_total_relation_size bug).
func TestPruneOldest_StopsAtBudgetFloor(t *testing.T) {
	sqlDB := dbtest.New(t)
	idx, err := OpenIndex(sqlDB)
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()

	ctx := context.Background()
	const n = 100
	rows := make([]HeaderRow, 0, n)
	for i := 0; i < n; i++ {
		rows = append(rows, HeaderRow{
			Group:       "alt.binaries.movies",
			MsgNum:      i + 1,
			MsgID:       fmt.Sprintf("msg-%d@test", i+1),
			Subject:     fmt.Sprintf("Title.S01E01.mkv yEnc (%d/%d)", i+1, n),
			ReleaseName: "Title.S01E01",
			PartN:       i + 1,
			PartM:       n,
			Filename:    "Title.S01E01.mkv",
			Bytes:       1000,
			PostedAt:    1700000000 + int64(i),
		})
	}
	if err := idx.Upsert(ctx, rows); err != nil {
		t.Fatal(err)
	}

	before, err := idx.CountHeaders(ctx)
	if err != nil || before != n {
		t.Fatalf("count before=%d err=%v", before, err)
	}
	// 100 rows × 200 B = 20_000. Target 10_000 → keep ~50 rows.
	if err := idx.PruneOldest(ctx, 10_000); err != nil {
		t.Fatal(err)
	}
	after, err := idx.CountHeaders(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if after == 0 {
		t.Fatal("PruneOldest wiped the entire index")
	}
	if after > 50 {
		t.Fatalf("after=%d, want ≤50 (budget 10KiB at 200 B/row)", after)
	}
	size, err := idx.ApproxBytes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if size > 10_000 {
		t.Fatalf("ApproxBytes=%d, want ≤10000", size)
	}
}

func TestPruneOldest_NoOpWhenUnderBudget(t *testing.T) {
	sqlDB := dbtest.New(t)
	idx, err := OpenIndex(sqlDB)
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()
	ctx := context.Background()
	if err := idx.Upsert(ctx, []HeaderRow{{
		Group: "g", MsgNum: 1, MsgID: "a@x", Subject: "x", ReleaseName: "x",
		PartN: 1, PartM: 1, Filename: "x.mkv", Bytes: 1, PostedAt: 1,
	}}); err != nil {
		t.Fatal(err)
	}
	if err := idx.PruneOldest(ctx, 1_000_000); err != nil {
		t.Fatal(err)
	}
	n, err := idx.CountHeaders(ctx)
	if err != nil || n != 1 {
		t.Fatalf("count=%d err=%v", n, err)
	}
}
