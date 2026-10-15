package usenetsearch

import (
	"context"
	"testing"

	"github.com/labbersanon/sakms/internal/dbtest"
)

func TestDemoteGroup_CapsAtFive(t *testing.T) {
	sqlDB := dbtest.New(t)
	idx, err := OpenIndex(sqlDB)
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()
	ctx := context.Background()
	var g int
	for i := 0; i < 8; i++ {
		g, err = idx.DemoteGroup(ctx, "movies", "alt.binaries.moovee")
		if err != nil {
			t.Fatal(err)
		}
	}
	if g != 5 {
		t.Fatalf("grade=%d want 5", g)
	}
}

func TestEligibleAndPruneBeyondCeiling(t *testing.T) {
	sqlDB := dbtest.New(t)
	idx, err := OpenIndex(sqlDB)
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()
	ctx := context.Background()
	group := "alt.binaries.moovee"
	if err := idx.Upsert(ctx, []HeaderRow{{
		Group: group, MsgNum: 1, MsgID: "a@x", Subject: "x", ReleaseName: "X",
		PartN: 1, PartM: 1, Filename: "x.mkv", Bytes: 1, PostedAt: 1,
	}}); err != nil {
		t.Fatal(err)
	}
	// Demote to grade 3
	for i := 0; i < 2; i++ {
		if _, err := idx.DemoteGroup(ctx, "movies", group); err != nil {
			t.Fatal(err)
		}
	}
	elig, err := idx.EligibleGroups(ctx, "movies", 2, []string{group})
	if err != nil {
		t.Fatal(err)
	}
	if len(elig) != 0 {
		t.Fatalf("grade 3 should be ineligible at ceiling 2: %+v", elig)
	}
	// Untouched groups stay grade 1 and remain eligible.
	okElig, err := idx.EligibleGroups(ctx, "movies", 2, []string{"alt.binaries.movies"})
	if err != nil || len(okElig) != 1 {
		t.Fatalf("untouched group should stay eligible: %+v err=%v", okElig, err)
	}
	if err := idx.PruneGroupsBeyondCeiling(ctx, "movies", 2, []string{group}); err != nil {
		t.Fatal(err)
	}
	n, err := idx.CountHeaders(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("headers=%d want 0 after prune", n)
	}
}

func TestGetGrade_DefaultOne(t *testing.T) {
	sqlDB := dbtest.New(t)
	idx, err := OpenIndex(sqlDB)
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()
	g, err := idx.GetGrade(context.Background(), "series", "alt.binaries.teevee")
	if err != nil || g != 1 {
		t.Fatalf("grade=%d err=%v", g, err)
	}
}
