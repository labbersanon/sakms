package usenetsearch

import (
	"context"
	"testing"

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
