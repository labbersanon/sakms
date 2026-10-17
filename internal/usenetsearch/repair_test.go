package usenetsearch

import (
	"context"
	"fmt"
	"testing"

	"github.com/labbersanon/sakms/internal/dbtest"
	"github.com/labbersanon/sakms/internal/quality"
	"github.com/labbersanon/sakms/internal/usenet"
)

// gapStub is a HeaderSource whose Overview omits article numbers until healed.
type gapStub struct {
	crawlStub
	omit      map[int]bool
	busy      bool
	overCalls int
}

func (g *gapStub) Overview(ctx context.Context, grp string, from, to int) ([]usenet.MessageOverview, error) {
	g.overCalls++
	if g.busy {
		return nil, usenet.ErrBusy
	}
	rows, _ := g.crawlStub.Overview(ctx, grp, from, to)
	out := rows[:0:0]
	for _, r := range rows {
		if !g.omit[r.Number] {
			out = append(out, r)
		}
	}
	return out, nil
}

func (g *gapStub) heal() { g.omit = map[int]bool{} }

func dueNow(t *testing.T, idx *Index) {
	t.Helper()
	if _, err := idx.db.Exec(`UPDATE usenet_nntp_missed_ranges SET next_try_at=0`); err != nil {
		t.Fatal(err)
	}
}

func rangeCount(t *testing.T, idx *Index) int {
	t.Helper()
	var n int
	if err := idx.db.QueryRow(`SELECT COUNT(*) FROM usenet_nntp_missed_ranges`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func newGapFixture(t *testing.T, parts int, omit ...int) (*Index, *gapStub, *Crawler, string) {
	t.Helper()
	sqlDB := dbtest.New(t)
	idx, err := OpenIndex(sqlDB)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { idx.Close() })
	const group = "alt.binaries.movies"
	name := "Gap.Movie.2020.1080p.WEB-DL.x264-GROUP"
	rows := make([]usenet.MessageOverview, 0, parts)
	for i := 1; i <= parts; i++ {
		rows = append(rows, usenet.MessageOverview{
			Number: i, Subject: fmt.Sprintf(`%s - "%s.mkv" yEnc (%d/%d)`, name, name, i, parts),
			From: "p@t", MessageID: fmt.Sprintf("gap%d@t", i), Bytes: 1000, DateUnix: 1700000000,
		})
	}
	om := map[int]bool{}
	for _, n := range omit {
		om[n] = true
	}
	src := &gapStub{crawlStub: crawlStub{gr: usenet.GroupRange{Low: 1, High: parts}, rows: rows}, omit: om}
	c := &Crawler{Src: src, Index: idx, Groups: []string{group}, RepairEnabled: true}
	idx.SetSpanRepair(true)
	return idx, src, c, group
}

func TestRepair_GapRecordedThenRecoveredBecomesSearchable(t *testing.T) {
	idx, src, c, group := newGapFixture(t, 10, 4, 5, 6)
	ctx := context.Background()
	if _, err := c.CrawlOnce(ctx); err != nil {
		t.Fatal(err)
	}
	wm, _ := idx.Watermark(ctx, group)
	if wm != 10 {
		t.Fatalf("watermark=%d want 10 (gap never holds the forward watermark)", wm)
	}
	if n := rangeCount(t, idx); n != 1 {
		t.Fatalf("ranges=%d want 1 (4-6 compressed)", n)
	}
	var lo, hi int
	if err := idx.db.QueryRow(`SELECT lo, hi FROM usenet_nntp_missed_ranges`).Scan(&lo, &hi); err != nil || lo != 4 || hi != 6 {
		t.Fatalf("range=%d-%d err=%v", lo, hi, err)
	}
	if _, err := idx.RecomputeDirty(ctx, 10); err != nil {
		t.Fatal(err)
	}
	cs, _ := idx.SearchReleases(ctx, Query{Groups: []string{group}, Terms: []string{"Gap"}, Limit: 5})
	if len(cs) != 0 {
		t.Fatalf("70%% release searchable: %+v", cs)
	}

	src.heal()
	dueNow(t, idx)
	rr, err := c.RepairOnce(ctx)
	if err != nil || rr.Busy || rr.Ranges != 1 || rr.RecoveredRows != 3 {
		t.Fatalf("repair=%+v err=%v", rr, err)
	}
	if n := rangeCount(t, idx); n != 0 {
		t.Fatalf("ranges=%d want 0 after recovery", n)
	}
	wm, _ = idx.Watermark(ctx, group)
	if wm != 10 {
		t.Fatalf("watermark=%d want unchanged 10", wm)
	}
	if _, err := idx.RecomputeDirty(ctx, 10); err != nil {
		t.Fatal(err)
	}
	cs, _ = idx.SearchReleases(ctx, Query{Groups: []string{group}, Terms: []string{"Gap"}, Limit: 5})
	if len(cs) != 1 || cs[0].SegmentCount != 10 {
		t.Fatalf("recovered release not searchable: %+v", cs)
	}
}

func TestRepair_RangeDeletedAfterThreeFailedAttempts(t *testing.T) {
	idx, _, c, _ := newGapFixture(t, 10, 4, 5, 6) // never healed
	ctx := context.Background()
	if _, err := c.CrawlOnce(ctx); err != nil {
		t.Fatal(err)
	}
	for attempt := 1; attempt <= 3; attempt++ {
		dueNow(t, idx)
		rr, err := c.RepairOnce(ctx)
		if err != nil || rr.Ranges != 1 {
			t.Fatalf("attempt %d: %+v err=%v", attempt, rr, err)
		}
		if attempt < 3 {
			if n := rangeCount(t, idx); n != 1 {
				t.Fatalf("attempt %d: ranges=%d want 1", attempt, n)
			}
			var attempts int
			var next int64
			if err := idx.db.QueryRow(`SELECT attempts, next_try_at FROM usenet_nntp_missed_ranges`).Scan(&attempts, &next); err != nil || attempts != attempt {
				t.Fatalf("attempt %d: attempts=%d err=%v", attempt, attempts, err)
			}
		} else if rr.Exhausted != 1 {
			t.Fatalf("exhausted=%d want 1", rr.Exhausted)
		}
	}
	if n := rangeCount(t, idx); n != 0 {
		t.Fatalf("ranges=%d want 0 after 3 failures", n)
	}
}

func TestRepair_BusyReturnsBusyWithoutChargingAttempts(t *testing.T) {
	idx, src, c, _ := newGapFixture(t, 10, 4, 5, 6)
	ctx := context.Background()
	if _, err := c.CrawlOnce(ctx); err != nil {
		t.Fatal(err)
	}
	src.busy = true
	dueNow(t, idx)
	rr, err := c.RepairOnce(ctx)
	if err != nil || !rr.Busy || rr.Ranges != 0 {
		t.Fatalf("repair=%+v err=%v, want Busy", rr, err)
	}
	var attempts int
	if err := idx.db.QueryRow(`SELECT attempts FROM usenet_nntp_missed_ranges`).Scan(&attempts); err != nil || attempts != 0 {
		t.Fatalf("attempts=%d err=%v, want 0", attempts, err)
	}
}

func TestRepair_GateRejectedKeyNeverUpserted(t *testing.T) {
	sqlDB := dbtest.New(t)
	idx, err := OpenIndex(sqlDB)
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()
	ctx := context.Background()
	const group = "alt.binaries.movies"
	rows := releaseRows(1, "Rejected.Movie.2001.720p.WEB-DL.x264-GROUP", "rj", 3)
	src := &gapStub{crawlStub: crawlStub{gr: usenet.GroupRange{Low: 1, High: 3}, rows: rows}}
	snap := &PolicySnapshot{Modes: map[string]Standard{"movies": movieModeStd([]quality.Tier{quality.Medium}, 1080, 0)}}
	c := &Crawler{Src: src, Index: idx, Groups: []string{group}, AdmissionEnabled: true, RepairEnabled: true,
		GroupModesFn: func(string) []string { return []string{"movies"} }, Admitter: newAdmitter(&fakePolicy{snap: snap})}
	if err := idx.AddMissedRanges(ctx, group, [][2]int{{1, 3}}, "gap"); err != nil {
		t.Fatal(err)
	}
	dueNow(t, idx)
	if _, err := c.RepairOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if n, _ := idx.CountHeaders(ctx); n != 0 {
		t.Fatalf("headers=%d want 0: repair resurrected a gate-rejected release", n)
	}
}

func TestRepair_ExistingKeyUpsertedDirectlyEvenIfPolicyNowRejects(t *testing.T) {
	sqlDB := dbtest.New(t)
	idx, err := OpenIndex(sqlDB)
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()
	ctx := context.Background()
	const group = "alt.binaries.movies"
	name := "Rejected.Movie.2001.720p.WEB-DL.x264-GROUP"
	rows := releaseRows(1, name, "ex", 3)
	// Part 1 was admitted earlier (policy then allowed it).
	if err := idx.Upsert(ctx, toHeaderRows(group, rows[:1])); err != nil {
		t.Fatal(err)
	}
	src := &gapStub{crawlStub: crawlStub{gr: usenet.GroupRange{Low: 1, High: 3}, rows: rows}}
	snap := &PolicySnapshot{Modes: map[string]Standard{"movies": movieModeStd([]quality.Tier{quality.Medium}, 1080, 0)}}
	c := &Crawler{Src: src, Index: idx, Groups: []string{group}, AdmissionEnabled: true, RepairEnabled: true,
		GroupModesFn: func(string) []string { return []string{"movies"} }, Admitter: newAdmitter(&fakePolicy{snap: snap})}
	if err := idx.AddMissedRanges(ctx, group, [][2]int{{2, 3}}, "gap"); err != nil {
		t.Fatal(err)
	}
	dueNow(t, idx)
	if _, err := c.RepairOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if n, _ := idx.CountHeaders(ctx); n != 3 {
		t.Fatalf("headers=%d want 3 (existing key bypasses re-admission)", n)
	}
}

func TestRepair_SpanEnqueuedForPendingName(t *testing.T) {
	idx, _, c, group := newGapFixture(t, 100)
	ctx := context.Background()
	key := ObfKey(group, "p@t", `[01/1] - "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.part01.rar" yEnc (1/100)`)
	rows := fileRows(group, key, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.part01.rar", "pn", 1000, 100, 90, 0)
	for i := range rows {
		rows[i].From = "p@t"
		rows[i].Obfuscated = true
		rows[i].ReleaseName = key
	}
	if err := idx.Upsert(ctx, rows); err != nil {
		t.Fatal(err)
	}
	if err := idx.SetWatermark(ctx, group, 500_000); err != nil {
		t.Fatal(err)
	}
	if err := idx.markDirty(ctx, []releaseKey{{group, key}}); err != nil {
		t.Fatal(err)
	}
	if _, err := idx.RecomputeDirty(ctx, 10); err != nil {
		t.Fatal(err)
	}
	var lo, hi int
	if err := idx.db.QueryRow(`SELECT lo, hi FROM usenet_nntp_missed_ranges WHERE reason='span'`).Scan(&lo, &hi); err != nil {
		t.Fatalf("pending span range: %v", err)
	}
	if lo == 0 || hi <= lo {
		t.Fatalf("span=%d-%d", lo, hi)
	}
	n, err := idx.EnqueueStalePendingSpans(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("stale enqueue=%d, want 0 (already span_requeued)", n)
	}
	_ = c
}

func TestRepair_SpanEnqueuedOncePerRelease(t *testing.T) {
	idx, _, c, group := newGapFixture(t, 100)
	ctx := context.Background()
	// Insert a 90/100 release by hand, forward watermark far past it.
	var rows []HeaderRow
	rows = append(rows, fileRows(group, "Span.Rel.2020", "s.mkv", "sp", 1000, 100, 90, 0)...)
	if err := idx.Upsert(ctx, rows); err != nil {
		t.Fatal(err)
	}
	if err := idx.SetWatermark(ctx, group, 500_000); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := idx.markDirty(ctx, []releaseKey{{group, "Span.Rel.2020"}}); err != nil {
			t.Fatal(err)
		}
		if _, err := idx.RecomputeDirty(ctx, 10); err != nil {
			t.Fatal(err)
		}
	}
	var lo, hi int
	if err := idx.db.QueryRow(`SELECT lo, hi FROM usenet_nntp_missed_ranges WHERE reason='span'`).Scan(&lo, &hi); err != nil {
		t.Fatalf("span range: %v", err)
	}
	if lo != 801 || hi != 1290 {
		t.Fatalf("span=%d-%d want 801-1290 (first-200 .. last+200)", lo, hi)
	}
	if n := rangeCount(t, idx); n != 1 {
		t.Fatalf("ranges=%d want exactly 1 (span_requeued once)", n)
	}
	_ = c
}

func TestMissingRanges(t *testing.T) {
	mk := func(nums ...int) []usenet.MessageOverview {
		var out []usenet.MessageOverview
		for _, n := range nums {
			out = append(out, usenet.MessageOverview{Number: n})
		}
		return out
	}
	cases := []struct {
		name string
		rows []usenet.MessageOverview
		a, b int
		want [][2]int
	}{
		{"none", mk(1, 2, 3), 1, 3, nil},
		{"middle", mk(1, 2, 6), 1, 6, [][2]int{{3, 5}}},
		{"head and tail", mk(3, 4), 1, 6, [][2]int{{1, 2}, {5, 6}}},
		{"empty chunk", nil, 10, 12, [][2]int{{10, 12}}},
		{"unsorted + out of range ignored", mk(5, 1, 99), 1, 5, [][2]int{{2, 4}}},
	}
	for _, tc := range cases {
		got := missingRanges(tc.rows, tc.a, tc.b)
		if fmt.Sprint(got) != fmt.Sprint(tc.want) {
			t.Errorf("%s: got %v want %v", tc.name, got, tc.want)
		}
	}
}

func TestRepairBackoff(t *testing.T) {
	if repairBackoff(0).Minutes() != 10 || repairBackoff(1).Minutes() != 20 || repairBackoff(2).Minutes() != 40 {
		t.Fatalf("backoff = %v %v %v", repairBackoff(0), repairBackoff(1), repairBackoff(2))
	}
}

func TestAddMissedRanges_CapDropsOldest(t *testing.T) {
	sqlDB := dbtest.New(t)
	idx, err := OpenIndex(sqlDB)
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()
	ctx := context.Background()
	var rs [][2]int
	for i := 0; i < maxGapRangesPerGroup+5; i++ {
		rs = append(rs, [2]int{i*10 + 1, i*10 + 2})
	}
	if err := idx.AddMissedRanges(ctx, "g", rs, "gap"); err != nil {
		t.Fatal(err)
	}
	var n, minLo int
	if err := sqlDB.QueryRowContext(ctx, `SELECT COUNT(*), MIN(lo) FROM usenet_nntp_missed_ranges WHERE group_name='g'`).Scan(&n, &minLo); err != nil {
		t.Fatal(err)
	}
	if n != maxGapRangesPerGroup || minLo != 5*10+1 {
		t.Fatalf("n=%d minLo=%d want %d / 51", n, minLo, maxGapRangesPerGroup)
	}
}
