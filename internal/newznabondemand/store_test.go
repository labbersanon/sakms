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

func TestStore_NextFromCatalogRetriesError(t *testing.T) {
	sqlDB := dbtest.New(t)
	ctx := context.Background()
	cat := newznabcatalog.NewStore(sqlDB)
	if _, err := cat.InsertHits(ctx, newznabcatalog.KindMovies, []newznab.CatalogHit{
		{Indexer: "newznab", GUID: "g-err", Title: "Err.Movie"},
	}); err != nil {
		t.Fatal(err)
	}
	st := NewStore(sqlDB)
	if err := st.Save(ctx, Row{Indexer: "newznab", GUID: "g-err", Kind: newznabcatalog.KindMovies, Status: StatusError}); err != nil {
		t.Fatal(err)
	}
	ref, ok, err := st.NextFromCatalog(ctx)
	if err != nil || !ok || ref.GUID != "g-err" {
		t.Fatalf("retry=%+v ok=%v err=%v", ref, ok, err)
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

func TestStore_NextVerifiedNeedingTMDB(t *testing.T) {
	sqlDB := dbtest.New(t)
	ctx := context.Background()
	cat := newznabcatalog.NewStore(sqlDB)
	if _, err := cat.InsertHits(ctx, newznabcatalog.KindMovies, []newznab.CatalogHit{
		{Indexer: "newznab", GUID: "empty", Title: "Empty.1080p", ImdbID: "tt1"},
		{Indexer: "newznab", GUID: "zero", Title: "Zero.1080p", ImdbID: "tt2", TmdbID: "0"},
		{Indexer: "newznab", GUID: "hit", Title: "Hit.1080p", ImdbID: "tt3", TmdbID: "278"},
	}); err != nil {
		t.Fatal(err)
	}
	st := NewStore(sqlDB)
	for _, guid := range []string{"empty", "zero", "hit"} {
		if err := st.Save(ctx, Row{
			Indexer: "newznab", GUID: guid, Kind: newznabcatalog.KindMovies,
			Status: StatusVerified, Streamable: true,
		}); err != nil {
			t.Fatal(err)
		}
	}
	row, ok, err := st.NextVerifiedNeedingTMDB(ctx)
	if err != nil || !ok || row.GUID != "empty" {
		t.Fatalf("row=%+v ok=%v err=%v", row, ok, err)
	}
	if err := st.SetCatalogTMDB(ctx, "newznab", "empty", "9"); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := st.NextVerifiedNeedingTMDB(ctx); err != nil || ok {
		t.Fatalf("after fill ok=%v err=%v", ok, err)
	}
}

func TestStore_StatSkippedIsNotRetried(t *testing.T) {
	sqlDB := dbtest.New(t)
	ctx := context.Background()
	cat := newznabcatalog.NewStore(sqlDB)
	if _, err := cat.InsertHits(ctx, newznabcatalog.KindMovies, []newznab.CatalogHit{
		{Indexer: "newznab2", GUID: "mutiny", Title: "Mutiny.2026.1080p.Blu-ray.Remux.AVC.TrueHD.7.1-HDT"},
	}); err != nil {
		t.Fatal(err)
	}
	st := NewStore(sqlDB)
	if err := st.Save(ctx, Row{
		Indexer: "newznab2", GUID: "mutiny", Kind: newznabcatalog.KindMovies,
		Title:  "Mutiny.2026.1080p.Blu-ray.Remux.AVC.TrueHD.7.1-HDT",
		Status: StatusStatSkipped, RejectReason: "stat budget exceeded",
		PayloadSegments: 32810, Streamable: true,
	}); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := st.NextFromCatalog(ctx); err != nil || ok {
		t.Fatalf("skipped row must not t=get again ok=%v err=%v", ok, err)
	}
	if _, ok, err := st.NextFetched(ctx); err != nil || ok {
		t.Fatalf("skipped row must not STAT again ok=%v err=%v", ok, err)
	}
	row, ok, err := st.NextRequeueableSkip(ctx, usenet.OnDemandStatMaxSegments, onDemandLosslessMbps)
	if err != nil || !ok || row.GUID != "mutiny" {
		t.Fatalf("remux under the cap requeues by size/bitrate row=%+v ok=%v err=%v", row, ok, err)
	}
}

func TestStore_RequeuesSizeSkipUnder4KHighCap(t *testing.T) {
	sqlDB := dbtest.New(t)
	ctx := context.Background()
	st := NewStore(sqlDB)
	if err := st.Save(ctx, Row{
		Indexer: "newznab2", GUID: "omen", Kind: newznabcatalog.KindMovies,
		Title:  "The.Omen.2006.1080p.MA.WEBRip.DDP5.1.x264-ZoroSenpai",
		Status: StatusStatSkipped, Streamable: true, PayloadSegments: 23493,
	}); err != nil {
		t.Fatal(err)
	}
	row, ok, err := st.NextRequeueableSkip(ctx, usenet.OnDemandStatMaxSegments, onDemandLosslessMbps)
	if err != nil || !ok || row.GUID != "omen" {
		t.Fatalf("row=%+v ok=%v err=%v", row, ok, err)
	}
}

func TestStore_DoesNotRequeueLosslessBitrate(t *testing.T) {
	sqlDB := dbtest.New(t)
	ctx := context.Background()
	st := NewStore(sqlDB)
	if err := st.Save(ctx, Row{
		Indexer: "newznab2", GUID: "big", Kind: newznabcatalog.KindMovies,
		Title: "Some.Movie.2021.1080p.WEB-DL", Status: StatusStatSkipped, Streamable: true,
		PayloadSegments: 1000, BitrateMbps: onDemandLosslessMbps,
	}); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := st.NextRequeueableSkip(ctx, usenet.OnDemandStatMaxSegments, onDemandLosslessMbps); err != nil || ok {
		t.Fatalf("lossless bitrate must stay skipped ok=%v err=%v", ok, err)
	}
}

func TestStore_CountByKind(t *testing.T) {
	sqlDB := dbtest.New(t)
	ctx := context.Background()
	st := NewStore(sqlDB)
	for _, row := range []Row{
		{Indexer: "newznab2", GUID: "v1", Kind: newznabcatalog.KindMovies, Title: "A", Status: StatusVerified, Streamable: true},
		{Indexer: "newznab2", GUID: "v2", Kind: newznabcatalog.KindMovies, Title: "B", Status: StatusVerified, Streamable: true},
		{Indexer: "newznab2", GUID: "f1", Kind: newznabcatalog.KindMovies, Title: "C", Status: StatusFetched, Streamable: true},
		{Indexer: "newznab2", GUID: "s1", Kind: newznabcatalog.KindMovies, Title: "D", Status: StatusStatSkipped, Streamable: true},
		{Indexer: "newznab2", GUID: "tv1", Kind: newznabcatalog.KindSeries, Title: "E", Status: StatusFetched, Streamable: true},
	} {
		if err := st.Save(ctx, row); err != nil {
			t.Fatal(err)
		}
	}
	got, err := st.CountByKind(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].Kind != newznabcatalog.KindMovies || got[1].Kind != newznabcatalog.KindSeries || got[2].Kind != newznabcatalog.KindAdult {
		t.Fatalf("kinds=%+v", got)
	}
	if got[0].Verified != 2 || got[0].Fetched != 1 || got[0].Skipped != 1 {
		t.Fatalf("movies=%+v", got[0])
	}
	if got[1].Verified != 0 || got[1].Fetched != 1 || got[1].Skipped != 0 {
		t.Fatalf("series=%+v", got[1])
	}
	if got[2].Verified != 0 || got[2].Fetched != 0 || got[2].Skipped != 0 {
		t.Fatalf("adult=%+v", got[2])
	}
}

func TestStore_CountDeadByIndexer(t *testing.T) {
	sqlDB := dbtest.New(t)
	ctx := context.Background()
	st := NewStore(sqlDB)
	for _, row := range []Row{
		{Indexer: "newznab", GUID: "d1", Kind: newznabcatalog.KindMovies, Title: "Dead.One", Status: StatusDead, StatMissing: 12},
		{Indexer: "newznab", GUID: "d2", Kind: newznabcatalog.KindSeries, Title: "Dead.Two", Status: StatusDead, StatMissing: 3},
		{Indexer: "newznab", GUID: "ok", Kind: newznabcatalog.KindMovies, Title: "Live", Status: StatusVerified, Streamable: true},
		{Indexer: "newznab2", GUID: "ok2", Kind: newznabcatalog.KindMovies, Title: "Also.Live", Status: StatusFetched, Streamable: true},
	} {
		if err := st.Save(ctx, row); err != nil {
			t.Fatal(err)
		}
	}
	got, err := st.CountDeadByIndexer(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Indexer != "newznab" || got[1].Indexer != "newznab2" {
		t.Fatalf("slots=%+v", got)
	}
	if got[0].Nzbs != 2 || got[0].Articles != 15 {
		t.Fatalf("newznab=%+v", got[0])
	}
	if got[1].Nzbs != 0 || got[1].Articles != 0 {
		t.Fatalf("newznab2=%+v", got[1])
	}
}

func TestLooksLikeURL(t *testing.T) {
	if !looksLikeURL("apikey=x") || looksLikeURL(filepath.Join("ondemand", "nzb", "newznab", "a.nzb")) {
		t.Fatal("looksLikeURL")
	}
}
