package usenetsearch

import (
	"context"
	"testing"

	"github.com/labbersanon/sakms/internal/dbtest"
)

func clearDemoteCooldown(t *testing.T, idx *Index, mode, group string) {
	t.Helper()
	_, err := idx.db.ExecContext(context.Background(),
		`UPDATE usenet_nntp_group_grades SET last_failed_at=0 WHERE mode=? AND group_name=?`, mode, group)
	if err != nil {
		t.Fatal(err)
	}
}

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
		if i > 0 {
			clearDemoteCooldown(t, idx, "movies", "alt.binaries.moovee")
		}
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
		if i > 0 {
			clearDemoteCooldown(t, idx, "movies", group)
		}
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
	n, err := idx.DeleteGroupIndex(ctx, group)
	if err != nil || n == 0 {
		t.Fatalf("DeleteGroupIndex n=%d err=%v", n, err)
	}
	left, err := idx.CountHeaders(ctx)
	if err != nil || left != 0 {
		t.Fatalf("headers=%d want 0 after delete", left)
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

func TestNoteSuccess_PromotesAfterTwo(t *testing.T) {
	sqlDB := dbtest.New(t)
	idx, err := OpenIndex(sqlDB)
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()
	ctx := context.Background()
	group := "alt.binaries.teevee"
	// Demote to 3 first so promote is observable.
	for i := 0; i < 2; i++ {
		if i > 0 {
			clearDemoteCooldown(t, idx, "series", group)
		}
		if _, err := idx.DemoteGroup(ctx, "series", group); err != nil {
			t.Fatal(err)
		}
	}
	g, promoted, err := idx.NoteSuccess(ctx, "series", group)
	if err != nil || promoted || g != 3 {
		t.Fatalf("first success: grade=%d promoted=%v err=%v", g, promoted, err)
	}
	g, promoted, err = idx.NoteSuccess(ctx, "series", group)
	if err != nil || !promoted || g != 2 {
		t.Fatalf("second success: grade=%d promoted=%v err=%v want grade 2 promoted", g, promoted, err)
	}
	// Demote resets streak — need two more successes.
	if _, err := idx.DemoteGroup(ctx, "series", group); err != nil {
		t.Fatal(err)
	}
	_, promoted, err = idx.NoteSuccess(ctx, "series", group)
	if err != nil || promoted {
		t.Fatalf("streak reset: promoted=%v err=%v", promoted, err)
	}
}
