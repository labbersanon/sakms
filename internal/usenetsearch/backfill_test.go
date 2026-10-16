package usenetsearch

import (
	"context"
	"testing"

	"github.com/labbersanon/sakms/internal/dbtest"
	"github.com/labbersanon/sakms/internal/quality"
	"github.com/labbersanon/sakms/internal/usenet"
)

type overCall struct {
	group    string
	from, to int
}

// bfStub serves per-group rows/ranges and records every Overview call.
type bfStub struct {
	crawlStub
	rows  map[string][]usenet.MessageOverview
	gr    map[string]usenet.GroupRange
	calls []overCall
	busy  bool
}

func (s *bfStub) GroupRange(_ context.Context, g string) (usenet.GroupRange, error) {
	return s.gr[g], nil
}

func (s *bfStub) Overview(_ context.Context, g string, from, to int) ([]usenet.MessageOverview, error) {
	s.calls = append(s.calls, overCall{g, from, to})
	if s.busy {
		return nil, usenet.ErrBusy
	}
	var out []usenet.MessageOverview
	for _, r := range s.rows[g] {
		if r.Number >= from && r.Number <= to {
			out = append(out, r)
		}
	}
	return out, nil
}

const (
	bfOld = "Old.Movie.2019.1080p.WEB-DL.x264-GROUP"
	bfNew = "New.Movie.2020.1080p.WEB-DL.x264-GROUP"
)

// newBFFixture: each group holds 6000 articles (Old 1-3000, New 3001-6000);
// the forward watermark is preset at 6000 and the backfill cursor at 4001, so
// only history below 4001 is walked.
func newBFFixture(t *testing.T, groups ...string) (*Index, *bfStub, *Crawler) {
	t.Helper()
	sqlDB := dbtest.New(t)
	idx, err := OpenIndex(sqlDB)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { idx.Close() })
	src := &bfStub{rows: map[string][]usenet.MessageOverview{}, gr: map[string]usenet.GroupRange{}}
	ctx := context.Background()
	for _, g := range groups {
		rows := releaseRows(1, bfOld, "o"+g, 3000)
		rows = append(rows, releaseRows(3001, bfNew, "n"+g, 3000)...)
		src.rows[g] = rows
		src.gr[g] = usenet.GroupRange{Low: 1, High: 6000}
		if err := idx.SetWatermark(ctx, g, 6000); err != nil {
			t.Fatal(err)
		}
		if err := idx.EnsureLow(ctx, g, 4001); err != nil {
			t.Fatal(err)
		}
	}
	snap := &PolicySnapshot{Modes: map[string]Standard{"movies": movieModeStd([]quality.Tier{quality.Medium}, 0, 0)}}
	c := &Crawler{Src: src, Index: idx, Groups: groups, AdmissionEnabled: true, BackfillEnabled: true,
		GroupModesFn: func(string) []string { return []string{"movies"} },
		Admitter:     newAdmitter(&fakePolicy{snap: snap})}
	if _, err := c.CrawlOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if len(src.calls) != 0 {
		t.Fatalf("forward crawl OVERed %v; watermark was already at High", src.calls)
	}
	return idx, src, c
}

func TestBackfill_WalksLowDownRoundRobinAndAssemblesAcrossBoundary(t *testing.T) {
	g1, g2 := "alt.binaries.movies", "alt.binaries.multimedia"
	idx, src, c := newBFFixture(t, g1, g2)
	ctx := context.Background()

	res, err := c.BackfillOnce(ctx)
	if err != nil || res.Busy || !res.Advanced || res.Chunks != 4 {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	want := []overCall{{g1, 2001, 4000}, {g2, 2001, 4000}, {g1, 1, 2000}, {g2, 1, 2000}}
	if len(src.calls) != len(want) {
		t.Fatalf("calls=%v want %v", src.calls, want)
	}
	for i := range want {
		if src.calls[i] != want[i] {
			t.Fatalf("call %d=%v want %v (round-robin, 2000-chunks, never above low-1)", i, src.calls[i], want[i])
		}
	}
	for _, g := range []string{g1, g2} {
		low, done, reason, found, err := idx.BackfillState(ctx, g)
		if err != nil || !found || low != 1 || !done || reason != backfillReasonRetention {
			t.Fatalf("%s low=%d done=%v reason=%q found=%v err=%v", g, low, done, reason, found, err)
		}
		if wm, _ := idx.Watermark(ctx, g); wm != 6000 {
			t.Fatalf("%s forward watermark=%d, backfill must never move it", g, wm)
		}
	}

	if _, err := idx.RecomputeDirty(ctx, 500); err != nil {
		t.Fatal(err)
	}
	cs, err := idx.SearchReleases(ctx, Query{Groups: []string{g1}, Terms: []string{"Old"}, Limit: 5})
	if err != nil || len(cs) != 1 || cs[0].SegmentCount != 3000 {
		t.Fatalf("cross-boundary release did not assemble into one candidate: %+v err=%v", cs, err)
	}

	res, err = c.BackfillOnce(ctx)
	if err != nil || res.Chunks != 0 || res.HasWork {
		t.Fatalf("finished groups must idle: %+v err=%v", res, err)
	}
	if len(src.calls) != 4 {
		t.Fatalf("finished groups were OVERed again: %v", src.calls)
	}
}

func TestBackfill_BudgetPausesThenResumes(t *testing.T) {
	g := "alt.binaries.movies"
	idx, src, c := newBFFixture(t, g)
	ctx := context.Background()

	c.budgetOverride = 1 // index is empty at pass start, so one chunk runs, then the budget trips
	res, err := c.BackfillOnce(ctx)
	if err != nil || res.Chunks != 1 || res.HasWork || len(src.calls) != 1 {
		t.Fatalf("res=%+v err=%v calls=%v", res, err, src.calls)
	}
	low, done, reason, _, _ := idx.BackfillState(ctx, g)
	if low != 2001 || done || reason != backfillReasonBudget {
		t.Fatalf("low=%d done=%v reason=%q, want 2001/false/budget", low, done, reason)
	}
	res, err = c.BackfillOnce(ctx)
	if err != nil || res.Chunks != 0 || len(src.calls) != 1 {
		t.Fatalf("still over budget: res=%+v err=%v calls=%v", res, err, src.calls)
	}

	c.budgetOverride = 0
	c.MaxGB = 0
	res, err = c.BackfillOnce(ctx)
	if err != nil || res.Chunks != 1 {
		t.Fatalf("resume res=%+v err=%v", res, err)
	}
	low, done, reason, _, _ = idx.BackfillState(ctx, g)
	if low != 1 || !done || reason != backfillReasonRetention {
		t.Fatalf("low=%d done=%v reason=%q after resume", low, done, reason)
	}
}

func TestBackfill_DaysLimitStopsAtOldChunk(t *testing.T) {
	g := "alt.binaries.movies"
	idx, src, c := newBFFixture(t, g)
	ctx := context.Background()
	c.BackfillDays = 1 // fixture articles are dated 2023

	res, err := c.BackfillOnce(ctx)
	if err != nil || res.Chunks != 1 || len(src.calls) != 1 {
		t.Fatalf("res=%+v err=%v calls=%v", res, err, src.calls)
	}
	low, done, reason, _, _ := idx.BackfillState(ctx, g)
	if low != 2001 || !done || reason != backfillReasonDays {
		t.Fatalf("low=%d done=%v reason=%q, want 2001/true/days", low, done, reason)
	}
}

func TestBackfill_BusyYieldsWithoutMovingCursor(t *testing.T) {
	g := "alt.binaries.movies"
	idx, src, c := newBFFixture(t, g)
	ctx := context.Background()
	src.busy = true

	res, err := c.BackfillOnce(ctx)
	if err != nil || !res.Busy || res.Advanced || res.HasWork {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	low, done, _, _, _ := idx.BackfillState(ctx, g)
	if low != 4001 || done {
		t.Fatalf("low=%d done=%v, a busy yield must not move or finish the cursor", low, done)
	}
}

func TestBackfill_SkippedWhenAdmissionOffOrDisabled(t *testing.T) {
	g := "alt.binaries.movies"
	_, src, c := newBFFixture(t, g)
	ctx := context.Background()

	c.AdmissionEnabled = false
	res, err := c.BackfillOnce(ctx)
	if err != nil || res.Skipped != "admission_off" || len(src.calls) != 0 {
		t.Fatalf("res=%+v err=%v calls=%v", res, err, src.calls)
	}

	c.AdmissionEnabled = true
	c.BackfillEnabled = false
	res, err = c.BackfillOnce(ctx)
	if err != nil || res.Skipped != "" || res.Chunks != 0 || len(src.calls) != 0 {
		t.Fatalf("disabled backfill must be a no-op: res=%+v err=%v calls=%v", res, err, src.calls)
	}
}

func TestBackfill_SkipsFirehoseWithoutNamedIndex(t *testing.T) {
	g1, g2 := "alt.binaries.movies", "alt.binaries.bloaf"
	_, src, c := newBFFixture(t, g1, g2)
	ctx := context.Background()
	res, err := c.BackfillOnce(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res.Filtered != 1 {
		t.Fatalf("Filtered=%d, want 1 (bloaf)", res.Filtered)
	}
	for _, call := range src.calls {
		if call.group == g2 {
			t.Fatalf("bloaf was backfilled: %v", src.calls)
		}
	}
	if res.Chunks == 0 {
		t.Fatal("movies core should still backfill")
	}
}
