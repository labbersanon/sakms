package usenetsearch

import (
	"context"
	"testing"

	"github.com/labbersanon/sakms/internal/dbtest"
	"github.com/labbersanon/sakms/internal/usenet"
)

func TestGapFillStatus_CountsAndBackfillCursor(t *testing.T) {
	g := "alt.binaries.movies"
	idx, _, c := newBFFixture(t, g)
	ctx := context.Background()
	if _, err := c.BackfillOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := idx.RecomputeDirty(ctx, 500); err != nil {
		t.Fatal(err)
	}
	if err := idx.AddMissedRanges(ctx, g, [][2]int{{10, 12}}, "gap"); err != nil {
		t.Fatal(err)
	}

	if _, err := idx.db.Exec(`UPDATE usenet_nntp_releases SET searchable=FALSE WHERE display_name=?`, bfNew); err != nil {
		t.Fatal(err)
	}

	st, err := idx.gapFillStatus(ctx, map[string]usenet.GroupRange{g: {Low: 1, High: 6000}})
	if err != nil {
		t.Fatal(err)
	}
	if st.ReleasesSearchable != 1 || st.ReleasesBelowGate != 1 {
		t.Fatalf("searchable=%d belowGate=%d want 1/1", st.ReleasesSearchable, st.ReleasesBelowGate)
	}
	if st.MissedRanges != 1 || st.PendingName != 0 {
		t.Fatalf("status=%+v", st)
	}
	if st.NamedBySource["subject"] != 2 {
		t.Fatalf("namedBySource=%v", st.NamedBySource)
	}
	if len(st.Backfill) != 1 || st.Backfill[0] != (BackfillGroupStatus{Group: g, Low: 1, Target: 1, Done: true, Reason: backfillReasonRetention}) {
		t.Fatalf("backfill=%+v", st.Backfill)
	}
}

func TestGapFillStatus_NoIndexIsZero(t *testing.T) {
	st, err := NewService(nil, nil).GapFillStatus(context.Background())
	if err != nil || st.ReleasesSearchable != 0 || st.NamedBySource == nil || st.Backfill == nil {
		t.Fatalf("st=%+v err=%v", st, err)
	}
}

func TestCrawlerStatus_DisabledZeros(t *testing.T) {
	st, err := NewService(nil, nil).CrawlerStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.Enabled || st.Headers != 0 || st.LastChunk != nil {
		t.Fatalf("disabled snapshot=%+v", st)
	}
	if st.IndexMaxGB != DefaultIndexMaxGB {
		t.Fatalf("indexMaxGb=%d", st.IndexMaxGB)
	}
}

func TestIdentifiedStats_BinarySceneVsPictures(t *testing.T) {
	sqlDB := dbtest.New(t)
	idx, err := OpenIndex(sqlDB)
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()
	ctx := context.Background()
	const g = "alt.binaries.movies"
	const p = "alt.binaries.pictures.erotica"
	var rows []HeaderRow
	rows = append(rows, fileRows(g, "Full.Movie.2020.1080p", "full.mkv", "full", 0, 10, 10, 0)...)
	rows = append(rows, fileRows(p, "20260905", "a.jpg", "pic", 0, 2, 2, 0)...)
	rows = append(rows, fileRows(g, "obf:abcdabcdabcdabcd", "x.rar", "obf", 100, 5, 5, 0)...)
	rows = append(rows, fileRows(g, "(????)", "q.mkv", "q", 200, 3, 3, 0)...)
	rows = append(rows, fileRows(g, "SubsPlease LIAR GAME 720p Batch", "b.mkv", "b", 300, 4, 4, 0)...)
	if err := idx.Upsert(ctx, rows); err != nil {
		t.Fatal(err)
	}
	if _, err := idx.RecomputeDirty(ctx, 100); err != nil {
		t.Fatal(err)
	}
	id, bin, err := idx.identifiedStats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if bin != 19 || id != 14 {
		t.Fatalf("identified=%d binary=%d want 14/19 (pictures+junk excluded, batch+scene named, pending in binary)", id, bin)
	}
}

func TestCrawlerStatus_LastChunk(t *testing.T) {
	s := NewService(nil, nil)
	s.lastChunk = CrawlChunkStat{Group: "alt.binaries.moovee", Rows: 10, Admitted: 2}
	st, err := s.CrawlerStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.LastChunk == nil || st.LastChunk.Group != "alt.binaries.moovee" || st.LastChunk.Admitted != 2 {
		t.Fatalf("lastChunk=%+v", st.LastChunk)
	}
}
