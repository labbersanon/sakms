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
	setHigh := func(high int) {
		if _, err := sqlDB.ExecContext(ctx, `
INSERT INTO usenet_nntp_watermarks (group_name, high) VALUES (?, ?)
ON CONFLICT (group_name) DO UPDATE SET high=EXCLUDED.high`, g, high); err != nil {
			t.Fatal(err)
		}
	}
	sweep := func() int {
		t.Helper()
		n, err := idx.SweepIncomplete(ctx, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}

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
	if n := sweep(); n != 1 {
		t.Fatalf("settled incomplete: swept %d, want 1", n)
	}
	var hdr, rel, skips int
	_ = sqlDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM usenet_nntp_headers WHERE group_name=?`, g).Scan(&hdr)
	_ = sqlDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM usenet_nntp_releases WHERE group_name=?`, g).Scan(&rel)
	_ = sqlDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM usenet_nntp_skips WHERE group_name=? AND release_key='Stale.Partial.2020'`, g).Scan(&skips)
	if hdr != 0 || rel != 0 || skips != 1 {
		t.Fatalf("headers=%d releases=%d skips=%d, want 0/0/1", hdr, rel, skips)
	}
	replay := fileRows(g, "Stale.Partial.2020", "s.mkv", "s", 200, 100, 50, 0)
	kept, dropped, err := idx.DropSkipped(ctx, g, replay)
	if err != nil {
		t.Fatal(err)
	}
	if dropped != len(replay) || len(kept) != 0 {
		t.Fatalf("DropSkipped kept=%d dropped=%d, want 0/%d", len(kept), dropped, len(replay))
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

func TestSweepIncomplete_KeepsNearCompleteAndInWindow(t *testing.T) {
	sqlDB := dbtest.New(t)
	idx, err := OpenIndex(sqlDB)
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()
	ctx := context.Background()
	const g = "alt.binaries.movies"
	// 80/100 = 8000 bp: keep-headers floor, not the 95% searchable gate.
	if err := idx.Upsert(ctx, fileRows(g, "Almost.Rel.2020", "a.mkv", "a", 100, 100, 80, 0)); err != nil {
		t.Fatal(err)
	}
	if err := idx.Upsert(ctx, fileRows(g, "Window.Rel.2020", "w.mkv", "w", 1000, 100, 10, 0)); err != nil {
		t.Fatal(err)
	}
	if _, err := idx.RecomputeDirty(ctx, 10); err != nil {
		t.Fatal(err)
	}
	var lastAlmost, lastWindow int
	if err := sqlDB.QueryRowContext(ctx, `SELECT last_msg FROM usenet_nntp_releases WHERE release_key='Almost.Rel.2020'`).Scan(&lastAlmost); err != nil {
		t.Fatal(err)
	}
	if err := sqlDB.QueryRowContext(ctx, `SELECT last_msg FROM usenet_nntp_releases WHERE release_key='Window.Rel.2020'`).Scan(&lastWindow); err != nil {
		t.Fatal(err)
	}
	// HIGH is past Almost's 50k margin (Almost is ≥80%, so still kept) and
	// inside Window's margin (10% incomplete, still eligible to complete).
	high := lastWindow + incompleteForwardMargin - 1
	if high <= lastAlmost+incompleteForwardMargin {
		t.Fatalf("fixture HIGH=%d does not isolate the two keep reasons", high)
	}
	if _, err := sqlDB.ExecContext(ctx, `INSERT INTO usenet_nntp_watermarks (group_name, high) VALUES (?, ?)`, g, high); err != nil {
		t.Fatal(err)
	}
	if n, err := idx.SweepIncomplete(ctx, time.Hour); err != nil || n != 0 {
		t.Fatalf("swept %d err=%v, want 0 (≥80%% and in-window stay)", n, err)
	}
}

func TestSweepIncomplete_PendingNameSettled(t *testing.T) {
	sqlDB := dbtest.New(t)
	idx, err := OpenIndex(sqlDB)
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()
	ctx := context.Background()
	const g = "alt.binaries.movies"
	const key = "obf:0123456789abcdef"
	if err := idx.Upsert(ctx, fileRows(g, key, "a.rar", "h", 100, 100, 10, 0)); err != nil {
		t.Fatal(err)
	}
	if _, err := idx.RecomputeDirty(ctx, 10); err != nil {
		t.Fatal(err)
	}
	var last int
	if err := sqlDB.QueryRowContext(ctx, `SELECT last_msg FROM usenet_nntp_releases WHERE release_key=?`, key).Scan(&last); err != nil {
		t.Fatal(err)
	}
	if _, err := sqlDB.ExecContext(ctx, `
INSERT INTO usenet_nntp_watermarks (group_name, high) VALUES (?, ?)`, g, last+incompleteForwardMargin+1); err != nil {
		t.Fatal(err)
	}
	n, err := idx.SweepIncomplete(ctx, time.Hour)
	if err != nil || n != 1 {
		t.Fatalf("swept %d err=%v, want 1 settled pending_name", n, err)
	}
	var skips int
	_ = sqlDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM usenet_nntp_skips WHERE release_key=? AND reason='incomplete'`, key).Scan(&skips)
	if skips != 1 {
		t.Fatalf("skips=%d, want 1 incomplete stub", skips)
	}
}

func TestRecomputeOne_YencCountedOncePerFile(t *testing.T) {
	sqlDB := dbtest.New(t)
	idx, err := OpenIndex(sqlDB)
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()
	ctx := context.Background()
	const g = "alt.binaries.movies"
	const key = "obf:0123456789abcdef"
	var rows []HeaderRow
	for i := 1; i <= 10; i++ {
		rows = append(rows, HeaderRow{
			Group: g, MsgNum: i, MsgID: fmt.Sprintf("y%d@t", i), Subject: "s", From: "p@t",
			PostedAt: 1700000000, Bytes: 1000, ReleaseName: key, PartN: i, PartM: 10,
			Filename: "a.rar", YencSize: 50_000_000, Obfuscated: true,
		})
	}
	if err := idx.Upsert(ctx, rows); err != nil {
		t.Fatal(err)
	}
	if _, err := idx.RecomputeDirty(ctx, 10); err != nil {
		t.Fatal(err)
	}
	var payload int64
	if err := sqlDB.QueryRowContext(ctx, `SELECT payload_bytes FROM usenet_nntp_releases WHERE release_key=?`, key).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	if payload != 50_000_000 {
		t.Fatalf("payload=%d, want 50000000 (once per file, not 10× yEnc)", payload)
	}
}

func TestRecomputeDirty_PendingCompleteFirst(t *testing.T) {
	sqlDB := dbtest.New(t)
	idx, err := OpenIndex(sqlDB)
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()
	ctx := context.Background()
	const g = "alt.binaries.movies"
	// Among pending, complete still beats incomplete even when newer.
	releases := []struct {
		key            string
		completenessBP int
		updatedAt      int64
		wantDirty      bool
	}{
		{"obf:aaaaaaaaaaaaaaaa", 0, 1, true},
		{"obf:bbbbbbbbbbbbbbbb", 9500, 99, false},
	}
	inc := fileRows(g, releases[0].key, "a.rar", "a", 100, 10, 1, 0)
	comp := fileRows(g, releases[1].key, "b.rar", "b", 200, 10, 10, 0)
	if err := idx.Upsert(ctx, append(inc, comp...)); err != nil {
		t.Fatal(err)
	}
	for _, r := range releases {
		if _, err := sqlDB.ExecContext(ctx, `UPDATE usenet_nntp_releases SET dirty=TRUE, completeness_bp=?, updated_at=? WHERE release_key=?`,
			r.completenessBP, r.updatedAt, r.key); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := idx.RecomputeDirty(ctx, 1); err != nil {
		t.Fatal(err)
	}
	for _, r := range releases {
		var dirty bool
		if err := sqlDB.QueryRowContext(ctx, `SELECT dirty FROM usenet_nntp_releases WHERE release_key=?`, r.key).Scan(&dirty); err != nil {
			t.Fatal(err)
		}
		if dirty != r.wantDirty {
			t.Errorf("%s dirty=%v, want %v (complete pending_name before incomplete pending)", r.key, dirty, r.wantDirty)
		}
	}
}

func TestRecomputeDirty_NamedCompleteBeforePending(t *testing.T) {
	sqlDB := dbtest.New(t)
	idx, err := OpenIndex(sqlDB)
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()
	ctx := context.Background()
	const g = "alt.binaries.movies"
	named := fileRows(g, "Named.Show.2020.1080p", "n.mkv", "n", 0, 10, 10, 0)
	pending := fileRows(g, "obf:aaaaaaaaaaaaaaaa", "a.rar", "a", 100, 10, 1, 0)
	if err := idx.Upsert(ctx, append(named, pending...)); err != nil {
		t.Fatal(err)
	}
	if _, err := sqlDB.ExecContext(ctx, `UPDATE usenet_nntp_releases SET dirty=TRUE, completeness_bp=9500, updated_at=99 WHERE release_key=?`, "Named.Show.2020.1080p"); err != nil {
		t.Fatal(err)
	}
	if _, err := sqlDB.ExecContext(ctx, `UPDATE usenet_nntp_releases SET dirty=TRUE, completeness_bp=0, updated_at=1 WHERE release_key=?`, "obf:aaaaaaaaaaaaaaaa"); err != nil {
		t.Fatal(err)
	}
	if _, err := idx.RecomputeDirty(ctx, 1); err != nil {
		t.Fatal(err)
	}
	var namedDirty, pendingDirty, searchable bool
	if err := sqlDB.QueryRowContext(ctx, `SELECT dirty, searchable FROM usenet_nntp_releases WHERE release_key=?`, "Named.Show.2020.1080p").Scan(&namedDirty, &searchable); err != nil {
		t.Fatal(err)
	}
	if err := sqlDB.QueryRowContext(ctx, `SELECT dirty FROM usenet_nntp_releases WHERE release_key=?`, "obf:aaaaaaaaaaaaaaaa").Scan(&pendingDirty); err != nil {
		t.Fatal(err)
	}
	if namedDirty || !searchable || !pendingDirty {
		t.Fatalf("named dirty=%v searchable=%v pending dirty=%v; want named recomputed searchable first", namedDirty, searchable, pendingDirty)
	}
}

func TestRecomputeDirty_NamedZeroBeforePending(t *testing.T) {
	sqlDB := dbtest.New(t)
	idx, err := OpenIndex(sqlDB)
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()
	ctx := context.Background()
	const g = "alt.binaries.movies"
	named := fileRows(g, "Named.Show.2020.1080p", "n.mkv", "nz", 0, 10, 10, 0)
	pending := fileRows(g, "obf:bbbbbbbbbbbbbbbb", "b.rar", "b", 100, 10, 1, 0)
	if err := idx.Upsert(ctx, append(named, pending...)); err != nil {
		t.Fatal(err)
	}
	if _, err := sqlDB.ExecContext(ctx, `UPDATE usenet_nntp_releases SET dirty=TRUE, completeness_bp=0, declared_segs=0, updated_at=99 WHERE release_key=?`, "Named.Show.2020.1080p"); err != nil {
		t.Fatal(err)
	}
	if _, err := sqlDB.ExecContext(ctx, `UPDATE usenet_nntp_releases SET dirty=TRUE, completeness_bp=0, updated_at=1 WHERE release_key=?`, "obf:bbbbbbbbbbbbbbbb"); err != nil {
		t.Fatal(err)
	}
	if _, err := idx.RecomputeDirty(ctx, 1); err != nil {
		t.Fatal(err)
	}
	var namedDirty, pendingDirty bool
	if err := sqlDB.QueryRowContext(ctx, `SELECT dirty FROM usenet_nntp_releases WHERE release_key=?`, "Named.Show.2020.1080p").Scan(&namedDirty); err != nil {
		t.Fatal(err)
	}
	if err := sqlDB.QueryRowContext(ctx, `SELECT dirty FROM usenet_nntp_releases WHERE release_key=?`, "obf:bbbbbbbbbbbbbbbb").Scan(&pendingDirty); err != nil {
		t.Fatal(err)
	}
	if namedDirty || !pendingDirty {
		t.Fatalf("named dirty=%v pending dirty=%v; want 0%% named recomputed first", namedDirty, pendingDirty)
	}
}

func TestRecomputeDirty_JunkMegaBucketNotSearchable(t *testing.T) {
	sqlDB := dbtest.New(t)
	idx, err := OpenIndex(sqlDB)
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()
	ctx := context.Background()
	const g = "alt.binaries.movies"
	if err := idx.Upsert(ctx, fileRows(g, "(Els1212)", "e.jpg", "e", 0, 10, 10, 0)); err != nil {
		t.Fatal(err)
	}
	if _, err := idx.RecomputeDirty(ctx, 10); err != nil {
		t.Fatal(err)
	}
	var searchable bool
	var bp int
	if err := sqlDB.QueryRowContext(ctx, `SELECT searchable, completeness_bp FROM usenet_nntp_releases WHERE release_key=?`, "(Els1212)").Scan(&searchable, &bp); err != nil {
		t.Fatal(err)
	}
	if searchable || bp < 9500 {
		t.Fatalf("searchable=%v bp=%d; 100%% junk mega-bucket must not be Searchable", searchable, bp)
	}
}

func TestRecomputeDirty_IdentQuotedKeyIgnoresCollectionFileM(t *testing.T) {
	sqlDB := dbtest.New(t)
	idx, err := OpenIndex(sqlDB)
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()
	ctx := context.Background()
	const g = "alt.binaries.multimedia.anime.highspeed"
	const title = "WOR-HAZ Chivalry of a Failed Knight S01E09 BD HEVC 1080p FLAC"
	// One episode at 90/100 parts. [9/12] fileM=12 would declare ~12×100
	// and miss the 80% ident searchable gate (live completeness ≈4.5%).
	if err := idx.Upsert(ctx, fileRows(g, title, title+".mkv", "ch", 0, 100, 90, 12)); err != nil {
		t.Fatal(err)
	}
	if _, err := idx.RecomputeDirty(ctx, 10); err != nil {
		t.Fatal(err)
	}
	var searchable bool
	var bp, declared int
	if err := sqlDB.QueryRowContext(ctx, `SELECT searchable, completeness_bp, declared_segs FROM usenet_nntp_releases WHERE release_key=?`, title).Scan(&searchable, &bp, &declared); err != nil {
		t.Fatal(err)
	}
	if !searchable || declared != 100 || bp < 8000 {
		t.Fatalf("searchable=%v declared=%d bp=%d; want ident episode 90/100 searchable, not 12×100 pack", searchable, declared, bp)
	}
}

func TestRecomputeDirty_StopsAtDeadline(t *testing.T) {
	sqlDB := dbtest.New(t)
	idx, err := OpenIndex(sqlDB)
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()
	ctx := context.Background()
	const g = "alt.binaries.movies"
	if err := idx.Upsert(ctx, append(
		fileRows(g, "A.Show.2020", "a.mkv", "a", 0, 10, 1, 0),
		fileRows(g, "B.Show.2020", "b.mkv", "b", 100, 10, 1, 0)...,
	)); err != nil {
		t.Fatal(err)
	}
	st, err := idx.recomputeDirtyBounded(ctx, 10, time.Now().Add(-time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if st.Releases != 0 {
		t.Fatalf("releases=%d, want 0 (deadline already passed)", st.Releases)
	}
	if st.Backlog != 2 {
		t.Fatalf("backlog=%d, want 2 still dirty", st.Backlog)
	}
}

func TestUpsert_RestageDoesNotDirty(t *testing.T) {
	sqlDB := dbtest.New(t)
	idx, err := OpenIndex(sqlDB)
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()
	ctx := context.Background()
	const g = "alt.binaries.movies"
	rows := fileRows(g, "obf:aaaaaaaaaaaaaaaa", "a.rar", "a", 0, 10, 10, 0)
	if err := idx.Upsert(ctx, rows); err != nil {
		t.Fatal(err)
	}
	if _, err := idx.RecomputeDirty(ctx, 10); err != nil {
		t.Fatal(err)
	}
	var dirty bool
	if err := sqlDB.QueryRowContext(ctx, `SELECT dirty FROM usenet_nntp_releases`).Scan(&dirty); err != nil {
		t.Fatal(err)
	}
	if dirty {
		t.Fatal("want clean after recompute")
	}
	if err := idx.Upsert(ctx, rows); err != nil {
		t.Fatal(err)
	}
	if err := sqlDB.QueryRowContext(ctx, `SELECT dirty FROM usenet_nntp_releases`).Scan(&dirty); err != nil {
		t.Fatal(err)
	}
	if dirty {
		t.Fatal("identical restage must not re-dirty")
	}
	extra := fileRows(g, "obf:aaaaaaaaaaaaaaaa", "a.rar", "b", 10, 10, 1, 0)
	extra[0].PartN = 11
	extra[0].PartM = 10
	if err := idx.Upsert(ctx, extra); err != nil {
		t.Fatal(err)
	}
	if err := sqlDB.QueryRowContext(ctx, `SELECT dirty FROM usenet_nntp_releases`).Scan(&dirty); err != nil {
		t.Fatal(err)
	}
	if !dirty {
		t.Fatal("new msgid must dirty")
	}
}
