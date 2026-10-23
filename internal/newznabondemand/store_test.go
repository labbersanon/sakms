package newznabondemand

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/labbersanon/sakms/internal/dbtest"
	"github.com/labbersanon/sakms/internal/newznab"
	"github.com/labbersanon/sakms/internal/newznabcatalog"
	"github.com/labbersanon/sakms/internal/usenet"
)

func TestStore_NextFromCatalogAndSave(t *testing.T) {
	sqlDB := dbtest.New(t)
	ctx := context.Background()
	cat := newznabcatalog.NewStore(sqlDB)
	if _, err := cat.InsertHits(ctx, newznabcatalog.KindMovies, []newznab.CatalogHit{
		{Indexer: "newznab", GUID: "g1", Title: "Movie.2021.1080p-GRP"},
	}); err != nil {
		t.Fatal(err)
	}
	st := NewStore(sqlDB)
	ref, ok, err := st.NextFromCatalog(ctx)
	if err != nil || !ok || ref.GUID != "g1" {
		t.Fatalf("ref=%+v ok=%v err=%v", ref, ok, err)
	}
	row := Row{
		Indexer: ref.Indexer, GUID: ref.GUID, Kind: ref.Kind, Title: ref.Title,
		Status: StatusFetched, Streamable: true,
		NZBRelPath: filepath.Join("ondemand", "nzb", "newznab", "ab.nzb"),
		Plan: usenet.SequencePlan{Play: []usenet.FilePlan{
			{Name: "Movie.part001.rar", MsgIDs: []string{"<1@x>"}},
		}},
	}
	if err := st.Save(ctx, row); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := st.NextFromCatalog(ctx); err != nil || ok {
		t.Fatalf("second next ok=%v err=%v", ok, err)
	}
	got, ok, err := st.NextFetched(ctx)
	if err != nil || !ok || got.GUID != "g1" || !got.Streamable || len(got.Plan.Play) != 1 {
		t.Fatalf("fetched=%+v ok=%v err=%v", got, ok, err)
	}
}

func TestStore_SaveDropsURLFields(t *testing.T) {
	sqlDB := dbtest.New(t)
	st := NewStore(sqlDB)
	ctx := context.Background()
	if err := st.Save(ctx, Row{
		Indexer: "newznab", GUID: "g1", Status: StatusFetched,
		NZBRelPath: "https://idx/get?apikey=secret",
	}); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := st.NextFetched(ctx); err != nil || ok {
		t.Fatalf("url row must not persist ok=%v err=%v", ok, err)
	}
}

func TestLooksLikeURL(t *testing.T) {
	if !looksLikeURL("apikey=x") || looksLikeURL(filepath.Join("ondemand", "nzb", "newznab", "a.nzb")) {
		t.Fatal("looksLikeURL")
	}
}
