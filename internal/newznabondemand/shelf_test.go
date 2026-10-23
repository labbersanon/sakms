package newznabondemand

import (
	"context"
	"testing"

	"github.com/labbersanon/sakms/internal/dbtest"
	"github.com/labbersanon/sakms/internal/newznab"
	"github.com/labbersanon/sakms/internal/newznabcatalog"
)

func TestDisplayTitle(t *testing.T) {
	cases := map[string]string{
		"Same.Movie.2021.1080p.WEB-DL.x264-GRP": "Same Movie 2021",
		"Some.Show.S01E02.1080p":                "Some Show S01E02",
		"Studio.Scene.2160p":                    "Studio Scene",
	}
	for in, want := range cases {
		if got := DisplayTitle(in); got != want {
			t.Errorf("DisplayTitle(%q)=%q want %q", in, got, want)
		}
	}
}

func TestQualityLabel(t *testing.T) {
	if QualityLabel("Movie.2021.2160p.WEB-DL") != "2160p" {
		t.Fatal(QualityLabel("Movie.2021.2160p.WEB-DL"))
	}
	if QualityLabel("Movie.2021.1080p") != "1080p" {
		t.Fatal(QualityLabel("Movie.2021.1080p"))
	}
}

func TestListVerified_GroupsByIMDBAndQuality(t *testing.T) {
	sqlDB := dbtest.New(t)
	ctx := context.Background()
	cat := newznabcatalog.NewStore(sqlDB)
	if _, err := cat.InsertHits(ctx, newznabcatalog.KindMovies, []newznab.CatalogHit{
		{Indexer: "newznab", GUID: "https://nzbgeek.info/geekseek.php?guid=aaa111", Title: "Same.Movie.2021.1080p-GRP", ImdbID: "tt0111161", TmdbID: "278", Year: 2021},
		{Indexer: "newznab2", GUID: "https://api.nzb.life/details/bbb222", Title: "Same.Movie.2021.2160p-GRP", ImdbID: "tt0111161", TmdbID: "278", Year: 2021},
		{Indexer: "newznab", GUID: "other", Title: "Other.Movie.2020.720p-GRP", ImdbID: "tt2", Year: 2020},
	}); err != nil {
		t.Fatal(err)
	}
	st := NewStore(sqlDB)
	for _, row := range []Row{
		{Indexer: "newznab", GUID: "https://nzbgeek.info/geekseek.php?guid=aaa111", Kind: newznabcatalog.KindMovies, Title: "Same.Movie.2021.1080p-GRP", Status: StatusVerified, Streamable: true},
		{Indexer: "newznab2", GUID: "https://api.nzb.life/details/bbb222", Kind: newznabcatalog.KindMovies, Title: "Same.Movie.2021.2160p-GRP", Status: StatusVerified, Streamable: true},
		{Indexer: "newznab", GUID: "other", Kind: newznabcatalog.KindMovies, Title: "Other.Movie.2020.720p-GRP", Status: StatusFetched, Streamable: true},
	} {
		if err := st.Save(ctx, row); err != nil {
			t.Fatal(err)
		}
	}
	got, err := st.ListVerified(ctx, newznabcatalog.KindMovies)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d titles: %+v", len(got), got)
	}
	if got[0].Title != "Same Movie 2021" || got[0].ImdbID != "tt0111161" || got[0].TmdbID != 278 || got[0].Year != 2021 {
		t.Fatalf("title=%+v", got[0])
	}
	if len(got[0].Qualities) != 2 {
		t.Fatalf("qualities=%+v", got[0].Qualities)
	}
	if got[0].Qualities[0].Label != "2160p" || got[0].Qualities[1].Label != "1080p" {
		t.Fatalf("order=%+v", got[0].Qualities)
	}
	if got[0].Qualities[1].NzbID != "aaa111" || got[0].Qualities[0].NzbID != "bbb222" {
		t.Fatalf("nzb ids=%+v", got[0].Qualities)
	}
	for _, q := range got[0].Qualities {
		if looksLikeURL(q.NzbID) || looksLikeURL(q.Indexer) {
			t.Fatalf("url leaked: %+v", q)
		}
	}
}

func TestListVerified_SeriesKeepsEpisodesApart(t *testing.T) {
	sqlDB := dbtest.New(t)
	ctx := context.Background()
	cat := newznabcatalog.NewStore(sqlDB)
	if _, err := cat.InsertHits(ctx, newznabcatalog.KindSeries, []newznab.CatalogHit{
		{Indexer: "newznab", GUID: "e1", Title: "Some.Show.S01E01.1080p", TvdbID: "123"},
		{Indexer: "newznab", GUID: "e2", Title: "Some.Show.S01E02.1080p", TvdbID: "123"},
	}); err != nil {
		t.Fatal(err)
	}
	st := NewStore(sqlDB)
	_ = st.Save(ctx, Row{Indexer: "newznab", GUID: "e1", Kind: newznabcatalog.KindSeries, Title: "Some.Show.S01E01.1080p", Status: StatusVerified, Streamable: true})
	_ = st.Save(ctx, Row{Indexer: "newznab", GUID: "e2", Kind: newznabcatalog.KindSeries, Title: "Some.Show.S01E02.1080p", Status: StatusVerified, Streamable: true})
	got, err := st.ListVerified(ctx, newznabcatalog.KindSeries)
	if err != nil || len(got) != 2 {
		t.Fatalf("got=%+v err=%v", got, err)
	}
}
