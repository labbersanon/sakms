package usenetsearch

import (
	"context"
	"testing"

	"github.com/labbersanon/sakms/internal/dbtest"
)

func TestGroupLanguageQuery_AllowsUnmarkedAndPreferred(t *testing.T) {
	cases := []struct {
		group     string
		preferred []string
		want      bool
	}{
		{"alt.binaries.moovee", nil, true},
		{"alt.binaries.dvd.german", nil, false},
		{"alt.binaries.dvd.german", []string{"german"}, true},
		{"alt.binaries.dvd.french.d", []string{"english"}, false},
		{"alt.binaries.teevee", []string{"english"}, true},
		{"alt.binaries.dvd.german.english", []string{"english"}, true},
	}
	for _, tc := range cases {
		got := languageAllowed(groupLanguageQuery(tc.group), tc.preferred, true)
		if got != tc.want {
			t.Errorf("group=%s pref=%v got=%v want=%v", tc.group, tc.preferred, got, tc.want)
		}
	}
}

func TestLanguageAllowed_FilterOffAlwaysAllows(t *testing.T) {
	if !languageAllowed("Show.S01E01.GERMAN.720p", nil, false) {
		t.Fatal("filter off must not reject")
	}
}

func TestPruneGroupWrongLanguage(t *testing.T) {
	sqlDB := dbtest.New(t)
	idx, err := OpenIndex(sqlDB)
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()
	ctx := context.Background()
	const group = "alt.binaries.moovee"
	if err := idx.Upsert(ctx, []HeaderRow{
		{Group: group, MsgNum: 1, MsgID: "en@x", Subject: "x", ReleaseName: "Some.Movie.2020.1080p.WEB-DL.x264-GROUP", PartN: 1, PartM: 1, Filename: "a.mkv", Bytes: 1, PostedAt: 1},
		{Group: group, MsgNum: 2, MsgID: "de@x", Subject: "x", ReleaseName: "Some.Movie.2020.GERMAN.1080p.WEB-DL.x264-GROUP", PartN: 1, PartM: 1, Filename: "b.mkv", Bytes: 1, PostedAt: 1},
	}); err != nil {
		t.Fatal(err)
	}
	n, err := pruneGroupWrongLanguage(ctx, idx, group, nil)
	if err != nil || n != 1 {
		t.Fatalf("pruned=%d err=%v want 1", n, err)
	}
	count, err := idx.CountHeaders(ctx)
	if err != nil || count != 1 {
		t.Fatalf("count=%d err=%v want 1", count, err)
	}
}

func TestSearch_FiltersNonPreferredLanguage(t *testing.T) {
	sqlDB := dbtest.New(t)
	idx, err := OpenIndex(sqlDB)
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()
	ctx := context.Background()
	const group = "g"
	if err := idx.Upsert(ctx, []HeaderRow{
		{Group: group, MsgNum: 1, MsgID: "en@x", Subject: "Foo.Bar.2024.mkv yEnc (1/1)", ReleaseName: "Foo.Bar.2024", PartN: 1, PartM: 1, Filename: "Foo.Bar.2024.mkv", Bytes: 1000, PostedAt: 1700000000},
		{Group: group, MsgNum: 2, MsgID: "de@x", Subject: "Foo.Bar.2024.GERMAN.mkv yEnc (1/1)", ReleaseName: "Foo.Bar.2024.GERMAN", PartN: 1, PartM: 1, Filename: "Foo.Bar.2024.GERMAN.mkv", Bytes: 1000, PostedAt: 1700000000},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := idx.RecomputeDirty(ctx, 100); err != nil {
		t.Fatal(err)
	}
	s := NewService(nil, sqlDB)
	s.idx = idx
	s.cfg.MoviesGroups = []string{group}
	s.SetPreferredLanguagesFn(func(context.Context) []string { return nil })
	cs, err := s.Search(ctx, Query{Terms: []string{"Foo"}, Groups: []string{group}, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(cs) != 1 || cs[0].Name != "Foo.Bar.2024" {
		t.Fatalf("got %+v", cs)
	}
}
