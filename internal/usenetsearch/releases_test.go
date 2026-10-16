package usenetsearch

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/labbersanon/sakms/internal/dbtest"
)

// TestPruneOldest_OldestPostedFirst proves backfilled-style rows (old
// posted_at, ingested LAST) are evicted before recent forward rows.
func TestPruneOldest_OldestPostedFirst(t *testing.T) {
	sqlDB := dbtest.New(t)
	idx, err := OpenIndex(sqlDB)
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()
	ctx := context.Background()
	const g = "alt.binaries.movies"

	var rows []HeaderRow
	for i := 0; i < 50; i++ {
		rows = append(rows, HeaderRow{
			Group: g, MsgNum: 1000 + i, MsgID: fmt.Sprintf("new%d@t", i), Subject: "s",
			ReleaseName: "Recent.Rel.2026", PartN: i + 1, PartM: 50, Filename: "r.mkv", Bytes: 1, PostedAt: 1790000000 + int64(i),
		})
	}
	if err := idx.Upsert(ctx, rows); err != nil {
		t.Fatal(err)
	}
	time.Sleep(1100 * time.Millisecond) // later ingested_at second
	rows = rows[:0]
	for i := 0; i < 50; i++ {
		rows = append(rows, HeaderRow{
			Group: g, MsgNum: 10 + i, MsgID: fmt.Sprintf("old%d@t", i), Subject: "s",
			ReleaseName: "Backfilled.Rel.2020", PartN: i + 1, PartM: 50, Filename: "b.mkv", Bytes: 1, PostedAt: 1600000000 + int64(i),
		})
	}
	if err := idx.Upsert(ctx, rows); err != nil {
		t.Fatal(err)
	}
	// 100 rows x 200 B = 20_000; budget 10_000 keeps 50 — all of the recent ones.
	if err := idx.PruneOldest(ctx, 10_000); err != nil {
		t.Fatal(err)
	}
	var recent, old int
	if err := sqlDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM usenet_nntp_headers WHERE release_name='Recent.Rel.2026'`).Scan(&recent); err != nil {
		t.Fatal(err)
	}
	if err := sqlDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM usenet_nntp_headers WHERE release_name='Backfilled.Rel.2020'`).Scan(&old); err != nil {
		t.Fatal(err)
	}
	if recent != 50 || old != 0 {
		t.Fatalf("recent=%d old=%d, want 50/0 (oldest posted_at evicted first)", recent, old)
	}
	// The pruned release is marked dirty and drops as an orphan.
	st, err := idx.RecomputeDirty(ctx, 10)
	if err != nil || st.Orphans != 1 {
		t.Fatalf("stats=%+v err=%v, want 1 orphan", st, err)
	}
}

func TestSweepIncomplete_RequiresAllThreeConditions(t *testing.T) {
	sqlDB := dbtest.New(t)
	idx, err := OpenIndex(sqlDB)
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()
	ctx := context.Background()
	const g = "alt.binaries.movies"

	if err := idx.Upsert(ctx, fileRows(g, "Stale.Partial.2020", "s.mkv", "s", 100, 100, 50, 0)); err != nil {
		t.Fatal(err)
	}
	if _, err := idx.RecomputeDirty(ctx, 10); err != nil {
		t.Fatal(err)
	}
	ttl := 72 * time.Hour
	age := func(secs int64) {
		if _, err := sqlDB.ExecContext(ctx, `UPDATE usenet_nntp_releases SET ingested_at=? WHERE release_key='Stale.Partial.2020'`, time.Now().Unix()-secs); err != nil {
			t.Fatal(err)
		}
	}
	setHigh := func(high int) {
		if _, err := sqlDB.ExecContext(ctx, `
INSERT INTO usenet_nntp_watermarks (group_name, high) VALUES (?, ?)
ON CONFLICT (group_name) DO UPDATE SET high=EXCLUDED.high`, g, high); err != nil {
			t.Fatal(err)
		}
	}
	sweep := func() int {
		t.Helper()
		n, err := idx.SweepIncomplete(ctx, ttl)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}

	age(int64(ttl/time.Second) + 60)
	setHigh(100 + 50 + 49_000) // watermark NOT past last_msg(150)+50k
	if n := sweep(); n != 0 {
		t.Fatalf("watermark not past margin: swept %d, want 0", n)
	}
	setHigh(150 + 50_001)
	if _, err := sqlDB.ExecContext(ctx, `INSERT INTO usenet_nntp_missed_ranges (group_name, lo, hi, reason) VALUES (?, 140, 160, 'span')`, g); err != nil {
		t.Fatal(err)
	}
	if n := sweep(); n != 0 {
		t.Fatalf("queued repair range: swept %d, want 0", n)
	}
	if _, err := sqlDB.ExecContext(ctx, `DELETE FROM usenet_nntp_missed_ranges`); err != nil {
		t.Fatal(err)
	}
	age(60) // too young
	if n := sweep(); n != 0 {
		t.Fatalf("within ttl: swept %d, want 0", n)
	}
	age(int64(ttl/time.Second) + 60)
	if n := sweep(); n != 1 {
		t.Fatalf("all conditions met: swept %d, want 1", n)
	}
	var hdr, rel int
	_ = sqlDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM usenet_nntp_headers WHERE group_name=?`, g).Scan(&hdr)
	_ = sqlDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM usenet_nntp_releases WHERE group_name=?`, g).Scan(&rel)
	if hdr != 0 || rel != 0 {
		t.Fatalf("headers=%d releases=%d, want 0/0", hdr, rel)
	}
}

func TestSweepIncomplete_KeepsSearchable(t *testing.T) {
	sqlDB := dbtest.New(t)
	idx, err := OpenIndex(sqlDB)
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()
	ctx := context.Background()
	const g = "alt.binaries.movies"
	if err := idx.Upsert(ctx, fileRows(g, "Whole.Rel.2020", "w.mkv", "w", 100, 10, 10, 0)); err != nil {
		t.Fatal(err)
	}
	if _, err := idx.RecomputeDirty(ctx, 10); err != nil {
		t.Fatal(err)
	}
	if _, err := sqlDB.ExecContext(ctx, `UPDATE usenet_nntp_releases SET ingested_at=1`); err != nil {
		t.Fatal(err)
	}
	if _, err := sqlDB.ExecContext(ctx, `INSERT INTO usenet_nntp_watermarks (group_name, high) VALUES (?, 999999)`, g); err != nil {
		t.Fatal(err)
	}
	if n, err := idx.SweepIncomplete(ctx, time.Hour); err != nil || n != 0 {
		t.Fatalf("swept %d err=%v, want 0 (searchable release is never reaped)", n, err)
	}
}
